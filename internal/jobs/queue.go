package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/semconv"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const DefaultLeaseDuration = 30 * time.Second

type Queue struct {
	pool          *pgxpool.Pool
	leaseDuration time.Duration
	traceSink     agent.TraceSink
}

type ClaimedJob struct {
	ID         string
	RunID      string
	AttemptNo  int
	LeaseToken string
}

func NewQueue(pool *pgxpool.Pool) *Queue {
	return &Queue{pool: pool, leaseDuration: DefaultLeaseDuration}
}

func NewQueueWithTraceSink(pool *pgxpool.Pool, sink agent.TraceSink) *Queue {
	return &Queue{pool: pool, leaseDuration: DefaultLeaseDuration, traceSink: sink}
}

func (q *Queue) ClaimNext(ctx context.Context) (ClaimedJob, bool, error) {
	for {
		tx, err := q.pool.Begin(ctx)
		if err != nil {
			return ClaimedJob{}, false, err
		}
		defer tx.Rollback(ctx)
		traceCtx := ctx
		var traceScope *agent.TraceScope
		if q.traceSink != nil {
			traceScope, err = agent.NewTraceScope(q.traceSink)
			if err != nil {
				return ClaimedJob{}, false, err
			}
			defer traceScope.Rollback()
			traceCtx = agent.ContextWithTraceScope(ctx, traceScope)
		}
		if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
			return ClaimedJob{}, false, err
		}
		if _, err := agent.NewStore(tx).ExpireIfOverdue(traceCtx, "", ""); err != nil {
			return ClaimedJob{}, false, err
		}

		var job ClaimedJob
		var status string
		err = tx.QueryRow(ctx, `
			select j.id, j.run_id, j.status, j.attempt_no
			from agent_jobs j
			join agent_runs r on r.id = j.run_id
			where (j.status = 'queued' and r.status = 'queued')
				or (j.status = 'running' and r.status = 'running' and j.lease_expires_at <= now())
			order by j.created_at, j.id
			for update of r, j skip locked
			limit 1`).Scan(&job.ID, &job.RunID, &status, &job.AttemptNo)
		if errors.Is(err, pgx.ErrNoRows) {
			if err := tx.Commit(ctx); err != nil {
				return ClaimedJob{}, false, err
			}
			if traceScope != nil {
				_ = traceScope.PublishAfterCommit(traceCtx)
			}
			return ClaimedJob{}, false, nil
		}
		if err != nil {
			return ClaimedJob{}, false, err
		}

		if status == "running" && job.AttemptNo >= 3 {
			if err := exhaustRecovery(traceCtx, tx, job); err != nil {
				return ClaimedJob{}, false, err
			}
			if err := tx.Commit(ctx); err != nil {
				return ClaimedJob{}, false, err
			}
			if traceScope != nil {
				_ = traceScope.PublishAfterCommit(traceCtx)
			}
			continue
		}

		previousAttemptNo := job.AttemptNo
		job.AttemptNo++
		job.LeaseToken = uuid.NewString()
		jobTag, err := tx.Exec(ctx, `
			update agent_jobs
			set status = 'running',
				attempt_no = $2,
				lease_token = $3,
				lease_expires_at = now() + ($4 * interval '1 second'),
				started_at = coalesce(started_at, now()),
				updated_at = now()
			where id = $1`, job.ID, job.AttemptNo, job.LeaseToken, q.leaseDuration.Seconds())
		if err != nil {
			return ClaimedJob{}, false, err
		}
		runTag, err := tx.Exec(ctx, `
			update agent_runs
			set status = 'running', started_at = coalesce(started_at, now()), updated_at = now()
			where id = $1 and status in ('queued', 'running')`, job.RunID)
		if err != nil {
			return ClaimedJob{}, false, err
		}
		if jobTag.RowsAffected() != 1 || runTag.RowsAffected() != 1 {
			return ClaimedJob{}, false, errors.New("claimable Job and Run did not transition together")
		}
		if status == "queued" {
			if _, err := tx.Exec(ctx, `select pg_notify('nano_agent_runs', $1)`, job.RunID); err != nil {
				return ClaimedJob{}, false, err
			}
		}
		traceRecorder, err := agent.NewRunTraceRecorder(traceCtx, tx, job.RunID)
		if err != nil {
			return ClaimedJob{}, false, err
		}
		tracer, err := agentobs.NewTracer(agentobs.TracerConfig{
			Recorder: traceRecorder, SemanticConventionVersion: agent.TraceSemanticConventionVersion,
		})
		if err != nil {
			return ClaimedJob{}, false, err
		}
		rootContext := agentobs.ContextWithSpanContext(traceCtx, traceRecorder.RootSpanContext())
		var priorAttempt agentobs.SpanContext
		if status == "running" {
			priorIdentity := agent.TraceAttemptStartIdentity(job.RunID, previousAttemptNo)
			priorAttempt, err = traceRecorder.SpanContextByIdentity(traceCtx, priorIdentity)
			if err != nil {
				return ClaimedJob{}, false, err
			}
			priorContext := agentobs.ContextWithSpanContext(ctx, priorAttempt)
			if err := tracer.Event(priorContext, agentobs.Event{
				IdentityKey: fmt.Sprintf("run/%s/attempt/%d/lease-expired", job.RunID, previousAttemptNo),
				Name:        agent.TraceEventLeaseExpired,
				Attributes: []agentobs.Attribute{
					agentobs.String(agent.TraceKeyJobID, job.ID),
					agentobs.Int64(agent.TraceKeyAttemptNumber, int64(previousAttemptNo)),
				},
			}); err != nil {
				return ClaimedJob{}, false, err
			}
		}
		attemptContext, _, err := tracer.StartSpan(rootContext, agentobs.SpanStart{
			IdentityKey: agent.TraceAttemptStartIdentity(job.RunID, job.AttemptNo),
			Name:        agent.TraceSpanJobAttempt,
			Attributes: []agentobs.Attribute{
				agentobs.String(agent.TraceKeyJobID, job.ID),
				agentobs.Int64(agent.TraceKeyAttemptNumber, int64(job.AttemptNo)),
			},
		})
		if err != nil {
			return ClaimedJob{}, false, err
		}
		if status == "running" {
			if err := tracer.Link(attemptContext, agentobs.Link{
				IdentityKey: fmt.Sprintf("run/%s/attempt/%d/continues", job.RunID, job.AttemptNo),
				Name:        semconv.LinkContinues,
				Target:      priorAttempt,
			}); err != nil {
				return ClaimedJob{}, false, err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return ClaimedJob{}, false, err
		}
		if traceScope != nil {
			_ = traceScope.PublishAfterCommit(traceCtx)
		}
		return job, true, nil
	}
}

func (q *Queue) Heartbeat(ctx context.Context, jobID, leaseToken string, leaseDuration time.Duration) (bool, error) {
	if leaseDuration <= 0 {
		leaseDuration = q.leaseDuration
	}
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `
		update agent_jobs
		set lease_expires_at = now() + ($3 * interval '1 second'), updated_at = now()
		where id = $1
			and status = 'running'
			and lease_token = $2
			and lease_expires_at > now()`, jobID, leaseToken, leaseDuration.Seconds())
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (q *Queue) ReleaseLease(ctx context.Context, jobID, leaseToken string) (bool, error) {
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `
		update agent_jobs
		set lease_expires_at = now(), updated_at = now()
		where id = $1 and status = 'running' and lease_token = $2::uuid`, jobID, leaseToken)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 1 {
		if _, err := tx.Exec(ctx, `select pg_notify('nano_agent_jobs', $1)`, jobID); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func exhaustRecovery(ctx context.Context, tx pgx.Tx, job ClaimedJob) error {
	jobTag, err := tx.Exec(ctx, `
		update agent_jobs
		set status = 'failed', lease_token = null, lease_expires_at = null,
			finished_at = now(), updated_at = now()
		where id = $1 and status = 'running'`, job.ID)
	if err != nil {
		return err
	}
	runTag, err := tx.Exec(ctx, `
		update agent_runs
		set status = 'failed', error_code = 'recovery_exhausted',
			finished_at = now(), updated_at = now()
		where id = $1 and status = 'running' and output_message_id is null`, job.RunID)
	if err != nil {
		return err
	}
	if jobTag.RowsAffected() != 1 || runTag.RowsAffected() != 1 {
		return errors.New("recovery exhaustion did not transition Run and Job together")
	}
	if err := agent.RecordAttemptLeaseExpiredInTx(ctx, tx, job.RunID, job.ID, job.AttemptNo); err != nil {
		return err
	}
	if err := agent.RecordRunTerminalInTx(ctx, tx, job.RunID, agent.RunTerminalTrace{
		CauseEvent: agent.TraceEventRecoveryExhausted,
		RunStatus:  "failed",
		SpanStatus: agentobs.StatusError,
		ErrorCode:  "recovery_exhausted",
	}); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `select pg_notify('nano_agent_runs', $1)`, job.RunID)
	return err
}

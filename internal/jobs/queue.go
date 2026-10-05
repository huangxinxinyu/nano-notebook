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
	metrics       *agent.TaskMetricsRecorder
}

type ClaimedJob struct {
	ID                 string
	RunID              string
	AttemptNo          int
	LeaseToken         string
	MaxAttempts        int
	FailedAttempts     int
	DeadlineAt         time.Time
	DefinitionIdentity string
	DefinitionVersion  int
	AdmittedAt         time.Time
}

// MaxAgentLeases bounds every lease of one Agent Job, including resumptions
// after waits, and matches the agent_jobs attempt_no constraint.
const MaxAgentLeases = 100

func NewQueue(pool *pgxpool.Pool) *Queue {
	return &Queue{pool: pool, leaseDuration: DefaultLeaseDuration}
}

func NewQueueWithTraceSink(pool *pgxpool.Pool, sink agent.TraceSink) *Queue {
	return &Queue{pool: pool, leaseDuration: DefaultLeaseDuration, traceSink: sink}
}

// WithMetrics attaches a Task metrics recorder and returns the same Queue,
// so callers can chain it onto either constructor above without another
// signature variant.
func (q *Queue) WithMetrics(recorder *agent.TaskMetricsRecorder) *Queue {
	q.metrics = recorder
	return q
}

func (q *Queue) ClaimNext(ctx context.Context) (ClaimedJob, bool, error) {
	for {
		tx, err := q.pool.Begin(ctx)
		if err != nil {
			return ClaimedJob{}, false, err
		}
		defer tx.Rollback(ctx)
		sink := agent.TraceSink(agent.DiscardTraceSink{})
		if q.traceSink != nil {
			sink = q.traceSink
		}
		traceScope, err := agent.NewTraceScope(sink)
		if err != nil {
			return ClaimedJob{}, false, err
		}
		defer traceScope.Rollback()
		traceCtx := agent.ContextWithTraceScope(ctx, traceScope)
		if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
			return ClaimedJob{}, false, err
		}
		if _, err := agent.NewStore(tx).ExpireIfOverdue(traceCtx, "", ""); err != nil {
			return ClaimedJob{}, false, err
		}

		var job ClaimedJob
		var status string
		var availableAt time.Time
		var definitionIdentity *string
		var definitionVersion *int
		err = tx.QueryRow(ctx, `
			select j.id, j.run_id, j.status, j.attempt_no, j.failed_attempts, coalesce(j.lease_token::text,''),
				coalesce(profile.max_attempts,(definition.limits->>'attempts')::integer),
				coalesce(r.deadline_at,tree.absolute_deadline),
				j.available_at, r.definition_identity, r.definition_version, r.created_at
			from agent_jobs j
			join agent_runs r on r.id = j.run_id
			left join agent_role_profiles profile on profile.configuration_set_id=r.agent_config_id and profile.role=r.agent_role
			left join agent_definition_versions definition on definition.definition_identity=r.definition_identity and definition.definition_version=r.definition_version
			left join agent_trees tree on tree.id=r.tree_id
			where (j.status = 'queued' and j.available_at <= now() and r.status = 'queued')
				or (j.status = 'running' and r.status = 'running' and j.lease_expires_at <= now())
			order by j.available_at, j.created_at, j.id
			for update of r, j skip locked
			limit 1`).Scan(&job.ID, &job.RunID, &status, &job.AttemptNo, &job.FailedAttempts, &job.LeaseToken, &job.MaxAttempts, &job.DeadlineAt,
			&availableAt, &definitionIdentity, &definitionVersion, &job.AdmittedAt)
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
		if definitionIdentity != nil {
			job.DefinitionIdentity = *definitionIdentity
		}
		if definitionVersion != nil {
			job.DefinitionVersion = *definitionVersion
		}
		taskKind, taskVariant := agent.ClassifyTask(job.DefinitionIdentity, job.DefinitionVersion)

		// A running Job claimed here lost its lease, which counts as a failed
		// Attempt; resumptions after a wait do not.
		leaseLost := status == "running"
		if leaseLost && (job.FailedAttempts+1 >= job.MaxAttempts || job.AttemptNo >= MaxAgentLeases) ||
			!leaseLost && job.AttemptNo >= MaxAgentLeases {
			if err := exhaustRecovery(traceCtx, tx, job); err != nil {
				return ClaimedJob{}, false, err
			}
			if err := tx.Commit(ctx); err != nil {
				return ClaimedJob{}, false, err
			}
			if traceScope != nil {
				_ = traceScope.PublishAfterCommit(traceCtx)
			}
			q.metrics.RecordAttempt(taskKind, taskVariant, string(agent.AttemptTerminal))
			q.metrics.RecordTerminal(taskKind, taskVariant, "expired", job.AttemptNo, job.AdmittedAt)
			q.metrics.RecordError(taskKind, "lifecycle", "recovery_exhausted")
			continue
		}
		q.metrics.RecordQueueWait(taskKind, taskVariant, time.Since(availableAt))

		previousAttemptNo := job.AttemptNo
		job.AttemptNo++
		if leaseLost {
			job.FailedAttempts++
		}
		job.LeaseToken = uuid.NewString()
		jobTag, err := tx.Exec(ctx, `
			update agent_jobs
			set status = 'running',
				attempt_no = $2,
				failed_attempts = $5,
				lease_token = $3,
				lease_expires_at = now() + ($4 * interval '1 second'),
				started_at = coalesce(started_at, now()),
				updated_at = now()
			where id = $1`, job.ID, job.AttemptNo, job.LeaseToken, q.leaseDuration.Seconds(), job.FailedAttempts)
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
		continuesPriorAttempt := status == "running"
		retriesPriorAttempt := status == "queued" && previousAttemptNo > 0
		if continuesPriorAttempt || retriesPriorAttempt {
			priorIdentity := agent.TraceAttemptStartIdentity(job.RunID, previousAttemptNo)
			priorAttempt, err = traceRecorder.SpanContextByIdentity(traceCtx, priorIdentity)
			if err != nil {
				return ClaimedJob{}, false, err
			}
		}
		if continuesPriorAttempt {
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
		if continuesPriorAttempt || retriesPriorAttempt {
			linkName := semconv.LinkContinues
			identitySuffix := "continues"
			if retriesPriorAttempt {
				linkName = semconv.LinkRetries
				identitySuffix = "retries"
			}
			if err := tracer.Link(attemptContext, agentobs.Link{
				IdentityKey: fmt.Sprintf("run/%s/attempt/%d/%s", job.RunID, job.AttemptNo, identitySuffix),
				Name:        linkName,
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

func (q *Queue) ResolveAttempt(ctx context.Context, job ClaimedJob, requested agent.AttemptResolution) (agent.AttemptResolution, error) {
	if !requested.Valid() {
		return agent.AttemptResolution{}, errors.New("invalid Attempt disposition")
	}
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return agent.AttemptResolution{}, err
	}
	defer tx.Rollback(ctx)
	sink := agent.TraceSink(agent.DiscardTraceSink{})
	if q.traceSink != nil {
		sink = q.traceSink
	}
	traceScope, err := agent.NewTraceScope(sink)
	if err != nil {
		return agent.AttemptResolution{}, err
	}
	defer traceScope.Rollback()
	traceCtx := agent.ContextWithTraceScope(ctx, traceScope)
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return agent.AttemptResolution{}, err
	}
	var status string
	var deadline time.Time
	var maxAttempts, failedAttempts int
	var currentLease *string
	var storedErrorCode string
	if err := tx.QueryRow(ctx, `
		select j.status,coalesce(r.deadline_at,tree.absolute_deadline),
			coalesce(profile.max_attempts,(definition.limits->>'attempts')::integer),j.lease_token::text,
			coalesce(j.last_error_code,r.error_code,'already_terminal'),j.failed_attempts
		from agent_jobs j
		join agent_runs r on r.id=j.run_id
		left join agent_role_profiles profile on profile.configuration_set_id=r.agent_config_id and profile.role=r.agent_role
		left join agent_definition_versions definition on definition.definition_identity=r.definition_identity and definition.definition_version=r.definition_version
		left join agent_trees tree on tree.id=r.tree_id
		where j.id=$1 and j.run_id=$2
		for update of j,r
	`, job.ID, job.RunID).Scan(&status, &deadline, &maxAttempts, &currentLease, &storedErrorCode, &failedAttempts); err != nil {
		return agent.AttemptResolution{}, err
	}
	taskKind, taskVariant := agent.ClassifyTask(job.DefinitionIdentity, job.DefinitionVersion)
	if status != "running" || currentLease == nil || *currentLease != job.LeaseToken {
		actual, ok := terminalJobDisposition(status, storedErrorCode)
		if !ok {
			return agent.AttemptResolution{}, agent.ErrLeaseLost
		}
		if err := tx.Commit(ctx); err != nil {
			return agent.AttemptResolution{}, err
		}
		_ = traceScope.PublishAfterCommit(traceCtx)
		return actual, nil
	}
	actual := requested
	if actual.Disposition == agent.AttemptCompleted || actual.Disposition == agent.AttemptWaiting {
		return agent.AttemptResolution{}, errors.New("Role Executor returned without committing completed or waiting state")
	}
	if actual.Disposition == agent.AttemptAbandoned {
		return agent.AttemptResolution{}, errors.New("current leased Attempt cannot be committed as abandoned")
	}
	if actual.Disposition == agent.AttemptRetryable {
		switch {
		case failedAttempts+1 >= maxAttempts || job.AttemptNo >= MaxAgentLeases:
			actual = agent.AttemptResolution{Disposition: agent.AttemptTerminal, ErrorCode: "retry_exhausted"}
		case !time.Now().Add(actual.Backoff).Before(deadline):
			actual = agent.AttemptResolution{Disposition: agent.AttemptTerminal, ErrorCode: "run_deadline_exceeded"}
		default:
			jobTag, err := tx.Exec(ctx, `
				update agent_jobs set status='queued',lease_token=null,lease_expires_at=null,failed_attempts=failed_attempts+1,
					available_at=now()+($4*interval '1 second'),last_error_code=$5,updated_at=now()
				where id=$1 and run_id=$2 and status='running' and lease_token=$3::uuid
			`, job.ID, job.RunID, job.LeaseToken, actual.Backoff.Seconds(), actual.ErrorCode)
			if err != nil {
				return agent.AttemptResolution{}, err
			}
			runTag, err := tx.Exec(ctx, `update agent_runs set status='queued',updated_at=now() where id=$1 and status='running'`, job.RunID)
			if err != nil {
				return agent.AttemptResolution{}, err
			}
			if jobTag.RowsAffected() != 1 || runTag.RowsAffected() != 1 {
				return agent.AttemptResolution{}, agent.ErrLeaseLost
			}
			if err := agent.RecordAttemptRetryableInTx(traceCtx, tx, job.RunID, job.ID, job.AttemptNo, actual.ErrorCode, actual.Backoff); err != nil {
				return agent.AttemptResolution{}, err
			}
			if _, err := tx.Exec(ctx, `select pg_notify('nano_agent_jobs',$1)`, job.ID); err != nil {
				return agent.AttemptResolution{}, err
			}
			if err := tx.Commit(ctx); err != nil {
				return agent.AttemptResolution{}, err
			}
			_ = traceScope.PublishAfterCommit(traceCtx)
			q.metrics.RecordAttempt(taskKind, taskVariant, string(agent.AttemptRetryable))
			return actual, nil
		}
	}
	attempt := agent.Attempt{JobID: job.ID, RunID: job.RunID, AttemptNo: job.AttemptNo, LeaseToken: job.LeaseToken}
	if err := agent.TerminalizeAttemptStateInTx(traceCtx, tx, attempt, actual.ErrorCode); err != nil {
		return agent.AttemptResolution{}, err
	}
	if err := agent.RecordRunTerminalInTx(traceCtx, tx, job.RunID, agent.RunTerminalTrace{
		RunStatus: "failed", SpanStatus: agentobs.StatusError, ErrorCode: actual.ErrorCode, AttemptNo: job.AttemptNo,
	}); err != nil {
		return agent.AttemptResolution{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return agent.AttemptResolution{}, err
	}
	_ = traceScope.PublishAfterCommit(traceCtx)
	q.metrics.RecordAttempt(taskKind, taskVariant, string(agent.AttemptTerminal))
	q.metrics.RecordTerminal(taskKind, taskVariant, agent.TaskOutcomeForRun("failed", actual.ErrorCode), job.AttemptNo, job.AdmittedAt)
	q.metrics.RecordError(taskKind, agent.ErrorLayerForCode(actual.ErrorCode), actual.ErrorCode)
	return actual, nil
}

func terminalJobDisposition(status, errorCode string) (agent.AttemptResolution, bool) {
	switch status {
	case "succeeded":
		return agent.AttemptResolution{Disposition: agent.AttemptCompleted}, true
	case "waiting":
		return agent.AttemptResolution{Disposition: agent.AttemptWaiting}, true
	case "failed":
		if !(agent.AttemptResolution{Disposition: agent.AttemptTerminal, ErrorCode: errorCode}).Valid() {
			errorCode = "already_terminal"
		}
		return agent.AttemptResolution{Disposition: agent.AttemptTerminal, ErrorCode: errorCode}, true
	case "cancelled":
		return agent.AttemptResolution{Disposition: agent.AttemptAbandoned, ErrorCode: agent.AttemptCauseCancelled}, true
	default:
		return agent.AttemptResolution{}, false
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
	attempt := agent.Attempt{JobID: job.ID, RunID: job.RunID, AttemptNo: job.AttemptNo, LeaseToken: job.LeaseToken}
	if err := agent.TerminalizeAttemptStateInTx(ctx, tx, attempt, "recovery_exhausted"); err != nil {
		return err
	}
	if err := agent.RecordAttemptLeaseExpiredInTx(ctx, tx, job.RunID, job.ID, job.AttemptNo); err != nil {
		return err
	}
	if err := agent.RecordRunTerminalInTx(ctx, tx, job.RunID, agent.RunTerminalTrace{
		CauseEvent: agent.TraceEventRecoveryExhausted,
		RunStatus:  "failed",
		SpanStatus: agentobs.StatusError,
		ErrorCode:  "recovery_exhausted",
		AttemptNo:  job.AttemptNo,
	}); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `select pg_notify('nano_agent_runs', $1)`, job.RunID)
	return err
}

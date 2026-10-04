package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type researchSourceImportBarrier interface {
	WaitIfPending(context.Context, ActionRequest) (bool, error)
}

type postgresResearchSourceImportBarrier struct {
	pool *pgxpool.Pool
}

// WaitIfPending moves only the current leased Attempt to waiting. No action
// result is checkpointed, so replay resumes the same immutable assembly action
// after every imported Source reaches a terminal admission state.
func (b postgresResearchSourceImportBarrier) WaitIfPending(ctx context.Context, request ActionRequest) (bool, error) {
	if request.Definition.Identity != "research.executor" || request.Definition.Version < 9 {
		return false, nil
	}
	if b.pool == nil || strings.TrimSpace(request.Attempt.RunID) == "" {
		return false, errors.New("Research Source import barrier is unavailable")
	}
	traceCtx := ctx
	var traceScope *TraceScope
	if _, ok := TraceScopeFromContext(ctx); !ok {
		traceScope, err := NewTraceScope(DiscardTraceSink{})
		if err != nil {
			return false, err
		}
		traceCtx = ContextWithTraceScope(ctx, traceScope)
		defer traceScope.Rollback()
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return false, err
	}

	// Source workers lock the Source and processing Job before terminalizing
	// them. Taking the same locks first closes the check-to-wait lost-wakeup
	// race without holding the Agent Job lock while waiting on Source work.
	rows, err := tx.Query(ctx, `
		select source.state,job.status,imported.barrier_observed_attempt_no
		from research_source_imports imported
		join source_sources source on source.id=imported.source_id
		join source_processing_jobs job on job.id=imported.processing_job_id
		where (imported.run_id=$1 or (nano_research_root_run($1)=$1 and nano_research_root_run(imported.run_id)=$1))
		order by source.id,job.id
		for update of imported,source,job
	`, request.Attempt.RunID)
	if err != nil {
		return false, err
	}
	pending := false
	refreshRequired := false
	for rows.Next() {
		var sourceState, jobStatus string
		var observedAttempt *int
		if err := rows.Scan(&sourceState, &jobStatus, &observedAttempt); err != nil {
			rows.Close()
			return false, err
		}
		if !researchSourceImportTerminal(sourceState, jobStatus) {
			pending = true
		}
		if observedAttempt == nil {
			refreshRequired = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	rows.Close()
	if !pending && !refreshRequired {
		return false, tx.Commit(ctx)
	}
	if err := lockCheckpointAuthority(ctx, tx, request.Attempt); err != nil {
		return false, err
	}
	var identity, executor string
	var version int
	if err := tx.QueryRow(ctx, `select definition_identity,definition_version,executor_identity from agent_runs where id=$1`, request.Attempt.RunID).Scan(&identity, &version, &executor); err != nil {
		return false, err
	}
	if identity != request.Definition.Identity || version != request.Definition.Version || executor != "research_root" {
		return false, ErrLeaseLost
	}
	if _, err := tx.Exec(ctx, `
		update research_source_imports
		set barrier_observed_attempt_no=$2
		where (run_id=$1 or (nano_research_root_run($1)=$1 and nano_research_root_run(run_id)=$1)) and barrier_observed_attempt_no is null
	`, request.Attempt.RunID, request.Attempt.AttemptNo); err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `
		update agent_jobs
		set status='waiting',lease_token=null,lease_expires_at=null,updated_at=now()
		where id=$1 and run_id=$2 and status='running' and lease_token=$3::uuid and attempt_no=$4
	`, request.Attempt.JobID, request.Attempt.RunID, request.Attempt.LeaseToken, request.Attempt.AttemptNo)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() != 1 {
		return false, ErrLeaseLost
	}
	if err := RecordAttemptWaitingInTx(traceCtx, tx, request.Attempt.RunID, request.Attempt.JobID, request.Attempt.AttemptNo); err != nil {
		return false, err
	}
	if !pending {
		runTag, err := tx.Exec(ctx, `update agent_runs set status='queued',updated_at=now() where id=$1 and status='running'`, request.Attempt.RunID)
		if err != nil {
			return false, err
		}
		jobTag, err := tx.Exec(ctx, `update agent_jobs set status='queued',available_at=now(),updated_at=now() where id=$1 and run_id=$2 and status='waiting'`, request.Attempt.JobID, request.Attempt.RunID)
		if err != nil {
			return false, err
		}
		if runTag.RowsAffected() != 1 || jobTag.RowsAffected() != 1 {
			return false, ErrLeaseLost
		}
		if _, err := tx.Exec(ctx, `select pg_notify('nano_agent_jobs',$1)`, request.Attempt.JobID); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(ctx, `select pg_notify('nano_agent_runs',$1)`, request.Attempt.RunID); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	if traceScope != nil {
		_ = traceScope.PublishAfterCommit(traceCtx)
	}
	return true, nil
}

func researchSourceImportTerminal(sourceState, jobStatus string) bool {
	return sourceState == "ready" || sourceState == "failed" ||
		(sourceState == "qualifying" && jobStatus == "succeeded")
}

// AttachResearchSourceEvidenceInTx extends only a live source-first Research
// Run's pinned scope, after Source completion proved an active Evidence
// Revision and verified active-index projection.
func AttachResearchSourceEvidenceInTx(ctx context.Context, tx pgx.Tx, sourceID, revisionID string) error {
	if tx == nil || strings.TrimSpace(sourceID) == "" || strings.TrimSpace(revisionID) == "" {
		return errors.New("Research Source Evidence attachment is incomplete")
	}
	var notebookID, indexVersionID string
	if err := tx.QueryRow(ctx, `
		select source.notebook_id,build.index_version_id
		from source_sources source
		join source_evidence_revisions revision
			on revision.id=$2 and revision.source_id=source.id and revision.status='active'
		join retrieval_source_index_builds build
			on build.revision_id=revision.id and build.source_id=source.id
			and build.notebook_id=source.notebook_id and build.status='verified'
		join retrieval_index_versions version
			on version.id=build.index_version_id and version.status='active'
		where source.id=$1 and source.state='ready'
	`, sourceID, revisionID).Scan(&notebookID, &indexVersionID); err != nil {
		return err
	}
	// Serialize scope expansion with spawn_agent's root lock. A new child
	// either copies the committed pins or participates in this expansion.
	// Without this lock, a child spawned after the family snapshot could miss
	// verified Sources forever. Root-first locking also orders concurrent imports.
	rootRows, err := tx.Query(ctx, `select run.id from agent_runs run
		where run.id in (select nano_research_root_run(imported.run_id)
			from research_source_imports imported where imported.source_id=$1)
		order by run.id for update of run`, sourceID)
	if err != nil {
		return err
	}
	for rootRows.Next() {
	}
	err = rootRows.Err()
	rootRows.Close()
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
		select distinct run.id
		from research_source_imports imported
		join agent_runs run on nano_research_root_run(run.id)=nano_research_root_run(imported.run_id)
		where imported.source_id=$1 and run.runtime_kind='configured'
			and run.definition_identity='research.executor' and run.definition_version>=9
			and run.executor_identity='research_root' and run.status in ('queued','running')
		order by run.id
	`, sourceID)
	if err != nil {
		return err
	}
	runIDs := make([]string, 0)
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			rows.Close()
			return err
		}
		runIDs = append(runIDs, runID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, runID := range runIDs {
		var incompatible int
		if err := tx.QueryRow(ctx, `
			select count(*) from agent_run_evidence_set
			where run_id=$1 and index_version_id<>$2
		`, runID, indexVersionID).Scan(&incompatible); err != nil {
			return err
		}
		if incompatible != 0 {
			if _, err := tx.Exec(ctx, `update research_source_imports set retrieval_error_code='index_scope_conflict' where run_id=$1 and source_id=$2`, runID, sourceID); err != nil {
				return err
			}
			continue
		}
		var ordinal int
		if err := tx.QueryRow(ctx, `select coalesce(max(ordinal),-1)+1 from agent_run_evidence_set where run_id=$1`, runID).Scan(&ordinal); err != nil {
			return err
		}
		if ordinal > 49 {
			if _, err := tx.Exec(ctx, `update research_source_imports set retrieval_error_code='source_scope_limit' where run_id=$1 and source_id=$2`, runID, sourceID); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.Exec(ctx, `
			insert into agent_run_evidence_set(run_id,ordinal,notebook_id,source_id,evidence_revision_id,index_version_id)
			values($1,$2,$3,$4,$5,$6)
			on conflict(run_id,source_id) do nothing
		`, runID, ordinal, notebookID, sourceID, revisionID, indexVersionID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update research_source_imports set retrieval_error_code=null where run_id=$1 and source_id=$2`, runID, sourceID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			update agent_runs
			set selected_source_count=(select count(*) from agent_run_evidence_set where run_id=$1),updated_at=now()
			where id=$1 and status in ('queued','running')
		`, runID); err != nil {
			return err
		}
	}
	return nil
}

// WakeResearchSourceImportWaitersInTx requeues every live Research Run whose
// imported Sources are now all Ready, Failed, or held for admission review.
// It must run in the same transaction that terminalizes the Source.
func WakeResearchSourceImportWaitersInTx(ctx context.Context, tx pgx.Tx, sourceID string) error {
	if tx == nil || strings.TrimSpace(sourceID) == "" {
		return errors.New("Research Source waiter wake is incomplete")
	}
	rows, err := tx.Query(ctx, `select distinct run.id from research_source_imports imported
		join agent_runs run on run.id=imported.run_id or run.id=nano_research_root_run(imported.run_id)
		where imported.source_id=$1 order by run.id`, sourceID)
	if err != nil {
		return err
	}
	runIDs := make([]string, 0)
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			rows.Close()
			return err
		}
		runIDs = append(runIDs, runID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, runID := range runIDs {
		var allTerminal bool
		if err := tx.QueryRow(ctx, `
			select exists(select 1 from research_source_imports imported where (imported.run_id=$1 or (nano_research_root_run($1)=$1 and nano_research_root_run(imported.run_id)=$1)))
			and not exists(
				select 1
				from research_source_imports imported
				left join source_sources source on source.id=imported.source_id
				left join source_processing_jobs job on job.id=imported.processing_job_id
				where (imported.run_id=$1 or (nano_research_root_run($1)=$1 and nano_research_root_run(imported.run_id)=$1)) and not (
					imported.source_id is null or source.state in ('ready','failed') or
					(source.state='qualifying' and job.status='succeeded')
				)
			)
		`, runID).Scan(&allTerminal); err != nil {
			return err
		}
		if !allTerminal {
			continue
		}
		runTag, err := tx.Exec(ctx, `
			update agent_runs
			set status='queued',updated_at=now()
			where id=$1 and status='running' and coalesce(
				deadline_at,(select absolute_deadline from agent_trees where id=agent_runs.tree_id)
			)>now() and exists(select 1 from agent_jobs where run_id=$1 and status='waiting')
		`, runID)
		if err != nil {
			return err
		}
		if runTag.RowsAffected() == 0 {
			continue
		}
		jobTag, err := tx.Exec(ctx, `
			update agent_jobs
			set status='queued',available_at=now(),updated_at=now()
			where run_id=$1 and status='waiting'
		`, runID)
		if err != nil {
			return err
		}
		if jobTag.RowsAffected() != 1 {
			return errors.New("Research Source waiter lost its Agent Job")
		}
		if _, err := tx.Exec(ctx, `select pg_notify('nano_agent_jobs',id) from agent_jobs where run_id=$1 and status='queued'`, runID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `select pg_notify('nano_agent_runs',$1)`, runID); err != nil {
			return err
		}
	}
	return nil
}

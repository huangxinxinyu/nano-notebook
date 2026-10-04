package agent

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// TerminalizeAttemptStateInTx applies a safe terminal disposition without
// exposing Role-owned payload semantics to the execution host or Job queue.
func TerminalizeAttemptStateInTx(ctx context.Context, tx pgx.Tx, attempt Attempt, errorCode string) error {
	if tx == nil || attempt.JobID == "" || attempt.RunID == "" || attempt.LeaseToken == "" || attempt.AttemptNo < 1 ||
		!safeAttemptErrorCode.MatchString(errorCode) {
		return errors.New("invalid Attempt terminalization")
	}
	var runtimeKind string
	var role *AgentRole
	var executorIdentity *string
	var runtimeSubagent bool
	if err := tx.QueryRow(ctx, `select runtime_kind,agent_role,executor_identity,
		exists(select 1 from agent_subagents where child_run_id=$1)
		from agent_runs where id=$1`, attempt.RunID).Scan(&runtimeKind, &role, &executorIdentity, &runtimeSubagent); err != nil {
		return err
	}
	isResearch := (runtimeKind == "legacy_role" && role != nil && *role == RoleResearch) ||
		(runtimeKind == "configured" && executorIdentity != nil && *executorIdentity == "research")
	isChatLeader := (runtimeKind == "legacy_role" && role != nil && *role == RoleLeader) ||
		(runtimeKind == "configured" && executorIdentity != nil && *executorIdentity == "chat_leader")
	isStudio := runtimeKind == "configured" && executorIdentity != nil && *executorIdentity == "studio_structured_output"
	isConfiguredResearch := runtimeKind == "configured" && executorIdentity != nil &&
		(*executorIdentity == "research_planner" || *executorIdentity == "research_root")
	if isResearch {
		if err := FailResearchPayloadInTx(ctx, tx, attempt.RunID, errorCode); err != nil {
			return err
		}
		if err := (DelegationKernel{}).TerminalizeInTx(ctx, tx, attempt, DelegationFailed, errorCode); err != nil {
			return err
		}
	} else if isChatLeader || isStudio || isConfiguredResearch {
		jobTag, err := tx.Exec(ctx, `
			update agent_jobs set status='failed',lease_token=null,lease_expires_at=null,last_error_code=$4,
				finished_at=now(),updated_at=now()
			where id=$1 and run_id=$2 and status='running' and lease_token=$3::uuid
		`, attempt.JobID, attempt.RunID, attempt.LeaseToken, errorCode)
		if err != nil {
			return err
		}
		runTag, err := tx.Exec(ctx, `
			update agent_runs set status='failed',error_code=$2,finished_at=now(),updated_at=now()
			where id=$1 and status='running' and output_message_id is null
		`, attempt.RunID, errorCode)
		if err != nil {
			return err
		}
		if jobTag.RowsAffected() != 1 || runTag.RowsAffected() != 1 {
			return ErrLeaseLost
		}
		if isConfiguredResearch && !runtimeSubagent {
			sessionTag, err := tx.Exec(ctx, `
				update research_sessions set status='failed',error_code=$2,updated_at=now()
				where (planning_run_id=$1 and status='planning')
				   or (execution_run_id=$1 and status in ('queued','running','publishing'))
			`, attempt.RunID, errorCode)
			if err != nil {
				return err
			}
			if sessionTag.RowsAffected() != 1 {
				return ErrLeaseLost
			}
		}
		if _, err := tx.Exec(ctx, `select pg_notify('nano_agent_runs',$1)`, attempt.RunID); err != nil {
			return err
		}
	} else {
		return errors.New("unsupported Agent Role terminalization")
	}
	_, err := tx.Exec(ctx, `update agent_jobs set last_error_code=$2 where id=$1`, attempt.JobID, errorCode)
	return err
}

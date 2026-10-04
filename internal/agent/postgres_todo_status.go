package agent

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/jackc/pgx/v5"
)

func (r *PostgresRuntime) LoadTodoActionState(ctx context.Context, attempt Attempt, actionID string) (string, time.Time, TodoSnapshot, bool, error) {
	decisionNo, err := actionDecisionNo(actionID)
	if err != nil {
		return "", time.Time{}, TodoSnapshot{}, false, err
	}
	tx, err := r.workerTx(ctx)
	if err != nil {
		return "", time.Time{}, TodoSnapshot{}, false, err
	}
	defer tx.Rollback(ctx)
	var inputMessageID string
	var proposedAt time.Time
	err = tx.QueryRow(ctx, `
		select coalesce(r.input_message_id,product.input_message_id),checkpoint.created_at
		from agent_runs r
		join agent_jobs job on job.run_id=r.id
		left join agent_trees tree on tree.id=r.tree_id
		left join chat_runs product on product.root_agent_run_id=tree.root_agent_run_id
		join agent_run_checkpoints checkpoint on checkpoint.run_id=r.id
		where r.id=$1 and job.id=$2 and job.lease_token=$3::uuid and job.attempt_no=$4
		  and r.status='running' and job.status='running' and job.lease_expires_at>now()
		  and checkpoint.kind='action_proposal' and checkpoint.decision_no=$5
	`, attempt.RunID, attempt.JobID, attempt.LeaseToken, attempt.AttemptNo, decisionNo).Scan(&inputMessageID, &proposedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", time.Time{}, TodoSnapshot{}, false, ErrLeaseLost
	}
	if err != nil {
		return "", time.Time{}, TodoSnapshot{}, false, err
	}
	prefixes, err := loadTodoScopePrefixes(ctx, tx, attempt.RunID, inputMessageID, nil)
	if err != nil {
		return "", time.Time{}, TodoSnapshot{}, false, err
	}
	observation, err := ObserveAgentStatus(inputMessageID, prefixes, proposedAt, "UTC")
	if err != nil {
		return "", time.Time{}, TodoSnapshot{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", time.Time{}, TodoSnapshot{}, false, err
	}
	if observation.Todo == nil {
		return inputMessageID, proposedAt, TodoSnapshot{}, false, nil
	}
	return inputMessageID, proposedAt, cloneTodoSnapshot(*observation.Todo), true, nil
}

func (r *PostgresRuntime) observeAgentStatus(ctx context.Context, execution Execution, current CheckpointPrefix) (AgentStatusObservation, error) {
	tx, err := r.workerTx(ctx)
	if err != nil {
		return AgentStatusObservation{}, err
	}
	defer tx.Rollback(ctx)
	prefixes, err := loadTodoScopePrefixes(ctx, tx, execution.RunID, execution.InputMessageID, &current)
	if err != nil {
		return AgentStatusObservation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AgentStatusObservation{}, err
	}
	now := time.Now
	if r != nil && r.now != nil {
		now = r.now
	}
	return ObserveAgentStatus(execution.InputMessageID, prefixes, now().UTC(), execution.TimeZone)
}

func loadTodoScopePrefixes(ctx context.Context, tx pgx.Tx, currentRunID, inputMessageID string, current *CheckpointPrefix) ([]CheckpointPrefix, error) {
	rows, err := tx.Query(ctx, `
		with current_run as (
			select coalesce(r.chat_id,product.chat_id) chat_id,r.created_at,r.id,
				r.runtime_kind current_runtime_kind,r.executor_identity current_executor_identity
			from agent_runs r left join agent_trees tree on tree.id=r.tree_id left join chat_runs product on product.root_agent_run_id=tree.root_agent_run_id
			where r.id=$1
		)
		select r.id
		from agent_runs r
		left join agent_trees tree on tree.id=r.tree_id
		left join chat_runs product on product.root_agent_run_id=tree.root_agent_run_id
		cross join current_run current
		where coalesce(r.input_message_id,product.input_message_id)=$2
		  and coalesce(r.chat_id,product.chat_id)=current.chat_id
		  and (
			(current.current_runtime_kind='configured' and current.current_executor_identity='research_root'
			  and r.id=current.id and r.runtime_kind='configured' and r.executor_identity='research_root')
			or ((current.current_runtime_kind<>'configured' or current.current_executor_identity<>'research_root')
			  and ((r.runtime_kind='legacy_role' and r.agent_role='leader')
			    or (r.runtime_kind='configured' and r.executor_identity='chat_leader')))
		  )
		  and (r.created_at,r.id)<=(current.created_at,current.id)
		order by r.created_at,r.id
	`, currentRunID, inputMessageID)
	if err != nil {
		return nil, err
	}
	runIDs := make([]string, 0)
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			rows.Close()
			return nil, err
		}
		runIDs = append(runIDs, runID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(runIDs) == 0 {
		return nil, errors.New("TODO scope has no authorized Run")
	}
	prefixes := make([]CheckpointPrefix, 0, len(runIDs))
	for _, runID := range runIDs {
		if runID == currentRunID && current != nil {
			prefixes = append(prefixes, cloneCheckpointPrefix(*current))
			continue
		}
		checkpoints, err := loadRunCheckpoints(ctx, tx, runID)
		if err != nil {
			return nil, err
		}
		prefix, err := LoadCheckpointPrefix(ctx, checkpoints)
		if err != nil {
			return nil, err
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}

func (r *PostgresRuntime) FinalizeDecisionRequest(ctx context.Context, execution Execution, prefix CheckpointPrefix, request models.ModelRequest) (models.ModelRequest, error) {
	messages := request.Messages[:0]
	for _, message := range request.Messages {
		if message.Role == models.RoleUser && strings.HasPrefix(message.Content, "<agent_status version=\"1\">") && strings.HasSuffix(message.Content, "</agent_status>") {
			continue
		}
		messages = append(messages, message)
	}
	request.Messages = messages
	observation, err := r.observeAgentStatus(ctx, execution, prefix)
	if err != nil {
		return models.ModelRequest{}, err
	}
	rendered, err := RenderAgentStatus(observation)
	if err != nil {
		return models.ModelRequest{}, err
	}
	FinalizeDecisionRequest(&request, rendered)
	attachAgentStatusTelemetry(&request, observation, rendered)
	count, err := EstimateModelRequestTokens(request)
	if err != nil {
		return models.ModelRequest{}, err
	}
	request.ContextTelemetry.InputTokens = count.Tokens
	request.ContextTelemetry.InputTokenSource = string(count.Source)
	return request, nil
}

func actionDecisionNo(actionID string) (int, error) {
	parts := strings.Split(actionID, "/")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "decision:") || !strings.HasPrefix(parts[1], "action:") {
		return 0, errors.New("invalid TODO Action ID")
	}
	decisionNo, err := strconv.Atoi(strings.TrimPrefix(parts[0], "decision:"))
	if err != nil || decisionNo < 1 {
		return 0, fmt.Errorf("invalid TODO Action ID %q", actionID)
	}
	if actionIndex, err := strconv.Atoi(strings.TrimPrefix(parts[1], "action:")); err != nil || actionIndex < 0 {
		return 0, fmt.Errorf("invalid TODO Action ID %q", actionID)
	}
	return decisionNo, nil
}

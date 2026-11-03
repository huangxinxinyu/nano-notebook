package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
	"github.com/jackc/pgx/v5"
)

// loadChatLane reconstructs the one durable causal lane through the current
// Run. User Messages define turn order; Runs for an input are ordered by their
// durable creation identity, never by checkpoint completion time.
func (r *PostgresRuntime) loadChatLane(ctx context.Context, execution Execution, current CheckpointPrefix) (ChatLane, error) {
	tx, err := r.workerTx(ctx)
	if err != nil {
		return ChatLane{}, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		with cutoff as (
			select created_at,id from chat_messages where id=$2 and chat_id=$1 and role='user'
		)
		select m.id,m.content
		from chat_messages m,cutoff ff
		where m.chat_id=$1 and m.role='user' and m.steering_run_id is null
		  and (m.created_at,m.id)<=(ff.created_at,ff.id)
		order by m.created_at,m.id
	`, execution.ChatID, execution.InputMessageID)
	if err != nil {
		return ChatLane{}, err
	}
	lane := ChatLane{Turns: make([]ChatLaneTurn, 0)}
	turnByMessage := make(map[string]int)
	for rows.Next() {
		var turn ChatLaneTurn
		if err := rows.Scan(&turn.MessageID, &turn.Content); err != nil {
			rows.Close()
			return ChatLane{}, err
		}
		turnByMessage[turn.MessageID] = len(lane.Turns)
		lane.Turns = append(lane.Turns, turn)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ChatLane{}, err
	}
	rows.Close()
	if len(lane.Turns) == 0 {
		return ChatLane{}, projectionError("Run context has no durable User Messages")
	}

	rows, err = tx.Query(ctx, `
		with current_run as (
			select created_at,id from agent_runs where id=$3
		), cutoff as (
			select created_at,id from chat_messages where id=$2 and chat_id=$1 and role='user'
		)
		select r.id,input.id,coalesce(output.content,'')
		from agent_runs r
		left join chat_runs product on product.root_agent_run_id=r.id
		join chat_messages input on input.id=coalesce(r.input_message_id,product.input_message_id)
		left join chat_messages output on output.id=coalesce(r.output_message_id,product.output_message_id)
		cross join current_run current
		cross join cutoff
		where coalesce(r.chat_id,product.chat_id)=$1
		  and ((r.runtime_kind='legacy_role' and r.agent_role='leader') or r.runtime_kind='configured')
		  and (input.created_at,input.id)<=(cutoff.created_at,cutoff.id)
		  and (
			(input.created_at,input.id)<(cutoff.created_at,cutoff.id)
			or (r.created_at,r.id)<=(current.created_at,current.id)
		  )
		order by input.created_at,input.id,r.created_at,r.id
	`, execution.ChatID, execution.InputMessageID, execution.RunID)
	if err != nil {
		return ChatLane{}, err
	}
	type orderedRun struct {
		run            ChatLaneRun
		inputMessageID string
	}
	orderedRuns := make([]orderedRun, 0)
	for rows.Next() {
		var run ChatLaneRun
		var inputMessageID string
		if err := rows.Scan(&run.RunID, &inputMessageID, &run.LegacyPublishedFinal); err != nil {
			rows.Close()
			return ChatLane{}, err
		}
		orderedRuns = append(orderedRuns, orderedRun{run: run, inputMessageID: inputMessageID})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ChatLane{}, err
	}
	rows.Close()
	for _, ordered := range orderedRuns {
		run := ordered.run
		inputMessageID := ordered.inputMessageID
		turnIndex, ok := turnByMessage[inputMessageID]
		if !ok {
			return ChatLane{}, projectionError("Run %q has no ordered User Message", run.RunID)
		}
		if run.RunID == execution.RunID {
			prefix := cloneCheckpointPrefix(current)
			run.Prefix = &prefix
		} else {
			if err := reconcileTerminalRunActions(ctx, tx, run.RunID); err != nil {
				return ChatLane{}, fmt.Errorf("reconcile historical Run %q: %w", run.RunID, err)
			}
			run.Checkpoints, err = loadRunCheckpoints(ctx, tx, run.RunID)
			if err != nil {
				return ChatLane{}, fmt.Errorf("load historical Run %q checkpoints: %w", run.RunID, err)
			}
		}
		lane.Turns[turnIndex].Runs = append(lane.Turns[turnIndex].Runs, run)
	}
	if err := tx.Commit(ctx); err != nil {
		return ChatLane{}, err
	}
	return lane, nil
}

// reconcileTerminalRunActions is a narrow closing authority used only after
// the current Chat lane has selected an owned historical Run. It never
// executes a Tool: cancellation or a terminal crash makes completion unknown,
// so every unresolved call receives an explicit interrupted Result.
func reconcileTerminalRunActions(ctx context.Context, tx pgx.Tx, runID string) error {
	var status string
	if err := tx.QueryRow(ctx, `select status from agent_runs where id=$1 for update`, runID).Scan(&status); err != nil {
		return err
	}
	if status == "queued" || status == "running" {
		return nil
	}
	checkpoints, err := loadRunCheckpoints(ctx, tx, runID)
	if err != nil {
		return err
	}
	prefix, err := LoadCheckpointPrefix(ctx, checkpoints)
	if err != nil {
		return err
	}
	proposal, pending, ok := incompleteActions(prefix)
	if !ok {
		return nil
	}
	sequenceNo := len(checkpoints)
	for _, action := range pending {
		result := enrichActionDomainError(ActionResult{
			Status: ActionDomainError, ErrorCode: ErrorActionInterrupted,
		})
		closing, err := NewActionResultCheckpoint(proposal.DecisionNo, action.Index, action.ActionID, result)
		if err != nil {
			return err
		}
		sequenceNo++
		if _, err := tx.Exec(ctx, `
			insert into agent_run_checkpoints(
				run_id,sequence_no,identity_key,kind,decision_no,action_index,action_id,
				payload_version,payload,payload_sha256
			) values($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10)
		`, runID, sequenceNo, closing.IdentityKey, closing.Kind, closing.DecisionNo, closing.ActionIndex,
			closing.ActionID, closing.PayloadVersion, string(closing.Payload), closing.PayloadSHA256); err != nil {
			return err
		}
	}
	return nil
}

func (r *PostgresRuntime) projectChatLane(ctx context.Context, execution Execution, current CheckpointPrefix) ([]ContextUnit, error) {
	lane, err := r.loadChatLane(ctx, execution, current)
	if err != nil {
		return nil, err
	}
	runs := make(map[string]ChatLaneRun)
	for _, turn := range lane.Turns {
		for _, run := range turn.Runs {
			runs[run.RunID] = run
		}
	}
	type searchProjection struct {
		execution  Execution
		candidates []retrieval.EvidenceCandidate
	}
	searchByRun := make(map[string]searchProjection)
	projectResult := func(projectCtx context.Context, runID string, action AcceptedAction) (ActionResult, error) {
		if action.Result == nil {
			return ActionResult{}, errors.New("missing accepted Action Result")
		}
		result := *action.Result
		result.Output = append([]byte(nil), action.Result.Output...)
		if action.Name != "search_evidence" || result.Status != ActionSucceeded {
			return result, nil
		}
		projection, ok := searchByRun[runID]
		if !ok {
			run, exists := runs[runID]
			if !exists {
				return ActionResult{}, errors.New("originating Run is missing")
			}
			origin := execution
			if runID != execution.RunID {
				origin, err = r.LoadForReplay(projectCtx, runID)
				if err != nil {
					return ActionResult{}, fmt.Errorf("load originating Run pins: %w", err)
				}
			}
			var prefix CheckpointPrefix
			if run.Prefix != nil {
				prefix = cloneCheckpointPrefix(*run.Prefix)
			} else {
				prefix, err = LoadCheckpointPrefix(projectCtx, run.Checkpoints)
				if err != nil {
					return ActionResult{}, err
				}
			}
			candidates, loadErr := r.loadSearchEvidenceModelCandidates(projectCtx, origin, prefix)
			if loadErr != nil {
				return ActionResult{}, loadErr
			}
			projection = searchProjection{execution: origin, candidates: candidates}
			searchByRun[runID] = projection
		}
		result.Output, err = projectSearchEvidenceForModel(projection.execution, result.Output, projection.candidates)
		return result, err
	}
	return ProjectChatLane(ctx, lane, projectResult)
}

func buildProjectedRequest(execution Execution, systemPrompt string, units []ContextUnit, definitions []models.ActionDefinition) models.ModelRequest {
	messages := make([]models.ModelMessage, 0, 1+len(units))
	messages = append(messages, models.ModelMessage{Role: models.RoleSystem, Content: systemPrompt})
	messages = append(messages, FlattenContextUnits(units)...)
	return models.ModelRequest{
		Model: execution.Model, Messages: messages, ActionDefinitions: cloneActionDefinitions(definitions),
	}
}

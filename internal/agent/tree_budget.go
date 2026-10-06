package agent

import (
	"context"
	"errors"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/jackc/pgx/v5"
)

type treeBudgetCharge struct {
	ModelCalls   int
	Actions      int
	ContextBytes int
	ResultBytes  int
}

func chargeConfiguredTreeInTx(ctx context.Context, tx pgx.Tx, runID string, charge treeBudgetCharge) error {
	if tx == nil || runID == "" || charge.ModelCalls < 0 || charge.Actions < 0 || charge.ContextBytes < 0 || charge.ResultBytes < 0 {
		return errors.New("invalid Agent Tree budget charge")
	}
	if charge == (treeBudgetCharge{}) {
		return nil
	}
	var configured bool
	if err := tx.QueryRow(ctx, `select runtime_kind='configured' from agent_runs where id=$1`, runID).Scan(&configured); err != nil {
		return err
	}
	if !configured {
		return nil
	}
	tag, err := tx.Exec(ctx, `
		update agent_trees
		set model_calls_consumed=model_calls_consumed+$2,
			actions_consumed=actions_consumed+$3,
			context_bytes_consumed=context_bytes_consumed+$4,
			result_bytes_consumed=result_bytes_consumed+$5,
			updated_at=now()
		where id=(select tree_id from agent_runs where id=$1)
		  and model_calls_consumed+$2<=model_call_limit
		  and actions_consumed+$3<=action_limit
		  and context_bytes_consumed+$4<=context_byte_limit
		  and result_bytes_consumed+$5<=result_byte_limit
	`, runID, charge.ModelCalls, charge.Actions, charge.ContextBytes, charge.ResultBytes)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("Agent Tree budget exhausted")
	}
	return nil
}

// ModelUsageRuntime accounts provider token usage to the Agent Tree and
// reports when a Definition's input_tokens limit is spent.
type ModelUsageRuntime interface {
	RecordModelUsage(ctx context.Context, attempt Attempt, usage models.ModelCallMetadata) error
	ModelTokenBudgetExhausted(ctx context.Context, attempt Attempt) (bool, error)
}

// TokenBudgetWrapUpRuntime names the tools a Run keeps once its token budget
// is spent, so it can finish its deliverable from what it already gathered
// instead of returning a bare Final. Without it every business tool closes.
type TokenBudgetWrapUpRuntime interface {
	TokenBudgetWrapUpTools(execution Execution) map[string]bool
}

// RecordModelUsage adds one model call's token usage to the Run's tree. The
// tokens are spent whether or not the response is later accepted.
func (r *PostgresRuntime) RecordModelUsage(ctx context.Context, attempt Attempt, usage models.ModelCallMetadata) error {
	count := func(value *int64) int64 {
		if value == nil || *value < 0 {
			return 0
		}
		return *value
	}
	input, cached, output := count(usage.InputTokens), count(usage.CachedTokens), count(usage.OutputTokens)
	if input == 0 && output == 0 {
		return nil
	}
	_, err := r.pool.Exec(ctx, `
		update agent_trees set input_tokens_consumed=input_tokens_consumed+$2,
			cached_input_tokens_consumed=cached_input_tokens_consumed+$3,
			output_tokens_consumed=output_tokens_consumed+$4,updated_at=now()
		where id=(select tree_id from agent_runs where id=$1)
	`, attempt.RunID, input, cached, output)
	return err
}

// ModelTokenBudgetExhausted reads the limit from the tree root's Definition,
// so every Run in a Research tree, subagents included, shares one budget.
func (r *PostgresRuntime) ModelTokenBudgetExhausted(ctx context.Context, attempt Attempt) (bool, error) {
	var limit, consumed int64
	err := r.pool.QueryRow(ctx, `
		select coalesce((definition.limits->>'input_tokens')::bigint,0),tree.input_tokens_consumed
		from agent_runs run
		join agent_trees tree on tree.id=run.tree_id
		join agent_runs root on root.id=tree.root_agent_run_id
		join agent_definition_versions definition on definition.definition_identity=root.definition_identity
			and definition.definition_version=root.definition_version
		where run.id=$1
	`, attempt.RunID).Scan(&limit, &consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return limit > 0 && consumed >= limit, nil
}

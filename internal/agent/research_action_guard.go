package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// researchDeduplicatingAction prevents a long Research Run from spending its
// external-call budget on an identical accepted tool input. The current
// proposal is already checkpointed when Execute runs, so two occurrences mean
// an earlier complete Agent Step used the same tool and canonical input.
type researchDeduplicatingAction struct {
	pool   *pgxpool.Pool
	action Action
}

func NewResearchDeduplicatingAction(pool *pgxpool.Pool, action Action) Action {
	if pool == nil || action == nil {
		return action
	}
	return &researchDeduplicatingAction{pool: pool, action: action}
}

func (a *researchDeduplicatingAction) Definition() models.ActionDefinition {
	return a.action.Definition()
}

func (a *researchDeduplicatingAction) ValidateInput(raw json.RawMessage) error {
	return a.action.ValidateInput(raw)
}

func (a *researchDeduplicatingAction) CrashReplaySafe() bool {
	policy, ok := a.action.(CrashReplayPolicy)
	return ok && policy.CrashReplaySafe()
}

func (a *researchDeduplicatingAction) CacheLongToolResults(definition agentcatalog.Reference) bool {
	policy, ok := a.action.(ToolResultCacheEligibility)
	return ok && policy.CacheLongToolResults(definition)
}

func (a *researchDeduplicatingAction) Execute(ctx context.Context, request ActionRequest) (ActionResult, error) {
	var executorIdentity *string
	if err := a.pool.QueryRow(ctx, `select executor_identity from agent_runs where id=$1`, request.Attempt.RunID).Scan(&executorIdentity); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return a.action.Execute(ctx, request)
		}
		return ActionResult{}, err
	}
	if executorIdentity == nil || *executorIdentity != "research_root" {
		return a.action.Execute(ctx, request)
	}
	rows, err := a.pool.Query(ctx, `select payload from agent_run_checkpoints where run_id=$1 and kind='action_proposal' order by sequence_no`, request.Attempt.RunID)
	if err != nil {
		return ActionResult{}, err
	}
	defer rows.Close()
	payloads := make([][]byte, 0)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return ActionResult{}, err
		}
		payloads = append(payloads, payload)
	}
	if err := rows.Err(); err != nil {
		return ActionResult{}, err
	}
	if earlier, repeated := repeatedResearchAction(payloads, a.action.Definition().Name, request.Input); repeated {
		return ActionResult{Status: ActionDomainError, Error: researchDuplicateActionError(request.Attempt.RunID, earlier)}, nil
	}
	return a.action.Execute(ctx, request)
}

// researchDuplicateActionError points the model at the earlier identical
// call's checkpointed result instead of leaving it to guess and retry.
func researchDuplicateActionError(runID, earlierActionID string) *ActionError {
	return &ActionError{
		Kind: "domain", Code: "research_duplicate_action",
		Message:    fmt.Sprintf("An identical call already ran in this Research Run as %s; it was not repeated.", earlierActionID),
		Suggestion: fmt.Sprintf("Use that earlier result: it is in your context, or page it with read_tool_result result_ref %q. Otherwise choose a different URL or query.", "run:"+runID+"/checkpoint:"+earlierActionID),
	}
}

func hasRepeatedResearchAction(payloads [][]byte, name string, input json.RawMessage) bool {
	_, repeated := repeatedResearchAction(payloads, name, input)
	return repeated
}

// repeatedResearchAction reports whether the current proposal repeats an
// earlier identical call, and that earlier call's action id.
func repeatedResearchAction(payloads [][]byte, name string, input json.RawMessage) (string, bool) {
	want, err := CanonicalJSONObject(input)
	if err != nil {
		return "", false
	}
	first := ""
	matches := 0
	for _, raw := range payloads {
		var proposal proposalCheckpointPayload
		if json.Unmarshal(raw, &proposal) != nil {
			continue
		}
		for _, action := range proposal.Actions {
			if action.Name != name {
				continue
			}
			got, err := CanonicalJSONObject(action.Input)
			if err == nil && bytes.Equal(got, want) {
				matches++
				if matches == 1 {
					first = action.ActionID
				}
				if matches > 1 {
					return first, true
				}
			}
		}
	}
	return "", false
}

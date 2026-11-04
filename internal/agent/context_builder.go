package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

const BarePromptVersion = "agent-bare-v1"
const GroundedPromptVersion = "agent-grounded-v1"

// BuildDecisionRequest projects the durable Chat lane through the current Run
// with completed Proposal/Result checkpoints. An incomplete Action batch must
// be resumed by the Controller before another model decision is requested.
func (r *PostgresRuntime) BuildDecisionRequest(
	ctx context.Context,
	execution Execution,
	prefix CheckpointPrefix,
	definitions []models.ActionDefinition,
) (models.ModelRequest, error) {
	if execution.PromptVersion != BarePromptVersion && execution.PromptVersion != GroundedPromptVersion {
		return models.ModelRequest{}, fmt.Errorf("unsupported prompt version %q", execution.PromptVersion)
	}
	if prefix.Final != nil {
		return models.ModelRequest{}, errors.New("Final Draft does not require another model decision")
	}
	units, err := r.projectChatLane(ctx, execution, prefix)
	if err != nil {
		return models.ModelRequest{}, err
	}
	var activeCompaction *ContextCompaction
	if compaction, ok, loadErr := r.loadLatestContextCompaction(ctx, execution.ChatID); loadErr != nil {
		return models.ModelRequest{}, loadErr
	} else if ok {
		units, err = ApplyContextCompaction(units, compaction)
		if err != nil {
			return models.ModelRequest{}, err
		}
		activeCompaction = &compaction
	}
	units, request := buildAgedProjectedRequest(execution, r.contextSystemPrompt(execution), units, definitions)
	request.InvocationPolicy = execution.ModelInvocation
	request, err = r.FinalizeDecisionRequest(ctx, execution, prefix, request)
	if err != nil {
		return models.ModelRequest{}, err
	}
	count, err := EstimateModelRequestTokens(request)
	if err != nil {
		return models.ModelRequest{}, err
	}
	attachContextTelemetry(&request, execution, units, count, activeCompaction)
	return request, nil
}

// groundedRequiredAction is consulted only for runtimes that also implement
// QueryContextRuntime (today, Studio Output generation) — see
// docs/superpowers/specs/2026-08-04-prompt-driven-leader-decision-loop-design.md
// for why the Leader chat loop no longer calls this.
func groundedRequiredAction(prefix CheckpointPrefix) (string, error) {
	research, err := parseResearchState(prefix)
	if err != nil {
		return "", err
	}
	if !research.performed {
		return "search_evidence", nil
	}
	return "", nil
}

func cloneActionDefinitions(definitions []models.ActionDefinition) []models.ActionDefinition {
	if len(definitions) == 0 {
		return nil
	}
	cloned := make([]models.ActionDefinition, 0, len(definitions))
	for _, definition := range definitions {
		definition.InputSchema = append([]byte(nil), definition.InputSchema...)
		cloned = append(cloned, definition)
	}
	return cloned
}

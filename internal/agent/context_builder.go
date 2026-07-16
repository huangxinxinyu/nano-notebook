package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

const BarePromptVersion = "agent-bare-v1"

// BuildDecisionRequest combines bounded durable Chat history with completed
// Proposal/Result checkpoints. An incomplete Action batch must be resumed by
// the Controller before another model decision is requested.
func (r *PostgresRuntime) BuildDecisionRequest(
	ctx context.Context,
	execution Execution,
	prefix CheckpointPrefix,
	definitions []models.ActionDefinition,
) (models.ModelRequest, error) {
	if execution.PromptVersion != BarePromptVersion {
		return models.ModelRequest{}, fmt.Errorf("unsupported prompt version %q", execution.PromptVersion)
	}
	if prefix.Final != nil {
		return models.ModelRequest{}, errors.New("Final Draft does not require another model decision")
	}
	request, err := r.Build(ctx, execution)
	if err != nil {
		return models.ModelRequest{}, err
	}
	for _, proposal := range prefix.Proposals {
		if err := ctx.Err(); err != nil {
			return models.ModelRequest{}, err
		}
		proposalMessage := models.ModelMessage{
			Role:        models.RoleAssistant,
			ActionCalls: make([]models.ModelActionCall, 0, len(proposal.Actions)),
		}
		for _, action := range proposal.Actions {
			if action.Result == nil {
				return models.ModelRequest{}, fmt.Errorf("proposal decision %d has incomplete Action %d", proposal.DecisionNo, action.Index)
			}
			proposalMessage.ActionCalls = append(proposalMessage.ActionCalls, models.ModelActionCall{
				ID: action.ActionID, Name: action.Name, Input: append([]byte(nil), action.Input...),
			})
		}
		request.Messages = append(request.Messages, proposalMessage)
		for _, action := range proposal.Actions {
			checkpoint, err := NewActionResultCheckpoint(proposal.DecisionNo, action.Index, action.ActionID, *action.Result)
			if err != nil {
				return models.ModelRequest{}, fmt.Errorf("reconstruct Action Result %q: %w", action.ActionID, err)
			}
			request.Messages = append(request.Messages, models.ModelMessage{
				Role: models.RoleAction, Content: string(checkpoint.Payload), ActionCallID: action.ActionID,
			})
		}
	}
	request.ActionDefinitions = cloneActionDefinitions(definitions)
	return request, nil
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

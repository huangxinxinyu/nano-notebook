package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

// ErrContextProjection is returned when durable Chat authority cannot be
// represented as a complete, causally valid model context.
var ErrContextProjection = errors.New("context_projection_failed")

type ContextUnitKind string

const (
	ContextUnitUserMessage ContextUnitKind = "user_message"
	ContextUnitAgentStep   ContextUnitKind = "agent_step"
)

// ContextUnit is the smallest legal Compaction boundary. An Agent Step keeps
// its proposal and every sibling Result together.
type ContextUnit struct {
	Kind       ContextUnitKind
	MessageID  string
	RunID      string
	DecisionNo int
	Messages   []models.ModelMessage
}

type ChatLane struct {
	Turns []ChatLaneTurn
}

type ChatLaneTurn struct {
	MessageID string
	Content   string
	Runs      []ChatLaneRun
}

type ChatLaneRun struct {
	RunID                string
	Checkpoints          []Checkpoint
	Prefix               *CheckpointPrefix
	LegacyPublishedFinal string
}

// HistoricalResultProjector may replace the stored, Provider-neutral Result
// with its model-visible form. It receives the Result's originating Run ID so
// durable references are always resolved against that Run's pins.
type HistoricalResultProjector func(context.Context, string, AcceptedAction) (ActionResult, error)

func ProjectChatLane(ctx context.Context, lane ChatLane, projectResult HistoricalResultProjector) ([]ContextUnit, error) {
	units := make([]ContextUnit, 0, len(lane.Turns))
	seenMessages := make(map[string]struct{}, len(lane.Turns))
	seenRuns := make(map[string]struct{})
	for _, turn := range lane.Turns {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if strings.TrimSpace(turn.MessageID) == "" || strings.TrimSpace(turn.Content) == "" {
			return nil, projectionError("invalid User Message")
		}
		if _, duplicate := seenMessages[turn.MessageID]; duplicate {
			return nil, projectionError("duplicate User Message %q", turn.MessageID)
		}
		seenMessages[turn.MessageID] = struct{}{}
		units = append(units, ContextUnit{
			Kind: ContextUnitUserMessage, MessageID: turn.MessageID,
			Messages: []models.ModelMessage{{Role: models.RoleUser, Content: turn.Content}},
		})
		for _, run := range turn.Runs {
			if strings.TrimSpace(run.RunID) == "" {
				return nil, projectionError("empty Run ID")
			}
			if _, duplicate := seenRuns[run.RunID]; duplicate {
				return nil, projectionError("duplicate Run %q", run.RunID)
			}
			seenRuns[run.RunID] = struct{}{}
			var prefix CheckpointPrefix
			var err error
			if run.Prefix != nil {
				prefix = cloneCheckpointPrefix(*run.Prefix)
			} else {
				prefix, err = LoadCheckpointPrefix(ctx, run.Checkpoints)
				if err != nil {
					return nil, projectionError("Run %q: %v", run.RunID, err)
				}
			}
			// Steering Messages sit between complete Agent Steps, before the
			// decision they were delivered for, in checkpoint order.
			steeringIndex := 0
			flushSteering := func(throughDecision int) {
				for steeringIndex < len(prefix.Steering) && prefix.Steering[steeringIndex].DecisionNo <= throughDecision {
					units = append(units, steeringContextUnit(run.RunID, prefix.Steering[steeringIndex]))
					steeringIndex++
				}
			}
			for _, proposal := range prefix.Proposals {
				flushSteering(proposal.DecisionNo)
				messages := make([]models.ModelMessage, 0, 1+len(proposal.Actions))
				proposalMessage := models.ModelMessage{Role: models.RoleAssistant, ActionCalls: make([]models.ModelActionCall, 0, len(proposal.Actions))}
				for _, action := range proposal.Actions {
					if action.Result == nil {
						return nil, projectionError("Run %q decision %d has incomplete Action %d", run.RunID, proposal.DecisionNo, action.Index)
					}
					proposalMessage.ActionCalls = append(proposalMessage.ActionCalls, models.ModelActionCall{
						ID: action.ActionID, Name: action.Name, Input: append([]byte(nil), action.Input...),
					})
				}
				messages = append(messages, proposalMessage)
				for _, action := range proposal.Actions {
					result := *action.Result
					result.Output = append([]byte(nil), result.Output...)
					if projectResult != nil {
						result, err = projectResult(ctx, run.RunID, action)
						if err != nil {
							return nil, projectionError("Run %q Action %q: %v", run.RunID, action.ActionID, err)
						}
					}
					checkpoint, err := NewActionResultCheckpoint(proposal.DecisionNo, action.Index, action.ActionID, result)
					if err != nil {
						return nil, projectionError("Run %q Action %q: %v", run.RunID, action.ActionID, err)
					}
					messages = append(messages, models.ModelMessage{
						Role: models.RoleAction, Content: string(checkpoint.Payload), ActionCallID: action.ActionID,
					})
				}
				units = append(units, ContextUnit{
					Kind: ContextUnitAgentStep, MessageID: turn.MessageID, RunID: run.RunID,
					DecisionNo: proposal.DecisionNo, Messages: messages,
				})
			}
			for ; steeringIndex < len(prefix.Steering); steeringIndex++ {
				units = append(units, steeringContextUnit(run.RunID, prefix.Steering[steeringIndex]))
			}
			if prefix.Final != nil {
				units = append(units, ContextUnit{
					Kind: ContextUnitAgentStep, MessageID: turn.MessageID, RunID: run.RunID,
					DecisionNo: prefix.AcceptedDecisions,
					Messages:   []models.ModelMessage{{Role: models.RoleAssistant, Content: prefix.Final.Text}},
				})
			} else if strings.TrimSpace(run.LegacyPublishedFinal) != "" {
				units = append(units, ContextUnit{
					Kind: ContextUnitAgentStep, MessageID: turn.MessageID, RunID: run.RunID,
					Messages: []models.ModelMessage{{Role: models.RoleAssistant, Content: run.LegacyPublishedFinal}},
				})
			}
		}
	}
	return units, nil
}

func FlattenContextUnits(units []ContextUnit) []models.ModelMessage {
	count := 0
	for _, unit := range units {
		count += len(unit.Messages)
	}
	messages := make([]models.ModelMessage, 0, count)
	for _, unit := range units {
		for _, message := range unit.Messages {
			copyMessage := message
			copyMessage.ActionCalls = append([]models.ModelActionCall(nil), message.ActionCalls...)
			for index := range copyMessage.ActionCalls {
				copyMessage.ActionCalls[index].Input = append([]byte(nil), message.ActionCalls[index].Input...)
			}
			messages = append(messages, copyMessage)
		}
	}
	return messages
}

func projectionError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrContextProjection, fmt.Sprintf(format, args...))
}

func cloneCheckpointPrefix(prefix CheckpointPrefix) CheckpointPrefix {
	cloned := prefix
	cloned.Proposals = make([]AcceptedProposal, len(prefix.Proposals))
	for proposalIndex, proposal := range prefix.Proposals {
		cloned.Proposals[proposalIndex] = proposal
		cloned.Proposals[proposalIndex].Actions = make([]AcceptedAction, len(proposal.Actions))
		for actionIndex, action := range proposal.Actions {
			cloned.Proposals[proposalIndex].Actions[actionIndex] = action
			cloned.Proposals[proposalIndex].Actions[actionIndex].Input = append([]byte(nil), action.Input...)
			if action.Result != nil {
				result := *action.Result
				result.Output = append([]byte(nil), action.Result.Output...)
				cloned.Proposals[proposalIndex].Actions[actionIndex].Result = &result
			}
		}
	}
	cloned.Steering = append([]AcceptedSteering(nil), prefix.Steering...)
	if prefix.Final != nil {
		final := *prefix.Final
		cloned.Final = &final
	}
	return cloned
}

// steeringContextMessage is the model-visible form of one Steering Message.
// The marker tells the model the text arrived mid-run rather than as a new
// question, so it adjusts the current work instead of starting over.
func steeringContextMessage(steering AcceptedSteering) models.ModelMessage {
	return models.ModelMessage{
		Role:    models.RoleUser,
		Content: "[Steering from the Member, received while you were working. Adjust the current work accordingly.]\n" + steering.Content,
	}
}

func steeringContextUnit(runID string, steering AcceptedSteering) ContextUnit {
	return ContextUnit{
		Kind: ContextUnitUserMessage, MessageID: steering.MessageID, RunID: runID, DecisionNo: steering.DecisionNo,
		Messages: []models.ModelMessage{steeringContextMessage(steering)},
	}
}

// withoutSteering returns the prefix with delivered Steering Messages removed
// from the trajectory, for projections that recite them elsewhere.
func withoutSteering(prefix CheckpointPrefix) CheckpointPrefix {
	cloned := cloneCheckpointPrefix(prefix)
	cloned.Steering = nil
	return cloned
}

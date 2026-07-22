package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

type Checkpoint struct {
	SequenceNo int
	PendingCheckpoint
	CreatedAt time.Time
}

type AcceptedAction struct {
	ActionID string
	Index    int
	Name     string
	Input    json.RawMessage
	Result   *ActionResult
}

type AcceptedProposal struct {
	DecisionNo int
	Actions    []AcceptedAction
}

type CheckpointPrefix struct {
	Proposals         []AcceptedProposal
	Final             *models.FinalDraft
	AcceptedDecisions int
	AcceptedActions   int
}

var ErrCheckpointInvalid = errors.New("checkpoint_invalid")

func LoadCheckpointPrefix(ctx context.Context, checkpoints []Checkpoint) (CheckpointPrefix, error) {
	prefix := CheckpointPrefix{Proposals: make([]AcceptedProposal, 0)}
	nextDecision := 1
	finalAccepted := false
	for position, checkpoint := range checkpoints {
		if err := ctx.Err(); err != nil {
			return CheckpointPrefix{}, err
		}
		if checkpoint.SequenceNo != position+1 {
			return CheckpointPrefix{}, invalidCheckpoint("sequence gap at %d", position+1)
		}
		if finalAccepted {
			return CheckpointPrefix{}, invalidCheckpoint("outcome follows Final Draft")
		}
		switch checkpoint.Kind {
		case CheckpointActionProposal:
			if hasIncompleteProposal(prefix) || checkpoint.DecisionNo != nextDecision {
				return CheckpointPrefix{}, invalidCheckpoint("unexpected proposal decision %d", checkpoint.DecisionNo)
			}
			var payload proposalCheckpointPayload
			if err := json.Unmarshal(checkpoint.Payload, &payload); err != nil || len(payload.Actions) == 0 {
				return CheckpointPrefix{}, invalidCheckpoint("invalid proposal payload")
			}
			batch := models.ActionProposalBatch{Actions: make([]models.ActionProposal, 0, len(payload.Actions))}
			for index, action := range payload.Actions {
				if action.Index != index || action.ActionID != fmt.Sprintf("decision:%d/action:%d", checkpoint.DecisionNo, index) {
					return CheckpointPrefix{}, invalidCheckpoint("invalid proposal Action ordinal")
				}
				batch.Actions = append(batch.Actions, models.ActionProposal{Name: action.Name, Input: action.Input})
			}
			expected, err := NewProposalCheckpoint(checkpoint.DecisionNo, batch)
			if err != nil || !checkpointMatches(checkpoint, expected) {
				return CheckpointPrefix{}, invalidCheckpoint("proposal identity or payload mismatch")
			}
			accepted := AcceptedProposal{DecisionNo: checkpoint.DecisionNo, Actions: make([]AcceptedAction, 0, len(payload.Actions))}
			for _, action := range payload.Actions {
				accepted.Actions = append(accepted.Actions, AcceptedAction{ActionID: action.ActionID, Index: action.Index, Name: action.Name, Input: append(json.RawMessage(nil), action.Input...)})
			}
			prefix.Proposals = append(prefix.Proposals, accepted)
			prefix.AcceptedDecisions++
			prefix.AcceptedActions += len(accepted.Actions)
			nextDecision++
		case CheckpointActionResult:
			if len(prefix.Proposals) == 0 {
				return CheckpointPrefix{}, invalidCheckpoint("Action Result has no proposal")
			}
			proposal := &prefix.Proposals[len(prefix.Proposals)-1]
			missingIndex := firstMissingResult(*proposal)
			if missingIndex < 0 || checkpoint.DecisionNo != proposal.DecisionNo || checkpoint.ActionIndex == nil || *checkpoint.ActionIndex != missingIndex {
				return CheckpointPrefix{}, invalidCheckpoint("Action Result is out of order")
			}
			var payload actionResultCheckpointPayload
			if err := json.Unmarshal(checkpoint.Payload, &payload); err != nil || payload.ActionID != proposal.Actions[missingIndex].ActionID {
				return CheckpointPrefix{}, invalidCheckpoint("invalid Action Result payload")
			}
			result := ActionResult{Status: payload.Status, Output: payload.Output, ErrorCode: payload.ErrorCode}
			expected, err := NewActionResultCheckpoint(proposal.DecisionNo, missingIndex, proposal.Actions[missingIndex].ActionID, result)
			if err != nil || !checkpointMatches(checkpoint, expected) {
				return CheckpointPrefix{}, invalidCheckpoint("Action Result identity or payload mismatch")
			}
			copyResult := result
			copyResult.Output = append(json.RawMessage(nil), result.Output...)
			proposal.Actions[missingIndex].Result = &copyResult
		case CheckpointFinalDraft:
			if hasIncompleteProposal(prefix) || checkpoint.DecisionNo != nextDecision {
				return CheckpointPrefix{}, invalidCheckpoint("unexpected Final Draft decision %d", checkpoint.DecisionNo)
			}
			var payload finalDraftCheckpointPayload
			if err := json.Unmarshal(checkpoint.Payload, &payload); err != nil {
				return CheckpointPrefix{}, invalidCheckpoint("invalid Final Draft payload")
			}
			draft := models.FinalDraft{Text: payload.Text}
			expected, err := NewFinalDraftCheckpoint(checkpoint.DecisionNo, draft)
			if err != nil || !checkpointMatches(checkpoint, expected) {
				return CheckpointPrefix{}, invalidCheckpoint("Final Draft identity or payload mismatch")
			}
			prefix.Final = &draft
			prefix.AcceptedDecisions++
			nextDecision++
			finalAccepted = true
		default:
			return CheckpointPrefix{}, invalidCheckpoint("unknown kind %q", checkpoint.Kind)
		}
	}
	return prefix, nil
}

func firstMissingResult(proposal AcceptedProposal) int {
	for index := range proposal.Actions {
		if proposal.Actions[index].Result == nil {
			return index
		}
	}
	return -1
}

func hasIncompleteProposal(prefix CheckpointPrefix) bool {
	if len(prefix.Proposals) == 0 {
		return false
	}
	return firstMissingResult(prefix.Proposals[len(prefix.Proposals)-1]) >= 0
}

func checkpointMatches(checkpoint Checkpoint, expected PendingCheckpoint) bool {
	if checkpoint.IdentityKey != expected.IdentityKey || checkpoint.Kind != expected.Kind || checkpoint.DecisionNo != expected.DecisionNo || checkpoint.ActionID != expected.ActionID || checkpoint.PayloadVersion != expected.PayloadVersion || checkpoint.PayloadSHA256 != expected.PayloadSHA256 || !sameJSON(checkpoint.Payload, expected.Payload) {
		return false
	}
	if checkpoint.ActionIndex == nil || expected.ActionIndex == nil {
		return checkpoint.ActionIndex == nil && expected.ActionIndex == nil
	}
	return *checkpoint.ActionIndex == *expected.ActionIndex
}

func sameJSON(left, right []byte) bool {
	if bytes.Equal(left, right) {
		return true
	}
	var leftValue, rightValue any
	leftDecoder := json.NewDecoder(bytes.NewReader(left))
	leftDecoder.UseNumber()
	rightDecoder := json.NewDecoder(bytes.NewReader(right))
	rightDecoder.UseNumber()
	return leftDecoder.Decode(&leftValue) == nil && rightDecoder.Decode(&rightValue) == nil && reflect.DeepEqual(leftValue, rightValue)
}

func invalidCheckpoint(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCheckpointInvalid, fmt.Sprintf(format, args...))
}

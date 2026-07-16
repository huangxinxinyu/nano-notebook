package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

type CheckpointKind string

const (
	CheckpointActionProposal CheckpointKind = "action_proposal"
	CheckpointActionResult   CheckpointKind = "action_result"
	CheckpointFinalDraft     CheckpointKind = "final_draft"
)

type PendingCheckpoint struct {
	IdentityKey    string
	Kind           CheckpointKind
	DecisionNo     int
	ActionIndex    *int
	ActionID       string
	PayloadVersion int
	Payload        json.RawMessage
	PayloadSHA256  string
}

type proposalCheckpointPayload struct {
	Actions []proposalCheckpointAction `json:"actions"`
}

type proposalCheckpointAction struct {
	ActionID string          `json:"action_id"`
	Index    int             `json:"index"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
}

func NewProposalCheckpoint(decisionNo int, batch models.ActionProposalBatch) (PendingCheckpoint, error) {
	if decisionNo < 1 || len(batch.Actions) == 0 {
		return PendingCheckpoint{}, errors.New("invalid Action proposal checkpoint")
	}
	payload := proposalCheckpointPayload{Actions: make([]proposalCheckpointAction, 0, len(batch.Actions))}
	for index, action := range batch.Actions {
		if !actionNamePattern.MatchString(action.Name) {
			return PendingCheckpoint{}, errors.New("invalid Action proposal name")
		}
		input, err := canonicalJSONObject(action.Input)
		if err != nil {
			return PendingCheckpoint{}, err
		}
		payload.Actions = append(payload.Actions, proposalCheckpointAction{
			ActionID: fmt.Sprintf("decision:%d/action:%d", decisionNo, index),
			Index:    index,
			Name:     action.Name,
			Input:    input,
		})
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return PendingCheckpoint{}, err
	}
	return PendingCheckpoint{
		IdentityKey:    fmt.Sprintf("decision:%d", decisionNo),
		Kind:           CheckpointActionProposal,
		DecisionNo:     decisionNo,
		PayloadVersion: 1,
		Payload:        encoded,
		PayloadSHA256:  hashPayload(encoded),
	}, nil
}

type actionResultCheckpointPayload struct {
	ActionID  string             `json:"action_id"`
	Status    ActionResultStatus `json:"status"`
	Output    json.RawMessage    `json:"output,omitempty"`
	ErrorCode string             `json:"error_code,omitempty"`
}

func NewActionResultCheckpoint(decisionNo, actionIndex int, actionID string, result ActionResult) (PendingCheckpoint, error) {
	expectedID := fmt.Sprintf("decision:%d/action:%d", decisionNo, actionIndex)
	if decisionNo < 1 || actionIndex < 0 || actionID != expectedID {
		return PendingCheckpoint{}, errors.New("invalid Action result checkpoint identity")
	}
	if err := result.Validate(); err != nil {
		return PendingCheckpoint{}, err
	}
	payload := actionResultCheckpointPayload{ActionID: actionID, Status: result.Status, ErrorCode: result.ErrorCode}
	if result.Status == ActionSucceeded {
		output, err := canonicalJSONObject(result.Output)
		if err != nil {
			return PendingCheckpoint{}, err
		}
		payload.Output = output
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return PendingCheckpoint{}, err
	}
	index := actionIndex
	return PendingCheckpoint{
		IdentityKey:    actionID,
		Kind:           CheckpointActionResult,
		DecisionNo:     decisionNo,
		ActionIndex:    &index,
		ActionID:       actionID,
		PayloadVersion: 1,
		Payload:        encoded,
		PayloadSHA256:  hashPayload(encoded),
	}, nil
}

type finalDraftCheckpointPayload struct {
	Text string `json:"text"`
}

func NewFinalDraftCheckpoint(decisionNo int, draft models.FinalDraft) (PendingCheckpoint, error) {
	if decisionNo < 1 || strings.TrimSpace(draft.Text) == "" || len([]byte(draft.Text)) > 64*1024 {
		return PendingCheckpoint{}, errors.New("invalid Final Draft checkpoint")
	}
	encoded, err := json.Marshal(finalDraftCheckpointPayload{Text: draft.Text})
	if err != nil {
		return PendingCheckpoint{}, err
	}
	return PendingCheckpoint{
		IdentityKey:    fmt.Sprintf("decision:%d/final", decisionNo),
		Kind:           CheckpointFinalDraft,
		DecisionNo:     decisionNo,
		PayloadVersion: 1,
		Payload:        encoded,
		PayloadSHA256:  hashPayload(encoded),
	}, nil
}

func canonicalJSONObject(raw json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, errors.New("payload must be a JSON object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("payload has trailing JSON")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func hashPayload(payload []byte) string {
	hash := sha256.Sum256(payload)
	return fmt.Sprintf("%x", hash[:])
}

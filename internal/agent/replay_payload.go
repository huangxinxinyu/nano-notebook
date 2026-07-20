package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/replay"
)

const replayPayloadSchemaVersion = 1

type replayPayloadHeader struct {
	SchemaVersion int          `json:"schema_version"`
	Class         replay.Class `json:"class"`
}

type modelRequestReplay struct {
	replayPayloadHeader
	Model             string                   `json:"model"`
	Messages          []modelReplayMessage     `json:"messages"`
	ActionDefinitions []replayActionDefinition `json:"action_definitions,omitempty"`
}

type modelReplayMessage struct {
	Role         models.ModelRole        `json:"role"`
	Text         string                  `json:"text,omitempty"`
	ActionCalls  []replayModelActionCall `json:"action_calls,omitempty"`
	ActionCallID string                  `json:"action_call_id,omitempty"`
	ActionResult json.RawMessage         `json:"action_result,omitempty"`
}

type replayModelActionCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type replayActionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func EncodeModelRequestReplay(request models.ModelRequest) (replay.PlainPayload, error) {
	budget := newReplaySizeBudget()
	budget.addString(request.Model)
	if strings.TrimSpace(request.Model) == "" {
		return replay.PlainPayload{}, errors.New("Replay Model Request model is empty")
	}
	document := modelRequestReplay{
		replayPayloadHeader: replayHeader(replay.ClassModelRequest),
		Model:               request.Model, Messages: make([]modelReplayMessage, 0, len(request.Messages)),
		ActionDefinitions: make([]replayActionDefinition, 0, len(request.ActionDefinitions)),
	}
	for _, message := range request.Messages {
		budget.addString(message.Content)
		item := modelReplayMessage{Role: message.Role}
		switch message.Role {
		case models.RoleSystem, models.RoleUser:
			if strings.TrimSpace(message.Content) == "" || len(message.ActionCalls) != 0 || message.ActionCallID != "" {
				return replay.PlainPayload{}, errors.New("Replay Model Request contains invalid text message")
			}
			item.Text = message.Content
		case models.RoleAssistant:
			if len(message.ActionCalls) == 0 {
				if strings.TrimSpace(message.Content) == "" || message.ActionCallID != "" {
					return replay.PlainPayload{}, errors.New("Replay Model Request contains invalid assistant message")
				}
				item.Text = message.Content
				break
			}
			if strings.TrimSpace(message.Content) != "" || message.ActionCallID != "" {
				return replay.PlainPayload{}, errors.New("Replay Model Request contains conflicting assistant message")
			}
			item.ActionCalls = make([]replayModelActionCall, 0, len(message.ActionCalls))
			for _, call := range message.ActionCalls {
				budget.addString(call.ID)
				budget.addString(call.Name)
				budget.addRaw(call.Input)
				if strings.TrimSpace(call.ID) == "" || !actionNamePattern.MatchString(call.Name) {
					return replay.PlainPayload{}, errors.New("Replay Model Request contains invalid Action call")
				}
				input, err := canonicalReplayObject(call.Input, true)
				if err != nil {
					return replay.PlainPayload{}, err
				}
				item.ActionCalls = append(item.ActionCalls, replayModelActionCall{ID: call.ID, Name: call.Name, Input: input})
			}
		case models.RoleAction:
			budget.addString(message.ActionCallID)
			budget.addRaw([]byte(message.Content))
			if strings.TrimSpace(message.ActionCallID) == "" || strings.TrimSpace(message.Content) == "" || len(message.ActionCalls) != 0 {
				return replay.PlainPayload{}, errors.New("Replay Model Request contains invalid Action result message")
			}
			result, err := canonicalReplayObject([]byte(message.Content), true)
			if err != nil {
				return replay.PlainPayload{}, err
			}
			item.ActionCallID = message.ActionCallID
			item.ActionResult = result
		default:
			return replay.PlainPayload{}, errors.New("Replay Model Request contains unsupported role")
		}
		document.Messages = append(document.Messages, item)
	}
	for _, definition := range request.ActionDefinitions {
		budget.addString(definition.Name)
		budget.addString(definition.Description)
		budget.addRaw(definition.InputSchema)
		if !actionNamePattern.MatchString(definition.Name) || strings.TrimSpace(definition.Description) == "" {
			return replay.PlainPayload{}, errors.New("Replay Model Request contains invalid Action definition")
		}
		schema, err := canonicalReplayObject(definition.InputSchema, false)
		if err != nil {
			return replay.PlainPayload{}, err
		}
		document.ActionDefinitions = append(document.ActionDefinitions, replayActionDefinition{
			Name: definition.Name, Description: definition.Description, InputSchema: schema,
		})
	}
	if err := budget.err(); err != nil {
		return replay.PlainPayload{}, err
	}
	return marshalReplayPayload(replay.ClassModelRequest, document)
}

type modelDecisionReplay struct {
	replayPayloadHeader
	Final   *replayFinalDraft      `json:"final,omitempty"`
	Actions []replayActionProposal `json:"actions,omitempty"`
}

type replayFinalDraft struct {
	Text   string              `json:"text"`
	Claims []models.DraftClaim `json:"claims,omitempty"`
}

type replayActionProposal struct {
	Index int             `json:"index"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

func EncodeModelDecisionReplay(decision models.ModelDecision) (replay.PlainPayload, error) {
	if err := decision.Validate(); err != nil {
		return replay.PlainPayload{}, err
	}
	budget := newReplaySizeBudget()
	document := modelDecisionReplay{replayPayloadHeader: replayHeader(replay.ClassModelDecision)}
	if decision.Final != nil {
		budget.addString(decision.Final.Text)
		for _, claim := range decision.Final.Claims {
			budget.addString(claim.Text)
			for _, citation := range claim.Citations {
				budget.addString(citation.SourceID)
				budget.addString(citation.EvidenceRevisionID)
				budget.addString(citation.UnitID)
			}
		}
		document.Final = &replayFinalDraft{Text: decision.Final.Text, Claims: decision.Final.Claims}
	} else {
		document.Actions = make([]replayActionProposal, 0, len(decision.Proposal.Actions))
		for index, action := range decision.Proposal.Actions {
			budget.addString(action.Name)
			budget.addRaw(action.Input)
			if !actionNamePattern.MatchString(action.Name) {
				return replay.PlainPayload{}, errors.New("Replay Model Decision contains invalid Action name")
			}
			input, err := canonicalReplayObject(action.Input, true)
			if err != nil {
				return replay.PlainPayload{}, err
			}
			document.Actions = append(document.Actions, replayActionProposal{Index: index, Name: action.Name, Input: input})
		}
	}
	if err := budget.err(); err != nil {
		return replay.PlainPayload{}, err
	}
	return marshalReplayPayload(replay.ClassModelDecision, document)
}

type claimSupportRequestReplay struct {
	replayPayloadHeader
	Operation     string                     `json:"operation"`
	Model         string                     `json:"model"`
	PromptVersion string                     `json:"prompt_version"`
	Claims        []models.ClaimSupportInput `json:"claims"`
}

func EncodeClaimSupportRequestReplay(request models.ClaimSupportRequest) (replay.PlainPayload, error) {
	if strings.TrimSpace(request.Model) == "" || strings.TrimSpace(request.PromptVersion) == "" || len(request.Claims) == 0 {
		return replay.PlainPayload{}, errors.New("Replay Claim Support request is invalid")
	}
	budget := newReplaySizeBudget()
	budget.addString(request.Model)
	budget.addString(request.PromptVersion)
	for ordinal, claim := range request.Claims {
		if claim.Ordinal != ordinal || strings.TrimSpace(claim.Text) == "" || len(claim.Evidence) == 0 {
			return replay.PlainPayload{}, errors.New("Replay Claim Support claim is invalid")
		}
		budget.addString(claim.Text)
		for _, evidence := range claim.Evidence {
			budget.addString(evidence.SourceID)
			budget.addString(evidence.RevisionID)
			budget.addString(evidence.UnitID)
			budget.addString(evidence.Text)
		}
	}
	if err := budget.err(); err != nil {
		return replay.PlainPayload{}, err
	}
	return marshalReplayPayload(replay.ClassModelRequest, claimSupportRequestReplay{
		replayPayloadHeader: replayHeader(replay.ClassModelRequest), Operation: "claim_support",
		Model: request.Model, PromptVersion: request.PromptVersion, Claims: request.Claims,
	})
}

type claimSupportVerdictReplay struct {
	replayPayloadHeader
	Operation string                       `json:"operation"`
	Verdicts  []models.ClaimSupportVerdict `json:"verdicts"`
}

func EncodeClaimSupportVerdictReplay(outcome models.ClaimSupportOutcome) (replay.PlainPayload, error) {
	if len(outcome.Verdicts) == 0 {
		return replay.PlainPayload{}, errors.New("Replay Claim Support verdict is empty")
	}
	for ordinal, verdict := range outcome.Verdicts {
		if verdict.Ordinal != ordinal {
			return replay.PlainPayload{}, errors.New("Replay Claim Support verdict is invalid")
		}
	}
	return marshalReplayPayload(replay.ClassModelDecision, claimSupportVerdictReplay{
		replayPayloadHeader: replayHeader(replay.ClassModelDecision), Operation: "claim_support", Verdicts: outcome.Verdicts,
	})
}

type actionInputReplay struct {
	replayPayloadHeader
	ActionName      string          `json:"action_name"`
	LogicalActionID string          `json:"logical_action_id"`
	Input           json.RawMessage `json:"input"`
	DefaultTimeZone string          `json:"default_time_zone,omitempty"`
}

func EncodeActionInputReplay(actionName, logicalActionID string, request ActionRequest) (replay.PlainPayload, error) {
	budget := newReplaySizeBudget()
	budget.addString(actionName)
	budget.addString(logicalActionID)
	budget.addString(request.DefaultTimeZone)
	budget.addRaw(request.Input)
	if err := budget.err(); err != nil {
		return replay.PlainPayload{}, err
	}
	if !actionNamePattern.MatchString(actionName) || strings.TrimSpace(logicalActionID) == "" {
		return replay.PlainPayload{}, errors.New("Replay Action Input identity is invalid")
	}
	input, err := canonicalReplayObject(request.Input, true)
	if err != nil {
		return replay.PlainPayload{}, err
	}
	return marshalReplayPayload(replay.ClassActionInput, actionInputReplay{
		replayPayloadHeader: replayHeader(replay.ClassActionInput), ActionName: actionName,
		LogicalActionID: logicalActionID, Input: input, DefaultTimeZone: request.DefaultTimeZone,
	})
}

type actionResultReplay struct {
	replayPayloadHeader
	ActionName      string             `json:"action_name"`
	LogicalActionID string             `json:"logical_action_id"`
	Status          ActionResultStatus `json:"status"`
	Output          json.RawMessage    `json:"output,omitempty"`
	ErrorCode       string             `json:"error_code,omitempty"`
}

func EncodeActionResultReplay(actionName, logicalActionID string, result ActionResult) (replay.PlainPayload, error) {
	budget := newReplaySizeBudget()
	budget.addString(actionName)
	budget.addString(logicalActionID)
	budget.addString(result.ErrorCode)
	budget.addRaw(result.Output)
	if err := budget.err(); err != nil {
		return replay.PlainPayload{}, err
	}
	if !actionNamePattern.MatchString(actionName) || strings.TrimSpace(logicalActionID) == "" {
		return replay.PlainPayload{}, errors.New("Replay Action Result identity is invalid")
	}
	if err := result.Validate(); err != nil {
		return replay.PlainPayload{}, err
	}
	document := actionResultReplay{
		replayPayloadHeader: replayHeader(replay.ClassActionResult), ActionName: actionName,
		LogicalActionID: logicalActionID, Status: result.Status, ErrorCode: result.ErrorCode,
	}
	if result.Status == ActionSucceeded {
		output, err := canonicalReplayObject(result.Output, true)
		if err != nil {
			return replay.PlainPayload{}, err
		}
		document.Output = output
	}
	return marshalReplayPayload(replay.ClassActionResult, document)
}

func replayHeader(class replay.Class) replayPayloadHeader {
	return replayPayloadHeader{SchemaVersion: replayPayloadSchemaVersion, Class: class}
}

func marshalReplayPayload(class replay.Class, document any) (replay.PlainPayload, error) {
	encoded, err := json.Marshal(document)
	if err != nil {
		return replay.PlainPayload{}, err
	}
	return replay.NewPlainPayload(class, replayPayloadSchemaVersion, encoded)
}

func canonicalReplayObject(raw []byte, redact bool) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, errors.New("Replay structured payload must be a JSON object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("Replay structured payload has trailing JSON")
	}
	if redact {
		redactReplaySecrets(value)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

var replaySecretKeys = map[string]struct{}{
	"authorization": {}, "proxy_authorization": {}, "proxyauthorization": {}, "cookie": {}, "set_cookie": {}, "setcookie": {},
	"password": {}, "passwd": {}, "api_key": {}, "apikey": {}, "access_token": {}, "accesstoken": {},
	"refresh_token": {}, "refreshtoken": {}, "session_token": {}, "sessiontoken": {}, "lease_token": {}, "leasetoken": {},
	"client_secret": {}, "clientsecret": {}, "secret_access_key": {}, "secretaccesskey": {},
	"private_key": {}, "privatekey": {}, "token": {}, "secret": {},
}

func redactReplaySecrets(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(key))
			if _, secret := replaySecretKeys[normalized]; secret {
				delete(typed, key)
				continue
			}
			redactReplaySecrets(child)
		}
	case []any:
		for _, child := range typed {
			redactReplaySecrets(child)
		}
	}
}

type replaySizeBudget struct {
	upperBound int64
}

func newReplaySizeBudget() *replaySizeBudget {
	return &replaySizeBudget{upperBound: 4096}
}

func (b *replaySizeBudget) addString(value string) {
	b.upperBound += int64(len(value))*6 + 32
}

func (b *replaySizeBudget) addRaw(value []byte) {
	b.upperBound += int64(len(value))*6 + 32
}

func (b *replaySizeBudget) err() error {
	if b.upperBound > replay.MaxPlaintextBytes {
		return replay.ErrPayloadTooLarge
	}
	return nil
}

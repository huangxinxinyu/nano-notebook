package agent_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/replay"
)

func TestReplayPayloadCodecsCaptureOnlyNormalizedModelAndActionData(t *testing.T) {
	request, err := agent.EncodeModelRequestReplay(models.ModelRequest{
		Model: "openai/gpt-5",
		Messages: []models.ModelMessage{
			{Role: models.RoleSystem, Content: "Answer with one concise paragraph."},
			{Role: models.RoleUser, Content: "Plan dinner in Shanghai."},
			{Role: models.RoleAssistant, ActionCalls: []models.ModelActionCall{{
				ID: "decision:1/action:0", Name: "current_time",
				Input: json.RawMessage(`{"time_zone":"Asia/Shanghai","api_key":"sk-test","accessToken":"camel-secret","nested":{"clientSecret":"client-secret"}}`),
			}}},
			{Role: models.RoleAction, ActionCallID: "decision:1/action:0", Content: `{"status":"succeeded","output":{"local_time":"20:30","authorization":"Bearer hidden"}}`},
		},
		ActionDefinitions: []models.ActionDefinition{{
			Name: "current_time", Description: "Read the current time.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"time_zone":{"type":"string"}}}`),
		}},
	})
	if err != nil {
		t.Fatalf("EncodeModelRequestReplay: %v", err)
	}
	decision, err := agent.EncodeModelDecisionReplay(models.ModelDecision{Proposal: &models.ActionProposalBatch{
		Actions: []models.ActionProposal{{Name: "calculate", Input: json.RawMessage(`{"expression":"6*7","password":"password-value"}`)}},
	}})
	if err != nil {
		t.Fatalf("EncodeModelDecisionReplay: %v", err)
	}
	actionInput, err := agent.EncodeActionInputReplay("current_time", "decision:1/action:0", agent.ActionRequest{
		Input:           json.RawMessage(`{"time_zone":"Asia/Shanghai","nested":{"session_token":"cookie-value"}}`),
		DefaultTimeZone: "Asia/Shanghai",
	})
	if err != nil {
		t.Fatalf("EncodeActionInputReplay: %v", err)
	}
	actionResult, err := agent.EncodeActionResultReplay("current_time", "decision:1/action:0", agent.ActionResult{
		Status: agent.ActionSucceeded,
		Output: json.RawMessage(`{"local_time":"20:30","refresh_token":"refresh-value"}`),
	})
	if err != nil {
		t.Fatalf("EncodeActionResultReplay: %v", err)
	}

	got := []replay.PlainPayload{request, decision, actionInput, actionResult}
	wantClasses := []replay.Class{replay.ClassModelRequest, replay.ClassModelDecision, replay.ClassActionInput, replay.ClassActionResult}
	for index, payload := range got {
		if payload.Class != wantClasses[index] || payload.SchemaVersion != 1 || len(payload.Bytes) == 0 || payload.SHA256 == "" {
			t.Fatalf("payload %d = %#v", index, payload)
		}
		var document map[string]any
		if err := json.Unmarshal(payload.Bytes, &document); err != nil {
			t.Fatalf("payload %d JSON: %v", index, err)
		}
		if document["class"] != string(wantClasses[index]) || document["schema_version"] != float64(1) {
			t.Fatalf("payload %d header = %#v", index, document)
		}
	}
	joined := bytes.Join([][]byte{request.Bytes, decision.Bytes, actionInput.Bytes, actionResult.Bytes}, nil)
	for _, prohibited := range []string{"sk-test", "camel-secret", "client-secret", "Bearer hidden", "password-value", "cookie-value", "refresh-value", "provider_request_id", "chain_of_thought"} {
		if bytes.Contains(joined, []byte(prohibited)) {
			t.Errorf("Replay payload retained prohibited value %q: %s", prohibited, joined)
		}
	}
	for _, required := range []string{"Plan dinner in Shanghai.", "current_time", "Asia/Shanghai", "local_time", "20:30"} {
		if !bytes.Contains(joined, []byte(required)) {
			t.Errorf("Replay payload omitted normalized value %q: %s", required, joined)
		}
	}
}

func TestReplayPayloadCodecRejectsInvalidAndOversizedContent(t *testing.T) {
	if _, err := agent.EncodeActionInputReplay("calculate", "decision:1/action:0", agent.ActionRequest{
		Input: json.RawMessage(`{"expression":`),
	}); err == nil {
		t.Fatal("EncodeActionInputReplay accepted invalid JSON")
	}
	_, err := agent.EncodeModelDecisionReplay(models.ModelDecision{Final: &models.FinalDraft{
		Text: strings.Repeat("x", replay.MaxPlaintextBytes+1),
	}})
	if !errors.Is(err, replay.ErrPayloadTooLarge) {
		t.Fatalf("oversized Replay error = %v, want ErrPayloadTooLarge", err)
	}
}

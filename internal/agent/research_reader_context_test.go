package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

func TestReaderRequestsKeepOnlyTheLatestPageInFull(t *testing.T) {
	page := func(text string) string {
		encoded, _ := json.Marshal(map[string]any{"result_ref": "run:r/checkpoint:a", "offset": 0, "next_offset": 40000, "content": text})
		return string(encoded)
	}
	first, second := strings.Repeat("first page body ", 500), strings.Repeat("second page body ", 500)
	messages := []models.ModelMessage{
		{Role: models.RoleAssistant, ActionCalls: []models.ModelActionCall{{ID: "a", Name: "read_url"}}},
		{Role: models.RoleAction, ActionCallID: "a", Content: page(first)},
		{Role: models.RoleAssistant, ActionCalls: []models.ModelActionCall{{ID: "c", Name: "record_claim"}}},
		{Role: models.RoleAction, ActionCallID: "c", Content: `{"id":"a1","status":"verified"}`},
		{Role: models.RoleAssistant, ActionCalls: []models.ModelActionCall{{ID: "b", Name: "read_tool_result"}}},
		{Role: models.RoleAction, ActionCallID: "b", Content: page(second)},
	}
	out := elideEarlierReaderPages(messages)
	var elided map[string]any
	if err := json.Unmarshal([]byte(out[1].Content), &elided); err != nil {
		t.Fatalf("elided page is not JSON: %v", err)
	}
	if elided["result_ref"] != "run:r/checkpoint:a" || elided["next_offset"] != float64(40000) ||
		!strings.Contains(elided["content"].(string), "earlier page text omitted") || len(out[1].Content) > 1000 {
		t.Fatalf("elided=%v", elided)
	}
	if out[5].Content != messages[5].Content || out[3].Content != messages[3].Content {
		t.Fatal("latest page or a card result was changed")
	}
	if messages[1].Content != page(first) {
		t.Fatal("elision mutated the input messages")
	}
	if single := elideEarlierReaderPages(messages[:2]); single[1].Content != messages[1].Content {
		t.Fatal("a reader's only page was elided")
	}
}

func TestReaderPageElisionAppliesToReadersFromExecutorV36(t *testing.T) {
	reader := Execution{ParentRunID: "run_parent", SubagentTask: researchReaderTaskPrefix + " https://x (X).", AgentConfigID: "research.executor@36"}
	if !isResearchReaderContextExecution(reader) {
		t.Fatal("v36 reader is not elided")
	}
	for _, execution := range []Execution{
		{ParentRunID: "run_parent", SubagentTask: reader.SubagentTask, AgentConfigID: "research.executor@35"},
		{ParentRunID: "run_parent", SubagentTask: "Scout open discovery for the parent researcher.", AgentConfigID: "research.executor@36"},
		{SubagentTask: reader.SubagentTask, AgentConfigID: "research.executor@36"},
	} {
		if isResearchReaderContextExecution(execution) {
			t.Fatalf("elided %+v", execution)
		}
	}
}

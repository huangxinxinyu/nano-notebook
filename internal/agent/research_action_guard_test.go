package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResearchActionProposalDuplicateDetectionUsesCanonicalToolAndInput(t *testing.T) {
	payloads := [][]byte{
		[]byte(`{"actions":[{"action_id":"decision:1/action:0","index":0,"name":"read_url","input":{"url":"https://example.com/a"}}]}`),
		[]byte(`{"actions":[{"action_id":"decision:2/action:0","index":0,"name":"web_search","input":{"queries":["new"]}}]}`),
		[]byte(`{"actions":[{"action_id":"decision:3/action:0","index":0,"name":"read_url","input":{"url":"https://example.com/a"}}]}`),
	}
	if !hasRepeatedResearchAction(payloads, "read_url", json.RawMessage(`{"url":"https://example.com/a"}`)) {
		t.Fatal("duplicate read_url input was not detected")
	}
	if hasRepeatedResearchAction(payloads, "web_search", json.RawMessage(`{"queries":["new"]}`)) {
		t.Fatal("one accepted web_search input was classified as a duplicate")
	}
}

func TestResearchDuplicateActionPointsAtTheEarlierResult(t *testing.T) {
	payloads := [][]byte{
		[]byte(`{"actions":[{"action_id":"decision:1/action:0","index":0,"name":"read_url","input":{"url":"https://example.com/a"}}]}`),
		[]byte(`{"actions":[{"action_id":"decision:4/action:1","index":1,"name":"read_url","input":{"url":"https://example.com/a"}}]}`),
	}
	earlier, repeated := repeatedResearchAction(payloads, "read_url", json.RawMessage(`{"url":"https://example.com/a"}`))
	if !repeated || earlier != "decision:1/action:0" {
		t.Fatalf("earlier=%q repeated=%v", earlier, repeated)
	}
	detail := researchDuplicateActionError("run_x", earlier)
	if detail.Code != "research_duplicate_action" || !strings.Contains(detail.Suggestion, `"run:run_x/checkpoint:decision:1/action:0"`) {
		t.Fatalf("detail=%+v", detail)
	}
	if !toolResultReferencePattern.MatchString("run:run_x/checkpoint:decision:1/action:0") {
		t.Fatal("suggested result_ref is not accepted by read_tool_result")
	}
	if !isResearchDuplicateResult(ActionResult{Status: ActionDomainError, Error: detail}) {
		t.Fatal("structured duplicate result is not recognized by duplicate recovery")
	}
}

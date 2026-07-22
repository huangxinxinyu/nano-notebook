package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGroundedSystemPromptDescribesPlainTextSourceMarkerContract(t *testing.T) {
	for _, required := range []string{
		"always use search_evidence before answering",
		"[source:<source_id>]",
		"ordinary plain text",
		"omit Source markers",
	} {
		if !strings.Contains(GroundedSystemPrompt, required) {
			t.Fatalf("grounded prompt is missing %q", required)
		}
	}
	for _, forbidden := range []string{"claims", "must be only JSON", "verbatim"} {
		if strings.Contains(GroundedSystemPrompt, forbidden) {
			t.Fatalf("grounded prompt still contains %q", forbidden)
		}
	}
}

func TestGroundedRequiredActionDependsOnDurableSearchAttempt(t *testing.T) {
	tests := []struct {
		name   string
		prefix CheckpointPrefix
		want   string
	}{
		{name: "no search", want: "search_evidence"},
		{name: "complete empty search", prefix: groundedSearchPrefix(t, true, false, nil)},
		{name: "degraded empty search", prefix: groundedSearchPrefix(t, false, true, nil)},
		{name: "evidence metadata without citeable range", prefix: groundedSearchPrefix(t, false, false, []map[string]any{{
			"source_id": "src_a", "evidence_revision_id": "evr_a", "evidence_ranges": []map[string]any{},
		}})},
		{name: "citeable evidence", prefix: groundedSearchPrefix(t, false, true, []map[string]any{{
			"source_id": "src_a", "evidence_revision_id": "evr_a", "evidence_ranges": []map[string]any{{
				"unit_id": "unit_a", "start_rune": 2, "end_rune": 9,
			}},
		}})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := groundedRequiredAction(tt.prefix)
			if err != nil || got != tt.want {
				t.Fatalf("required action=%q err=%v, want %q", got, err, tt.want)
			}
		})
	}
}

func groundedSearchPrefix(t *testing.T, completeEmpty, degraded bool, evidence []map[string]any) CheckpointPrefix {
	t.Helper()
	output, err := json.Marshal(map[string]any{
		"complete_empty": completeEmpty,
		"degraded":       degraded,
		"evidence":       evidence,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := ActionResult{Status: ActionSucceeded, Output: output}
	return CheckpointPrefix{Proposals: []AcceptedProposal{{DecisionNo: 1, Actions: []AcceptedAction{{
		ActionID: "decision:1/action:0", Index: 0, Name: "search_evidence", Result: &result,
	}}}}}
}

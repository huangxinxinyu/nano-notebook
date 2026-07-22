package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

func TestGroundedSystemPromptDescribesEvidenceAwareFinalContract(t *testing.T) {
	for _, required := range []string{
		"Before any search_evidence result contains a citeable Evidence range",
		"plain text",
		"After any search_evidence result contains a citeable Evidence range",
		"must be only JSON",
	} {
		if !strings.Contains(GroundedSystemPrompt, required) {
			t.Fatalf("grounded prompt is missing %q", required)
		}
	}
	if strings.Contains(GroundedSystemPrompt, "fresh Source-free fallback") {
		t.Fatal("grounded prompt still describes the removed fallback call")
	}
}

func TestGroundedFinalDraftFormatDependsOnCiteableEvidence(t *testing.T) {
	tests := []struct {
		name   string
		prefix CheckpointPrefix
		want   string
	}{
		{name: "no search", want: models.FinalDraftFormatGroundedOptionalV1},
		{name: "complete empty search", prefix: groundedSearchPrefix(t, true, false, nil), want: models.FinalDraftFormatGroundedOptionalV1},
		{name: "degraded empty search", prefix: groundedSearchPrefix(t, false, true, nil), want: models.FinalDraftFormatGroundedOptionalV1},
		{name: "evidence metadata without citeable range", prefix: groundedSearchPrefix(t, false, false, []map[string]any{{
			"source_id": "src_a", "evidence_revision_id": "evr_a", "evidence_ranges": []map[string]any{},
		}}), want: models.FinalDraftFormatGroundedOptionalV1},
		{name: "citeable evidence", prefix: groundedSearchPrefix(t, false, true, []map[string]any{{
			"source_id": "src_a", "evidence_revision_id": "evr_a", "evidence_ranges": []map[string]any{{
				"unit_id": "unit_a", "start_rune": 2, "end_rune": 9,
			}},
		}}), want: models.FinalDraftFormatGroundedV1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := groundedFinalDraftFormat(tt.prefix)
			if err != nil || got != tt.want {
				t.Fatalf("format=%q err=%v, want %q", got, err, tt.want)
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

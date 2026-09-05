package agent

import (
	"encoding/json"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

func TestPreserveCurrentSearchQueryKeepsServerSelectedScope(t *testing.T) {
	fallback := models.ActionProposal{Name: "search_evidence", Input: json.RawMessage(`{
		"query":"conclusion",
		"purpose":"read the paper conclusion",
		"source_id":"src_1",
		"entry_id":"entry_conclusion"
	}`)}
	proposal := models.ActionProposal{Name: "search_evidence", Input: json.RawMessage(`{
		"query":"main findings and limitations",
		"purpose":"ground the conclusion"
	}`)}

	preserved, err := preserveCurrentSearchQuery(proposal, fallback)
	if err != nil {
		t.Fatal(err)
	}
	input, err := decodeSearchEvidenceInput(preserved.Input)
	if err != nil {
		t.Fatal(err)
	}
	if input.SourceID != "src_1" || input.EntryID != "entry_conclusion" {
		t.Fatalf("contextualized scope = %q/%q, want fallback scope", input.SourceID, input.EntryID)
	}
}

func TestPreserveCurrentSearchQueryCannotReplaceServerSelectedScope(t *testing.T) {
	fallback := models.ActionProposal{Name: "search_evidence", Input: json.RawMessage(`{
		"query":"conclusion",
		"purpose":"read the paper conclusion",
		"source_id":"src_authorized",
		"entry_id":"entry_authorized"
	}`)}
	proposal := models.ActionProposal{Name: "search_evidence", Input: json.RawMessage(`{
		"query":"conclusion details",
		"purpose":"ground the conclusion",
		"source_id":"src_other",
		"entry_id":"entry_other"
	}`)}

	preserved, err := preserveCurrentSearchQuery(proposal, fallback)
	if err != nil {
		t.Fatal(err)
	}
	input, err := decodeSearchEvidenceInput(preserved.Input)
	if err != nil {
		t.Fatal(err)
	}
	if input.SourceID != "src_authorized" || input.EntryID != "entry_authorized" {
		t.Fatalf("contextualized scope = %q/%q, want fallback scope", input.SourceID, input.EntryID)
	}
}

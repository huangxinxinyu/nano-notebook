package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
)

type evidenceSearchStub struct {
	attempt Attempt
	query   string
	purpose string
	result  retrieval.SearchResult
	err     error
}

func (s *evidenceSearchStub) SearchEvidence(_ context.Context, attempt Attempt, query, purpose string) (retrieval.SearchResult, error) {
	s.attempt, s.query, s.purpose = attempt, query, purpose
	return s.result, s.err
}

func TestSearchEvidenceActionUsesServerBoundAttemptAndReturnsEvidenceAddresses(t *testing.T) {
	backend := &evidenceSearchStub{result: retrieval.SearchResult{Candidates: []retrieval.EvidenceCandidate{{
		ID: "chunk_internal", SourceID: "src_a", RevisionID: "evr_a", SourceTitle: "Report",
		Preview: "Grounded passage", UnitRefs: []retrieval.UnitRef{{UnitID: "unit_a", StartRune: 2, EndRune: 19}},
	}}}}
	action := NewSearchEvidenceAction(backend)
	input := json.RawMessage(`{"query":"What changed?","purpose":"find the stated change"}`)
	if err := action.ValidateInput(input); err != nil {
		t.Fatal(err)
	}
	attempt := Attempt{RunID: "run_a", JobID: "job_a", AttemptNo: 2, LeaseToken: "lease_a"}
	result, err := action.Execute(context.Background(), ActionRequest{Input: input, Attempt: attempt})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ActionSucceeded || backend.attempt != attempt || backend.query != "What changed?" || backend.purpose != "find the stated change" {
		t.Fatalf("result/backend=%+v/%+v", result, backend)
	}
	var output struct {
		Evidence []struct {
			SourceID           string              `json:"source_id"`
			EvidenceRevisionID string              `json:"evidence_revision_id"`
			EvidenceRanges     []retrieval.UnitRef `json:"evidence_ranges"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Evidence) != 1 || output.Evidence[0].SourceID != "src_a" || output.Evidence[0].EvidenceRevisionID != "evr_a" ||
		len(output.Evidence[0].EvidenceRanges) != 1 || output.Evidence[0].EvidenceRanges[0].UnitID != "unit_a" {
		t.Fatalf("output=%s", result.Output)
	}
	if string(result.Output) == "" || bytes.Contains(result.Output, []byte(`"chunk_id"`)) || bytes.Contains(result.Output, []byte(`"index_version_id"`)) {
		t.Fatalf("Action leaked projection identity: %s", result.Output)
	}
}

func TestSearchEvidenceActionReportsCompleteEmptyAndDegradationWithoutInventingEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		result retrieval.SearchResult
		err    error
		status ActionResultStatus
		code   string
	}{
		{name: "complete empty", result: retrieval.SearchResult{CompleteEmpty: true}, status: ActionSucceeded},
		{name: "degraded", result: retrieval.SearchResult{Degraded: true, Degradations: []string{"reranker_unavailable"}}, status: ActionSucceeded},
		{name: "unavailable", err: retrieval.ErrRetrievalUnavailable, status: ActionDomainError, code: "retrieval_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := NewSearchEvidenceAction(&evidenceSearchStub{result: test.result, err: test.err}).Execute(context.Background(), ActionRequest{
				Input: json.RawMessage(`{"query":"q","purpose":"p"}`), Attempt: Attempt{RunID: "run"},
			})
			if err != nil || result.Status != test.status || result.ErrorCode != test.code {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestSearchEvidenceActionRejectsModelSuppliedScopeAndMalformedInput(t *testing.T) {
	action := NewSearchEvidenceAction(&evidenceSearchStub{})
	for _, input := range []string{
		`{"query":"q","purpose":"p","source_ids":["src_forged"]}`,
		`{"query":"","purpose":"p"}`,
		`{"query":"q"}`,
		`{"query":"q","purpose":"p"} trailing`,
	} {
		if err := action.ValidateInput(json.RawMessage(input)); err == nil {
			t.Fatalf("accepted input %s", input)
		}
	}
	if _, err := NewSearchEvidenceAction(nil).Execute(context.Background(), ActionRequest{Input: json.RawMessage(`{"query":"q","purpose":"p"}`)}); !errors.Is(err, ErrSearchEvidenceUnavailable) {
		t.Fatalf("nil backend error=%v", err)
	}
}

func TestSearchEvidenceDefinitionIsAvailableOnlyForRunsWithPinnedSources(t *testing.T) {
	registry, err := NewActionRegistry(NewSearchEvidenceAction(&evidenceSearchStub{}))
	if err != nil {
		t.Fatal(err)
	}
	empty := Execution{SelectedSourceCount: 0}
	selected := Execution{SelectedSourceCount: 2}
	if got := registry.Definitions(ActionPolicy{RemainingActions: 1, Execution: &empty}); len(got) != 0 {
		t.Fatalf("empty Run definitions=%+v", got)
	}
	if got := registry.Definitions(ActionPolicy{RemainingActions: 1, Execution: &selected}); len(got) != 1 || got[0].Name != "search_evidence" {
		t.Fatalf("selected Run definitions=%+v", got)
	}
}

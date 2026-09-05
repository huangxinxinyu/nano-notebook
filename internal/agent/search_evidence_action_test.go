package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
)

type evidenceSearchStub struct {
	attempt Attempt
	request EvidenceSearchRequest
	query   string
	purpose string
	result  EvidenceSearchResult
	err     error
}

func TestSearchEvidenceCheckpointStaysSmallWhenAuthoritativeCandidatesAreLarge(t *testing.T) {
	candidates := make([]retrieval.EvidenceCandidate, 0, maxSearchEvidenceCandidates)
	for index := 0; index < maxSearchEvidenceCandidates; index++ {
		refs := make([]retrieval.UnitRef, 0, 100)
		for range 100 {
			refs = append(refs, retrieval.UnitRef{UnitID: strings.Repeat("unit", 20), StartRune: 0, EndRune: 100})
		}
		candidates = append(candidates, retrieval.EvidenceCandidate{
			ID: fmt.Sprintf("chunk_%064d", index), SourceID: fmt.Sprintf("src_%d", index), RevisionID: fmt.Sprintf("evr_%d", index),
			SourceTitle: strings.Repeat("title", 100), Preview: strings.Repeat("large evidence body ", 1000), UnitRefs: refs,
		})
	}
	result, err := NewSearchEvidenceAction(&evidenceSearchStub{result: EvidenceSearchResult{SearchResult: retrieval.SearchResult{Candidates: candidates}}}).Execute(
		context.Background(), ActionRequest{Input: json.RawMessage(`{"query":"q","purpose":"p"}`), Attempt: Attempt{RunID: "run"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := NewActionResultCheckpoint(1, 0, "decision:1/action:0", result)
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoint.Payload) >= 4*1024 {
		t.Fatalf("compact checkpoint bytes=%d payload=%s", len(checkpoint.Payload), checkpoint.Payload)
	}
}

func (s *evidenceSearchStub) SearchEvidenceScoped(_ context.Context, attempt Attempt, request EvidenceSearchRequest) (EvidenceSearchResult, error) {
	s.attempt, s.request, s.query, s.purpose = attempt, request, request.Query, request.Purpose
	return s.result, s.err
}

func TestSearchEvidenceActionUsesServerBoundAttemptAndReturnsEvidenceAddresses(t *testing.T) {
	backend := &evidenceSearchStub{result: EvidenceSearchResult{SearchResult: retrieval.SearchResult{Candidates: []retrieval.EvidenceCandidate{{
		ID: "chunk_internal", SourceID: "src_a", RevisionID: "evr_a", SourceTitle: "Report",
		Preview: "Grounded passage", UnitRefs: []retrieval.UnitRef{{UnitID: "unit_a", StartRune: 2, EndRune: 19}},
	}}}}}
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
	if result.Status != ActionSucceeded || backend.attempt != attempt || backend.request.Query != "What changed?" || backend.request.Purpose != "find the stated change" {
		t.Fatalf("result/backend=%+v/%+v", result, backend)
	}
	var output struct {
		ResultVersion int `json:"result_version"`
		Evidence      []struct {
			SourceID           string `json:"source_id"`
			EvidenceRevisionID string `json:"evidence_revision_id"`
			ChunkID            string `json:"chunk_id"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.ResultVersion != 2 || len(output.Evidence) != 1 || output.Evidence[0].SourceID != "src_a" ||
		output.Evidence[0].EvidenceRevisionID != "evr_a" || output.Evidence[0].ChunkID != "chunk_internal" {
		t.Fatalf("output=%s", result.Output)
	}
	for _, forbidden := range [][]byte{[]byte(`"source_title"`), []byte(`"preview"`), []byte(`"evidence_ranges"`), []byte(`"index_version_id"`), []byte("unit_a"), []byte("Grounded passage")} {
		if bytes.Contains(result.Output, forbidden) {
			t.Fatalf("Action persisted model-only evidence data %q: %s", forbidden, result.Output)
		}
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
			result, err := NewSearchEvidenceAction(&evidenceSearchStub{result: EvidenceSearchResult{SearchResult: test.result}, err: test.err}).Execute(context.Background(), ActionRequest{
				Input: json.RawMessage(`{"query":"q","purpose":"p"}`), Attempt: Attempt{RunID: "run"},
			})
			if err != nil || result.Status != test.status || result.ErrorCode != test.code {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestSearchEvidenceActionPassesNavigationScopeAndHidesUnavailableLocator(t *testing.T) {
	backend := &evidenceSearchStub{result: EvidenceSearchResult{Scope: &EvidenceSearchResolvedScope{
		SourceID: "src_ready", EntryID: "entry_conclusion", PageStart: 14, PageEnd: 16,
	}}}
	action := NewSearchEvidenceAction(backend)
	input := json.RawMessage(`{"query":"conclusion","purpose":"summarize","source_id":"src_ready","entry_id":"entry_conclusion"}`)
	result, err := action.Execute(context.Background(), ActionRequest{Input: input, Attempt: Attempt{RunID: "run_ready"}})
	if err != nil || result.Status != ActionSucceeded || backend.request.SourceID != "src_ready" || backend.request.EntryID != "entry_conclusion" {
		t.Fatalf("result=%+v request=%+v err=%v", result, backend.request, err)
	}
	var output struct {
		Scope *EvidenceSearchResolvedScope `json:"scope"`
	}
	if json.Unmarshal(result.Output, &output) != nil || output.Scope == nil || *output.Scope != *backend.result.Scope {
		t.Fatalf("resolved scope missing from output: %s", result.Output)
	}

	backend.err = ErrEvidenceScopeUnavailable
	result, err = action.Execute(context.Background(), ActionRequest{Input: input, Attempt: Attempt{RunID: "run_ready"}})
	if err != nil || result.Status != ActionDomainError || result.ErrorCode != "evidence_scope_unavailable" {
		t.Fatalf("scope failure result=%+v err=%v", result, err)
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

func TestSearchEvidenceActionAcceptsSourceAndEntryScopeButRequiresTheirHierarchy(t *testing.T) {
	action := NewSearchEvidenceAction(&evidenceSearchStub{})
	for _, input := range []string{
		`{"query":"conclusion","purpose":"summarize the paper","source_id":"src_ready"}`,
		`{"query":"limitations","purpose":"summarize the section","source_id":"src_ready","entry_id":"entry_conclusion"}`,
	} {
		if err := action.ValidateInput(json.RawMessage(input)); err != nil {
			t.Fatalf("rejected scoped input %s: %v", input, err)
		}
	}
	if err := action.ValidateInput(json.RawMessage(`{"query":"conclusion","purpose":"summarize","entry_id":"entry_conclusion"}`)); err == nil {
		t.Fatal("accepted entry_id without source_id")
	}

	var schema map[string]any
	if err := json.Unmarshal(action.Definition().InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	if properties["source_id"] == nil || properties["entry_id"] == nil {
		t.Fatalf("scope properties missing from schema: %s", action.Definition().InputSchema)
	}
}

func TestSearchEvidenceDefinitionIsAvailableOnlyForRunsWithPinnedSources(t *testing.T) {
	registry, err := NewActionRegistry(NewSearchEvidenceAction(&evidenceSearchStub{}))
	if err != nil {
		t.Fatal(err)
	}
	empty := Execution{SelectedSourceCount: 0}
	selected := Execution{SelectedSourceCount: 2}
	if got := registry.Definitions(context.Background(), ActionPolicy{RemainingActions: 1, Execution: &empty}); len(got) != 0 {
		t.Fatalf("empty Run definitions=%+v", got)
	}
	if got := registry.Definitions(context.Background(), ActionPolicy{RemainingActions: 1, Execution: &selected}); len(got) != 1 || got[0].Name != "search_evidence" {
		t.Fatalf("selected Run definitions=%+v", got)
	}
}

func TestSearchEvidenceAvailableReturnsTypedReason(t *testing.T) {
	action := NewSearchEvidenceAction(&evidenceSearchStub{}).(ActionAvailability)
	if ok, reason := action.Available(Execution{}); ok || reason != "no_sources_selected" {
		t.Fatalf("empty ok=%t reason=%q", ok, reason)
	}
	if ok, reason := action.Available(Execution{SelectedSourceCount: 1}); !ok || reason != "" {
		t.Fatalf("selected ok=%t reason=%q", ok, reason)
	}
}

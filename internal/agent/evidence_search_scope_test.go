package agent

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/qdrantstore"
	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
	"github.com/huangxinxinyu/nano-notebook/internal/sourcemap"
)

type entryScopeResolverStub struct {
	scope   pinnedSearchScope
	entryID string
	result  entrySearchResolution
	err     error
}

type scopedVectorCapture struct {
	mu     sync.Mutex
	scopes []qdrantstore.Scope
}

func (s *scopedVectorCapture) SearchDense(_ context.Context, _ []float32, scope qdrantstore.Scope, _ int) ([]retrieval.Candidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scopes = append(s.scopes, scope)
	return nil, nil
}

func (s *scopedVectorCapture) SearchSparse(_ context.Context, _ retrieval.SparseVector, scope qdrantstore.Scope, _ int) ([]retrieval.Candidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scopes = append(s.scopes, scope)
	return nil, nil
}

type scopedEvidenceModelsStub struct{}

func (scopedEvidenceModelsStub) Embed(context.Context, models.EmbeddingRequest) (models.EmbeddingOutcome, error) {
	return models.EmbeddingOutcome{Vectors: [][]float32{{0.1, 0.2, 0.3}}}, nil
}

func (scopedEvidenceModelsStub) Rerank(context.Context, models.RerankRequest) (models.RerankOutcome, error) {
	return models.RerankOutcome{}, nil
}

func (s *entryScopeResolverStub) ResolveEntryScope(_ context.Context, scope pinnedSearchScope, entryID string) (entrySearchResolution, error) {
	s.scope, s.entryID = scope, entryID
	return s.result, s.err
}

func TestNarrowPinnedSearchScopeSelectsOnePinnedSourceWithoutExistenceLeak(t *testing.T) {
	input := pinnedSearchScope{
		NotebookID: "nb_ready",
		Evidence: []pinnedEvidence{
			{SourceID: "src_first", RevisionID: "evr_first", Title: "First"},
			{SourceID: "src_target", RevisionID: "evr_target", Title: "Target"},
		},
	}

	got, err := narrowPinnedSearchScope(input, "src_target")
	if err != nil || len(got.Evidence) != 1 || got.Evidence[0] != input.Evidence[1] || len(input.Evidence) != 2 {
		t.Fatalf("scope=%+v original=%+v err=%v", got, input, err)
	}
	for _, unavailable := range []string{"src_missing", " src_first\x00forged "} {
		if _, err := narrowPinnedSearchScope(input, unavailable); !errors.Is(err, ErrEvidenceScopeUnavailable) {
			t.Fatalf("source %q error=%v", unavailable, err)
		}
	}
}

func TestSourceScopeReachesDenseAndSparseBeforeTheirCandidateLimits(t *testing.T) {
	scope := pinnedSearchScope{
		NotebookID: "nb_ready",
		Version: retrieval.IndexVersion{ID: "riv_ready", Config: retrieval.IndexConfig{
			Chunk:      retrieval.ChunkConfig{MaxRunes: 64, OverlapRunes: 8},
			AnalyzerID: "nano-mixed-v1", BM25K1: 1.2, BM25B: 0.75, BM25AverageDocumentLength: 240,
			EmbeddingModel: "embedding", EmbeddingDimensions: 3, EmbeddingProfileID: retrieval.EmbeddingProfileGeminiRetrievalV1,
			DenseCandidates: 2, SparseCandidates: 3, RRFK: 60, RerankerID: "reranker", RerankCandidates: 1,
		}},
		Evidence: []pinnedEvidence{
			{SourceID: "src_other", RevisionID: "evr_other"},
			{SourceID: "src_target", RevisionID: "evr_target"},
		},
	}
	narrowed, err := narrowPinnedSearchScope(scope, "src_target")
	if err != nil {
		t.Fatal(err)
	}
	vectors := &scopedVectorCapture{}
	service := &EvidenceSearchService{vectors: vectors, models: scopedEvidenceModelsStub{}}
	result, err := service.searchEvidenceScope(context.Background(), narrowed, "paper conclusion", RetrievalSearchOverrides{}, true)
	if err != nil || !result.CompleteEmpty {
		t.Fatalf("search result=%+v err=%v", result, err)
	}
	if len(vectors.scopes) != 2 {
		t.Fatalf("vector scopes=%+v", vectors.scopes)
	}
	for _, got := range vectors.scopes {
		if len(got.Evidence) != 1 || got.Evidence[0] != (qdrantstore.EvidenceRef{SourceID: "src_target", RevisionID: "evr_target"}) {
			t.Fatalf("pre-top-k scope=%+v", got)
		}
	}
}

func TestResolveEntrySearchScopeDerivesEveryOverlappingChunkFromAuthoritativePages(t *testing.T) {
	units := []sourceInspectionUnit{
		{ID: "unit_intro", Ordinal: 0, Kind: "paragraph", Text: "Introduction evidence.", Coordinate: retrieval.EvidenceCoordinate{Kind: "pdf_region", Page: 1}},
		{ID: "unit_conclusion_a", Ordinal: 1, Kind: "heading", Text: "Conclusion", Coordinate: retrieval.EvidenceCoordinate{Kind: "pdf_region", Page: 3}},
		{ID: "unit_conclusion_b", Ordinal: 2, Kind: "paragraph", Text: "The principal conclusion.", Coordinate: retrieval.EvidenceCoordinate{Kind: "pdf_region", Page: 4}},
		{ID: "unit_references", Ordinal: 3, Kind: "paragraph", Text: "References.", Coordinate: retrieval.EvidenceCoordinate{Kind: "pdf_region", Page: 5}},
	}
	config := retrieval.ChunkConfig{MaxRunes: 32, OverlapRunes: 4, PreserveHeadingContext: true}
	value := sourcemap.SourceMap{
		SourceID: "src_ready", RevisionID: "evr_ready", PageCount: 5,
		Entries: []sourcemap.NavigationEntry{{
			EntryID: "entry_conclusion", Kind: "section", Heading: "Conclusion", PageStart: 3, PageEnd: 4,
		}},
	}

	resolution, err := resolveEntrySearchScope("src_ready", "entry_conclusion", value, units, "riv_ready", config)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Scope != (EvidenceSearchResolvedScope{SourceID: "src_ready", EntryID: "entry_conclusion", PageStart: 3, PageEnd: 4}) {
		t.Fatalf("resolved scope=%+v", resolution.Scope)
	}
	chunks, err := retrieval.BuildChunks("riv_ready", "evr_ready", []retrieval.Unit{
		{ID: "unit_intro", Ordinal: 0, Kind: "paragraph", Text: "Introduction evidence."},
		{ID: "unit_conclusion_a", Ordinal: 1, Kind: "heading", Text: "Conclusion"},
		{ID: "unit_conclusion_b", Ordinal: 2, Kind: "paragraph", Text: "The principal conclusion."},
		{ID: "unit_references", Ordinal: 3, Kind: "paragraph", Text: "References."},
	}, config)
	if err != nil {
		t.Fatal(err)
	}
	want := make([]string, 0)
	for _, chunk := range chunks {
		for _, ref := range chunk.UnitRefs {
			if ref.UnitID == "unit_conclusion_a" || ref.UnitID == "unit_conclusion_b" {
				want = append(want, chunk.ID)
				break
			}
		}
	}
	if !reflect.DeepEqual(resolution.AllowedChunkIDs, want) || len(want) == 0 {
		t.Fatalf("allowed=%v want=%v chunks=%+v", resolution.AllowedChunkIDs, want, chunks)
	}
}

func TestResolveEntrySearchScopeHidesMissingOrMismatchedEntry(t *testing.T) {
	value := sourcemap.SourceMap{
		SourceID: "src_ready", RevisionID: "evr_ready", PageCount: 1,
		Entries: []sourcemap.NavigationEntry{{EntryID: "entry_ready", PageStart: 1, PageEnd: 1}},
	}
	units := []sourceInspectionUnit{{
		ID: "unit_ready", Ordinal: 0, Kind: "paragraph", Text: "Evidence.",
		Coordinate: retrieval.EvidenceCoordinate{Kind: "pdf_region", Page: 1},
	}}
	config := retrieval.ChunkConfig{MaxRunes: 32, OverlapRunes: 4}
	for _, test := range []struct{ sourceID, entryID string }{
		{sourceID: "src_other", entryID: "entry_ready"},
		{sourceID: "src_ready", entryID: "entry_missing"},
	} {
		if _, err := resolveEntrySearchScope(test.sourceID, test.entryID, value, units, "riv_ready", config); !errors.Is(err, ErrEvidenceScopeUnavailable) {
			t.Fatalf("scope %+v error=%v", test, err)
		}
	}
}

func TestResolveRequestedSearchScopeCarriesEntryChunkRestriction(t *testing.T) {
	input := pinnedSearchScope{
		NotebookID: "nb_ready",
		Evidence: []pinnedEvidence{
			{SourceID: "src_other", RevisionID: "evr_other"},
			{SourceID: "src_ready", RevisionID: "evr_ready"},
		},
	}
	resolver := &entryScopeResolverStub{result: entrySearchResolution{
		Scope: EvidenceSearchResolvedScope{
			SourceID: "src_ready", EntryID: "entry_conclusion", PageStart: 3, PageEnd: 4,
		},
		AllowedChunkIDs: []string{"chunk_conclusion"},
	}}

	got, err := resolveRequestedSearchScope(context.Background(), input, EvidenceSearchRequest{
		SourceID: "src_ready", EntryID: "entry_conclusion",
	}, resolver)
	if err != nil || len(got.Evidence) != 1 || got.Evidence[0].SourceID != "src_ready" ||
		!reflect.DeepEqual(got.AllowedChunkIDs, []string{"chunk_conclusion"}) || got.ResolvedScope == nil ||
		*got.ResolvedScope != resolver.result.Scope || resolver.entryID != "entry_conclusion" || len(resolver.scope.Evidence) != 1 {
		t.Fatalf("scope=%+v resolver=%+v err=%v", got, resolver, err)
	}
}

func TestResolveRequestedSearchScopePreservesInfrastructureFailure(t *testing.T) {
	input := pinnedSearchScope{Evidence: []pinnedEvidence{{SourceID: "src_ready", RevisionID: "evr_ready"}}}
	want := errors.New("Source Map storage unavailable")
	resolver := &entryScopeResolverStub{err: want}

	_, err := resolveRequestedSearchScope(context.Background(), input, EvidenceSearchRequest{
		SourceID: "src_ready", EntryID: "entry_conclusion",
	}, resolver)
	if !errors.Is(err, want) {
		t.Fatalf("scope resolution error=%v, want infrastructure cause", err)
	}
}

func TestSearchEvidenceScopedRejectsEntryWithoutSourceBeforeRetrieval(t *testing.T) {
	service := &EvidenceSearchService{}
	_, err := service.SearchEvidenceScoped(context.Background(), Attempt{}, EvidenceSearchRequest{
		Query: "conclusion", Purpose: "ground conclusion", EntryID: "entry_conclusion",
	})
	if !errors.Is(err, ErrEvidenceScopeUnavailable) {
		t.Fatalf("entry-only request error=%v", err)
	}
}

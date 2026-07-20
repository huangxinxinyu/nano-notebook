package retrieval_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
)

func TestHybridPipelineReloadsAuthorityBeforeBoundedReranking(t *testing.T) {
	rerankedInput := []string(nil)
	pipeline := retrieval.Pipeline{
		Dense: func(context.Context, retrieval.SearchRequest) ([]retrieval.Candidate, error) {
			return []retrieval.Candidate{{ID: "unit_a", Score: .9}, {ID: "deleted", Score: .8}}, nil
		},
		Sparse: func(context.Context, retrieval.SearchRequest) ([]retrieval.Candidate, error) {
			return []retrieval.Candidate{{ID: "unit_b", Score: 12}, {ID: "unit_a", Score: 5}}, nil
		},
		Reload: func(_ context.Context, scope retrieval.Scope, ids []string) ([]retrieval.EvidenceCandidate, error) {
			if !reflect.DeepEqual(scope.SourceIDs, []string{"src_1"}) {
				t.Fatalf("scope = %+v", scope)
			}
			return []retrieval.EvidenceCandidate{
				{ID: "unit_a", Preview: "authoritative A"}, {ID: "unit_b", Preview: "authoritative B"},
			}, nil
		},
		Rerank: func(_ context.Context, _ string, candidates []retrieval.EvidenceCandidate) ([]string, error) {
			for _, candidate := range candidates {
				rerankedInput = append(rerankedInput, candidate.ID)
			}
			return []string{"unit_b", "unit_a"}, nil
		},
	}
	result, err := pipeline.Search(context.Background(), retrieval.SearchRequest{
		Query: "evidence", Scope: retrieval.Scope{NotebookID: "nb_1", SourceIDs: []string{"src_1"}},
		DenseLimit: 10, SparseLimit: 10, RerankLimit: 2, MinimumSurvivors: 1, RRFK: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Degraded || result.CompleteEmpty || !reflect.DeepEqual(rerankedInput, []string{"unit_a", "unit_b"}) ||
		len(result.Candidates) != 2 || result.Candidates[0].ID != "unit_b" {
		t.Fatalf("result=%+v reranked=%v", result, rerankedInput)
	}
}

func TestHybridPipelineAppliesExplicitDegradationMatrix(t *testing.T) {
	unavailable := errors.New("channel unavailable")
	base := retrieval.Pipeline{
		Dense: func(context.Context, retrieval.SearchRequest) ([]retrieval.Candidate, error) { return nil, unavailable },
		Sparse: func(context.Context, retrieval.SearchRequest) ([]retrieval.Candidate, error) {
			return []retrieval.Candidate{{ID: "unit_a", Score: 4}, {ID: "unit_b", Score: 3}}, nil
		},
		Reload: func(_ context.Context, _ retrieval.Scope, ids []string) ([]retrieval.EvidenceCandidate, error) {
			result := make([]retrieval.EvidenceCandidate, 0, len(ids))
			for _, id := range ids {
				result = append(result, retrieval.EvidenceCandidate{ID: id, Preview: id})
			}
			return result, nil
		},
		Rerank: func(context.Context, string, []retrieval.EvidenceCandidate) ([]string, error) {
			return nil, unavailable
		},
	}
	request := retrieval.SearchRequest{
		Query: "query", Scope: retrieval.Scope{NotebookID: "nb", SourceIDs: []string{"src"}},
		DenseLimit: 5, SparseLimit: 5, RerankLimit: 2, MinimumSurvivors: 2, RRFK: 60,
	}
	result, err := base.Search(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Degraded || result.CompleteEmpty || !reflect.DeepEqual(result.Degradations, []string{"dense_unavailable", "reranker_unavailable"}) ||
		len(result.Candidates) != 2 || result.Candidates[0].ID != "unit_a" {
		t.Fatalf("degraded result = %+v", result)
	}

	request.MinimumSurvivors = 3
	if _, err := base.Search(context.Background(), request); !errors.Is(err, retrieval.ErrRetrievalUnavailable) {
		t.Fatalf("below-minimum survivor error = %v", err)
	}
	base.Sparse = func(context.Context, retrieval.SearchRequest) ([]retrieval.Candidate, error) { return nil, unavailable }
	if _, err := base.Search(context.Background(), request); !errors.Is(err, retrieval.ErrRetrievalUnavailable) {
		t.Fatalf("both-channel error = %v", err)
	}
}

func TestHybridPipelineMarksCompleteEmptyOnlyWhenBothChannelsComplete(t *testing.T) {
	pipeline := retrieval.Pipeline{
		Dense: func(context.Context, retrieval.SearchRequest) ([]retrieval.Candidate, error) {
			return []retrieval.Candidate{}, nil
		},
		Sparse: func(context.Context, retrieval.SearchRequest) ([]retrieval.Candidate, error) {
			return []retrieval.Candidate{}, nil
		},
		Reload: func(context.Context, retrieval.Scope, []string) ([]retrieval.EvidenceCandidate, error) {
			return nil, nil
		},
	}
	result, err := pipeline.Search(context.Background(), retrieval.SearchRequest{
		Query: "nothing", Scope: retrieval.Scope{NotebookID: "nb", SourceIDs: []string{"src"}},
		DenseLimit: 5, SparseLimit: 5, RerankLimit: 2, MinimumSurvivors: 1, RRFK: 60,
	})
	if err != nil || !result.CompleteEmpty || result.Degraded || len(result.Candidates) != 0 {
		t.Fatalf("empty result=%+v err=%v", result, err)
	}
}

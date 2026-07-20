package retrieval

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrInvalidSearchRequest = errors.New("invalid search request")
	ErrRetrievalUnavailable = errors.New("retrieval unavailable")
)

type Scope struct {
	NotebookID  string
	SourceIDs   []string
	RevisionIDs []string
}

type SearchRequest struct {
	Query            string
	Scope            Scope
	DenseLimit       int
	SparseLimit      int
	RerankLimit      int
	MinimumSurvivors int
	RRFK             int
}

type EvidenceCandidate struct {
	ID          string
	SourceID    string
	RevisionID  string
	SourceTitle string
	Preview     string
	UnitRefs    []UnitRef
}

type SearchResult struct {
	Candidates    []EvidenceCandidate
	Degraded      bool
	CompleteEmpty bool
	Degradations  []string
}

type DenseSearchFunc func(context.Context, SearchRequest) ([]Candidate, error)
type SparseSearchFunc func(context.Context, SearchRequest) ([]Candidate, error)
type AuthorityReloadFunc func(context.Context, Scope, []string) ([]EvidenceCandidate, error)
type RerankFunc func(context.Context, string, []EvidenceCandidate) ([]string, error)

type Pipeline struct {
	Dense  DenseSearchFunc
	Sparse SparseSearchFunc
	Reload AuthorityReloadFunc
	Rerank RerankFunc
}

func (p Pipeline) Search(ctx context.Context, request SearchRequest) (SearchResult, error) {
	if err := validateSearchRequest(request); err != nil {
		return SearchResult{}, err
	}

	dense, denseErr := runSearchChannel(ctx, p.Dense, request, request.DenseLimit)
	sparse, sparseErr := runSearchChannel(ctx, p.Sparse, request, request.SparseLimit)
	if denseErr != nil && sparseErr != nil {
		return SearchResult{}, fmt.Errorf("%w: dense and BM25 channels failed", ErrRetrievalUnavailable)
	}

	result := SearchResult{}
	channels := make(map[string][]Candidate, 2)
	if denseErr != nil {
		result.Degradations = append(result.Degradations, "dense_unavailable")
	} else {
		channels["dense"] = dense
	}
	if sparseErr != nil {
		result.Degradations = append(result.Degradations, "bm25_unavailable")
	} else {
		channels["bm25"] = sparse
	}
	result.Degraded = len(result.Degradations) > 0

	if !result.Degraded && len(dense) == 0 && len(sparse) == 0 {
		result.CompleteEmpty = true
		return result, nil
	}

	if result.Degraded {
		for _, candidates := range channels {
			if len(candidates) < request.MinimumSurvivors {
				return SearchResult{}, fmt.Errorf("%w: surviving channel returned %d candidates, need %d", ErrRetrievalUnavailable, len(candidates), request.MinimumSurvivors)
			}
		}
	}

	fused, err := FuseRRF(channels, request.RRFK)
	if err != nil {
		return SearchResult{}, fmt.Errorf("%w: invalid channel result: %v", ErrRetrievalUnavailable, err)
	}
	if len(fused) > request.RerankLimit {
		fused = fused[:request.RerankLimit]
	}
	ids := make([]string, 0, len(fused))
	for _, candidate := range fused {
		ids = append(ids, candidate.ID)
	}

	if len(ids) == 0 {
		if result.Degraded {
			return SearchResult{}, fmt.Errorf("%w: surviving channel returned no candidates", ErrRetrievalUnavailable)
		}
		result.CompleteEmpty = true
		return result, nil
	}
	if p.Reload == nil {
		return SearchResult{}, fmt.Errorf("%w: authority reload is not configured", ErrRetrievalUnavailable)
	}
	reloaded, err := p.Reload(ctx, request.Scope, ids)
	if err != nil {
		return SearchResult{}, fmt.Errorf("%w: authority reload failed: %v", ErrRetrievalUnavailable, err)
	}
	authoritative, err := orderAuthoritativeCandidates(ids, reloaded)
	if err != nil {
		return SearchResult{}, fmt.Errorf("%w: invalid authority result: %v", ErrRetrievalUnavailable, err)
	}
	if result.Degraded && len(authoritative) < request.MinimumSurvivors {
		return SearchResult{}, fmt.Errorf("%w: authority reload retained %d candidates, need %d", ErrRetrievalUnavailable, len(authoritative), request.MinimumSurvivors)
	}
	if len(authoritative) == 0 {
		if result.Degraded {
			return SearchResult{}, fmt.Errorf("%w: authority reload retained no candidates", ErrRetrievalUnavailable)
		}
		result.CompleteEmpty = true
		return result, nil
	}

	result.Candidates = authoritative
	if p.Rerank == nil {
		return result, nil
	}
	orderedIDs, err := p.Rerank(ctx, request.Query, append([]EvidenceCandidate(nil), authoritative...))
	if err != nil {
		result.Degraded = true
		result.Degradations = append(result.Degradations, "reranker_unavailable")
		return result, nil
	}
	result.Candidates, err = applyRerankOrder(authoritative, orderedIDs)
	if err != nil {
		return SearchResult{}, fmt.Errorf("%w: invalid reranker result: %v", ErrRetrievalUnavailable, err)
	}
	return result, nil
}

func validateSearchRequest(request SearchRequest) error {
	if strings.TrimSpace(request.Query) == "" || strings.TrimSpace(request.Scope.NotebookID) == "" ||
		len(request.Scope.SourceIDs) == 0 || request.DenseLimit <= 0 || request.SparseLimit <= 0 ||
		request.RerankLimit <= 0 || request.MinimumSurvivors <= 0 || request.RRFK <= 0 {
		return ErrInvalidSearchRequest
	}
	for _, sourceID := range request.Scope.SourceIDs {
		if strings.TrimSpace(sourceID) == "" {
			return ErrInvalidSearchRequest
		}
	}
	return nil
}

func runSearchChannel(ctx context.Context, search func(context.Context, SearchRequest) ([]Candidate, error), request SearchRequest, limit int) ([]Candidate, error) {
	if search == nil {
		return nil, errors.New("channel is not configured")
	}
	candidates, err := search(ctx, request)
	if err != nil {
		return nil, err
	}
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, nil
}

func orderAuthoritativeCandidates(ids []string, candidates []EvidenceCandidate) ([]EvidenceCandidate, error) {
	wanted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	byID := make(map[string]EvidenceCandidate, len(candidates))
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.ID) == "" {
			return nil, errors.New("candidate identity is required")
		}
		if _, ok := wanted[candidate.ID]; !ok {
			return nil, errors.New("authority reload expanded the candidate set")
		}
		if _, duplicate := byID[candidate.ID]; duplicate {
			return nil, errors.New("authority reload returned a duplicate candidate")
		}
		byID[candidate.ID] = candidate
	}
	ordered := make([]EvidenceCandidate, 0, len(candidates))
	for _, id := range ids {
		if candidate, ok := byID[id]; ok {
			ordered = append(ordered, candidate)
		}
	}
	return ordered, nil
}

func applyRerankOrder(candidates []EvidenceCandidate, ids []string) ([]EvidenceCandidate, error) {
	if len(ids) != len(candidates) {
		return nil, errors.New("reranker must return an exact permutation")
	}
	byID := make(map[string]EvidenceCandidate, len(candidates))
	for _, candidate := range candidates {
		byID[candidate.ID] = candidate
	}
	ordered := make([]EvidenceCandidate, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		candidate, ok := byID[id]
		if !ok {
			return nil, errors.New("reranker expanded the candidate set")
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, errors.New("reranker returned a duplicate candidate")
		}
		seen[id] = struct{}{}
		ordered = append(ordered, candidate)
	}
	return ordered, nil
}

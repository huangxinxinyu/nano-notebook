package retrieval

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
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
	Diagnostics   SearchDiagnostics
}

type SearchStageDiagnostics struct {
	Completed           bool
	DurationNanoseconds int64
	CandidateIDs        []string
}

type SearchDiagnostics struct {
	Dense        SearchStageDiagnostics
	BM25         SearchStageDiagnostics
	Fused        SearchStageDiagnostics
	EvidenceLoad SearchStageDiagnostics
	Rerank       SearchStageDiagnostics
	Degradations []string
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

	result := SearchResult{}
	denseStarted := time.Now()
	dense, denseErr := runSearchChannel(ctx, p.Dense, request, request.DenseLimit)
	result.Diagnostics.Dense = candidateStage(denseErr == nil, time.Since(denseStarted), candidateIDs(dense))
	sparseStarted := time.Now()
	sparse, sparseErr := runSearchChannel(ctx, p.Sparse, request, request.SparseLimit)
	result.Diagnostics.BM25 = candidateStage(sparseErr == nil, time.Since(sparseStarted), candidateIDs(sparse))
	if denseErr != nil && sparseErr != nil {
		return SearchResult{}, fmt.Errorf("%w: dense and BM25 channels failed", ErrRetrievalUnavailable)
	}

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
	result.Diagnostics.Degradations = append([]string(nil), result.Degradations...)

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

	fusedStarted := time.Now()
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
	result.Diagnostics.Fused = candidateStage(true, time.Since(fusedStarted), ids)

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
	reloadStarted := time.Now()
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
	result.Diagnostics.EvidenceLoad = candidateStage(true, time.Since(reloadStarted), evidenceCandidateIDs(authoritative))
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
	rerankStarted := time.Now()
	orderedIDs, err := p.Rerank(ctx, request.Query, append([]EvidenceCandidate(nil), authoritative...))
	if err != nil {
		result.Degraded = true
		result.Degradations = append(result.Degradations, "reranker_unavailable")
		result.Diagnostics.Rerank = candidateStage(false, time.Since(rerankStarted), nil)
		result.Diagnostics.Degradations = append([]string(nil), result.Degradations...)
		return result, nil
	}
	result.Candidates, err = applyRerankOrder(authoritative, orderedIDs)
	if err != nil {
		return SearchResult{}, fmt.Errorf("%w: invalid reranker result: %v", ErrRetrievalUnavailable, err)
	}
	result.Diagnostics.Rerank = candidateStage(true, time.Since(rerankStarted), evidenceCandidateIDs(result.Candidates))
	return result, nil
}

func candidateStage(completed bool, duration time.Duration, ids []string) SearchStageDiagnostics {
	return SearchStageDiagnostics{Completed: completed, DurationNanoseconds: duration.Nanoseconds(), CandidateIDs: append([]string(nil), ids...)}
}

func candidateIDs(candidates []Candidate) []string {
	result := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, candidate.ID)
	}
	return result
}

func evidenceCandidateIDs(candidates []EvidenceCandidate) []string {
	result := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, candidate.ID)
	}
	return result
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

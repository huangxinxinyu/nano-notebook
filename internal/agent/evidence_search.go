package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/qdrantstore"
	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxSearchEvidenceCandidates = 10

type evidenceVectorSearcher interface {
	SearchDense(context.Context, []float32, qdrantstore.Scope, int) ([]retrieval.Candidate, error)
	SearchSparse(context.Context, retrieval.SparseVector, qdrantstore.Scope, int) ([]retrieval.Candidate, error)
}

type evidenceModelCapabilities interface {
	Embed(context.Context, models.EmbeddingRequest) (models.EmbeddingOutcome, error)
	Rerank(context.Context, models.RerankRequest) (models.RerankOutcome, error)
}

type EvidenceSearchService struct {
	pool        *pgxpool.Pool
	vectors     evidenceVectorSearcher
	models      evidenceModelCapabilities
	entryScopes evidenceEntryScopeResolver
	metrics     *TaskMetricsRecorder
}

type RetrievalSearchOverrides struct {
	DenseCandidates  int
	SparseCandidates int
	RRFK             int
	RerankCandidates int
	SkipRerank       bool
}

// WithMetrics attaches the Sprint 12 task-lifecycle metrics recorder and
// returns the same Service, chainable onto NewEvidenceSearchService.
func (s *EvidenceSearchService) WithMetrics(recorder *TaskMetricsRecorder) *EvidenceSearchService {
	s.metrics = recorder
	return s
}

type pinnedEvidence struct {
	SourceID   string
	RevisionID string
	Title      string
}

type pinnedSearchScope struct {
	NotebookID      string
	Version         retrieval.IndexVersion
	Evidence        []pinnedEvidence
	AllowedChunkIDs []string
	ResolvedScope   *EvidenceSearchResolvedScope
}

func NewEvidenceSearchService(pool *pgxpool.Pool, vectors evidenceVectorSearcher, modelCapabilities evidenceModelCapabilities) *EvidenceSearchService {
	return &EvidenceSearchService{pool: pool, vectors: vectors, models: modelCapabilities}
}

func (s *EvidenceSearchService) WithSourceMapObjects(objects sourceInspectionObjectReader) *EvidenceSearchService {
	if s != nil {
		s.entryScopes = &postgresEvidenceEntryScopeResolver{service: s, objects: objects}
	}
	return s
}

func (s *EvidenceSearchService) SearchEvidence(ctx context.Context, attempt Attempt, query, _ string) (retrieval.SearchResult, error) {
	return s.searchEvidence(ctx, attempt, query, RetrievalSearchOverrides{}, true)
}

func (s *EvidenceSearchService) SearchEvidenceScoped(ctx context.Context, attempt Attempt, request EvidenceSearchRequest) (EvidenceSearchResult, error) {
	if strings.TrimSpace(request.EntryID) != "" && strings.TrimSpace(request.SourceID) == "" {
		return EvidenceSearchResult{}, ErrEvidenceScopeUnavailable
	}
	if strings.TrimSpace(request.SourceID) == "" {
		result, err := s.searchEvidence(ctx, attempt, request.Query, RetrievalSearchOverrides{}, true)
		return EvidenceSearchResult{SearchResult: result}, err
	}
	if s == nil || s.pool == nil || s.vectors == nil || s.models == nil || strings.TrimSpace(request.Query) == "" {
		return EvidenceSearchResult{}, ErrSearchEvidenceUnavailable
	}
	scope, err := s.loadPinnedScope(ctx, attempt)
	if err != nil {
		return EvidenceSearchResult{}, err
	}
	scope, err = resolveRequestedSearchScope(ctx, scope, request, s.entryScopes)
	if err != nil {
		return EvidenceSearchResult{}, err
	}
	result, err := s.searchEvidenceScope(ctx, scope, request.Query, RetrievalSearchOverrides{}, true)
	return EvidenceSearchResult{
		SearchResult: result,
		Scope:        cloneEvidenceSearchResolvedScope(scope.ResolvedScope),
	}, err
}

func narrowPinnedSearchScope(scope pinnedSearchScope, sourceID string) (pinnedSearchScope, error) {
	sourceID = strings.TrimSpace(sourceID)
	for _, item := range scope.Evidence {
		if item.SourceID != sourceID {
			continue
		}
		narrowed := scope
		narrowed.Evidence = []pinnedEvidence{item}
		return narrowed, nil
	}
	return pinnedSearchScope{}, ErrEvidenceScopeUnavailable
}

// SearchEvidenceWithOverrides runs the same production retrieval pipeline as
// SearchEvidence, but lets a sweep executor replace the query-time Index Config
// values. This is a deliberate divergence from the normal Agent path: RerankLimit
// is not capped at maxSearchEvidenceCandidates, and an RRF-only sweep can
// explicitly skip the configured reranker.
func (s *EvidenceSearchService) SearchEvidenceWithOverrides(ctx context.Context, attempt Attempt, query string, overrides RetrievalSearchOverrides) (retrieval.SearchResult, error) {
	return s.searchEvidence(ctx, attempt, query, overrides, false)
}

func (s *EvidenceSearchService) searchEvidence(ctx context.Context, attempt Attempt, query string, overrides RetrievalSearchOverrides, capRerank bool) (retrieval.SearchResult, error) {
	if s == nil || s.pool == nil || s.vectors == nil || s.models == nil || strings.TrimSpace(query) == "" {
		return retrieval.SearchResult{}, ErrSearchEvidenceUnavailable
	}
	scope, err := s.loadPinnedScope(ctx, attempt)
	if err != nil {
		return retrieval.SearchResult{}, err
	}
	return s.searchEvidenceScope(ctx, scope, query, overrides, capRerank)
}

func (s *EvidenceSearchService) searchEvidenceScope(ctx context.Context, scope pinnedSearchScope, query string, overrides RetrievalSearchOverrides, capRerank bool) (retrieval.SearchResult, error) {
	embeddingQuery, err := retrieval.FormatEmbeddingQuery(scope.Version.Config.EmbeddingProfileID, query)
	if err != nil {
		return retrieval.SearchResult{}, fmt.Errorf("%w: dense query profile: %v", retrieval.ErrRetrievalUnavailable, err)
	}
	embedded, err := s.models.Embed(ctx, models.EmbeddingRequest{
		Model: scope.Version.Config.EmbeddingModel, Inputs: []string{embeddingQuery}, Dimensions: scope.Version.Config.EmbeddingDimensions,
	})
	if err != nil || len(embedded.Vectors) != 1 {
		if err == nil {
			err = errors.New("embedding returned the wrong vector count")
		}
		return retrieval.SearchResult{}, fmt.Errorf("%w: dense query embedding: %v", retrieval.ErrRetrievalUnavailable, err)
	}
	encoder, err := retrieval.NewSparseEncoder(
		retrieval.NewMixedAnalyzer(scope.Version.Config.AnalyzerID), scope.Version.Config.BM25K1,
		scope.Version.Config.BM25B, scope.Version.Config.BM25AverageDocumentLength,
	)
	if err != nil {
		return retrieval.SearchResult{}, fmt.Errorf("%w: sparse query configuration: %v", retrieval.ErrRetrievalUnavailable, err)
	}
	sparse, err := encoder.Query(query)
	if err != nil {
		return retrieval.SearchResult{}, fmt.Errorf("%w: sparse query encoding: %v", retrieval.ErrRetrievalUnavailable, err)
	}
	qdrantScope := qdrantstore.Scope{
		NotebookID: scope.NotebookID, IndexVersionID: scope.Version.ID,
		Evidence:        make([]qdrantstore.EvidenceRef, 0, len(scope.Evidence)),
		AllowedChunkIDs: append([]string(nil), scope.AllowedChunkIDs...),
	}
	sourceIDs := make([]string, 0, len(scope.Evidence))
	revisionIDs := make([]string, 0, len(scope.Evidence))
	for _, item := range scope.Evidence {
		qdrantScope.Evidence = append(qdrantScope.Evidence, qdrantstore.EvidenceRef{SourceID: item.SourceID, RevisionID: item.RevisionID})
		sourceIDs = append(sourceIDs, item.SourceID)
		revisionIDs = append(revisionIDs, item.RevisionID)
	}
	denseLimit := overrideOrDefault(overrides.DenseCandidates, scope.Version.Config.DenseCandidates)
	sparseLimit := overrideOrDefault(overrides.SparseCandidates, scope.Version.Config.SparseCandidates)
	rrfK := overrideOrDefault(overrides.RRFK, scope.Version.Config.RRFK)
	rerankLimit := overrideOrDefault(overrides.RerankCandidates, scope.Version.Config.RerankCandidates)
	if capRerank && rerankLimit > maxSearchEvidenceCandidates {
		rerankLimit = maxSearchEvidenceCandidates
	}
	pipeline := retrieval.Pipeline{
		Dense: func(ctx context.Context, _ retrieval.SearchRequest) ([]retrieval.Candidate, error) {
			return s.vectors.SearchDense(ctx, embedded.Vectors[0], qdrantScope, denseLimit)
		},
		Sparse: func(ctx context.Context, _ retrieval.SearchRequest) ([]retrieval.Candidate, error) {
			return s.vectors.SearchSparse(ctx, sparse, qdrantScope, sparseLimit)
		},
		Reload: func(ctx context.Context, _ retrieval.Scope, ids []string) ([]retrieval.EvidenceCandidate, error) {
			return s.reloadCandidates(ctx, scope, ids)
		},
		Rerank: func(ctx context.Context, query string, candidates []retrieval.EvidenceCandidate) (retrieval.RerankResult, error) {
			items := make([]models.RerankCandidate, 0, len(candidates))
			for _, candidate := range candidates {
				items = append(items, models.RerankCandidate{ID: candidate.ID, Text: candidate.Preview})
			}
			outcome, err := s.models.Rerank(ctx, models.RerankRequest{
				Model: scope.Version.Config.RerankerID, Query: query, Candidates: items, TopN: len(items),
			})
			return retrieval.RerankResult{OrderedIDs: outcome.CandidateIDs, Scores: outcome.Scores}, err
		},
	}
	searchStartedAt := time.Now()
	request := retrievalSearchRequest(query, retrieval.Scope{NotebookID: scope.NotebookID, SourceIDs: sourceIDs, RevisionIDs: revisionIDs}, denseLimit, sparseLimit, rerankLimit, rrfK, scope.Version.Config.RerankRelevanceThreshold, overrides.SkipRerank)
	result, searchErr := pipeline.Search(ctx, request)
	s.recordSearchMetrics(result, searchErr, searchStartedAt)
	return result, searchErr
}

func overrideOrDefault(override, fallback int) int {
	if override > 0 {
		return override
	}
	return fallback
}

func retrievalSearchRequest(query string, scope retrieval.Scope, denseLimit, sparseLimit, rerankLimit, rrfK int, rerankRelevanceThreshold float64, skipRerank bool) retrieval.SearchRequest {
	return retrieval.SearchRequest{
		Query: query, Scope: scope, DenseLimit: denseLimit, SparseLimit: sparseLimit,
		RerankLimit: rerankLimit, MinimumSurvivors: 1, RRFK: rrfK,
		RerankRelevanceThreshold: rerankRelevanceThreshold, SkipRerank: skipRerank,
	}
}

// recordSearchMetrics emits nano_retrieval_search_seconds, nano_retrieval_stage_seconds,
// and nano_retrieval_degraded_total from the SearchDiagnostics that
// retrieval.Pipeline.Search already computed (internal/retrieval/pipeline.go),
// without adding any new timing code inside internal/retrieval itself
// (PRD criteria 38-39, 71).
func (s *EvidenceSearchService) recordSearchMetrics(result retrieval.SearchResult, searchErr error, startedAt time.Time) {
	if s.metrics == nil {
		return
	}
	outcome := "completed"
	if searchErr != nil {
		outcome = "failed"
	}
	s.metrics.RecordRetrievalSearch(outcome, time.Since(startedAt))
	if searchErr != nil {
		return
	}
	stages := map[string]retrieval.SearchStageDiagnostics{
		"dense": result.Diagnostics.Dense, "bm25": result.Diagnostics.BM25, "fuse": result.Diagnostics.Fused,
		"evidence_load": result.Diagnostics.EvidenceLoad, "rerank": result.Diagnostics.Rerank,
	}
	for stage, diagnostics := range stages {
		if diagnostics.DurationNanoseconds == 0 && !diagnostics.Completed {
			continue
		}
		stageOutcome := "completed"
		if !diagnostics.Completed {
			stageOutcome = "failed"
		}
		s.metrics.RecordRetrievalStage(stage, stageOutcome, time.Duration(diagnostics.DurationNanoseconds))
	}
	for _, degradation := range result.Degradations {
		s.metrics.RecordRetrievalDegraded(degradation)
	}
}

func (s *EvidenceSearchService) loadPinnedScope(ctx context.Context, attempt Attempt) (pinnedSearchScope, error) {
	tx, err := s.workerTx(ctx)
	if err != nil {
		return pinnedSearchScope{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var selectedCount int
	err = tx.QueryRow(ctx, `
		select coalesce(r.selected_source_count,0)
		from agent_runs r join agent_jobs j on j.run_id=r.id
		left join agent_trees tree on tree.id=r.tree_id
		where r.id=$1 and j.id=$2 and j.lease_token=$3::uuid and j.attempt_no=$4
			and r.status='running' and r.output_message_id is null and coalesce(r.deadline_at,tree.absolute_deadline)>now()
			and j.status='running' and j.lease_expires_at > now()
	`, attempt.RunID, attempt.JobID, attempt.LeaseToken, attempt.AttemptNo).Scan(&selectedCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return pinnedSearchScope{}, ErrLeaseLost
	}
	if err != nil {
		return pinnedSearchScope{}, err
	}
	scope, err := s.loadPinnedEvidenceRows(ctx, tx, attempt.RunID, selectedCount)
	if err != nil {
		return pinnedSearchScope{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return pinnedSearchScope{}, err
	}
	return scope, nil
}

// loadPinnedScopeForReplay loads the same pinned Evidence scope
// loadPinnedScope does, but without its live-lease/status check — the
// caller supplies selectedSourceCount (already known from a prior
// LoadForReplay) instead of it being re-derived from a running Attempt.
// For internal/agenteval's offline decision-replay tooling only; must not
// be used by the live Controller loop.
func (s *EvidenceSearchService) loadPinnedScopeForReplay(ctx context.Context, runID string, selectedSourceCount int) (pinnedSearchScope, error) {
	tx, err := s.workerTx(ctx)
	if err != nil {
		return pinnedSearchScope{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	scope, err := s.loadPinnedEvidenceRows(ctx, tx, runID, selectedSourceCount)
	if err != nil {
		return pinnedSearchScope{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return pinnedSearchScope{}, err
	}
	return scope, nil
}

func (s *EvidenceSearchService) loadPinnedEvidenceRows(ctx context.Context, tx pgx.Tx, runID string, selectedCount int) (pinnedSearchScope, error) {
	if selectedCount < 1 {
		return pinnedSearchScope{}, fmt.Errorf("%w: Run has no selected Sources", retrieval.ErrRetrievalUnavailable)
	}
	rows, err := tx.Query(ctx, `
		select e.notebook_id, e.source_id, e.evidence_revision_id, e.index_version_id, s.title, v.config_json
		from agent_run_evidence_set e
		join source_sources s on s.id=e.source_id and s.notebook_id=e.notebook_id and s.state='ready'
		join source_evidence_revisions r on r.id=e.evidence_revision_id and r.source_id=e.source_id and r.status='active'
		join retrieval_source_index_builds b on b.revision_id=e.evidence_revision_id
			and b.source_id=e.source_id and b.notebook_id=e.notebook_id
			and b.index_version_id=e.index_version_id and b.status='verified'
		join retrieval_index_versions v on v.id=e.index_version_id and v.status in ('candidate','active','retired')
		where e.run_id=$1 order by e.ordinal
	`, runID)
	if err != nil {
		return pinnedSearchScope{}, err
	}
	defer rows.Close()
	var scope pinnedSearchScope
	for rows.Next() {
		var item pinnedEvidence
		var notebookID, versionID string
		var configJSON []byte
		if err := rows.Scan(&notebookID, &item.SourceID, &item.RevisionID, &versionID, &item.Title, &configJSON); err != nil {
			return pinnedSearchScope{}, err
		}
		if len(scope.Evidence) == 0 {
			scope.NotebookID, scope.Version.ID = notebookID, versionID
			if err := json.Unmarshal(configJSON, &scope.Version.Config); err != nil {
				return pinnedSearchScope{}, err
			}
		} else if scope.NotebookID != notebookID || scope.Version.ID != versionID {
			return pinnedSearchScope{}, fmt.Errorf("%w: inconsistent pinned scope", retrieval.ErrRetrievalUnavailable)
		}
		scope.Evidence = append(scope.Evidence, item)
	}
	if err := rows.Err(); err != nil {
		return pinnedSearchScope{}, err
	}
	if len(scope.Evidence) != selectedCount || !validSearchIndexConfig(scope.Version.Config) {
		return pinnedSearchScope{}, fmt.Errorf("%w: pinned Source authority changed", retrieval.ErrRetrievalUnavailable)
	}
	return scope, nil
}

func (s *EvidenceSearchService) reloadCandidates(ctx context.Context, scope pinnedSearchScope, ids []string) ([]retrieval.EvidenceCandidate, error) {
	wanted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	result := make([]retrieval.EvidenceCandidate, 0, len(ids))
	tx, err := s.workerTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, evidence := range scope.Evidence {
		rows, err := tx.Query(ctx, `
			select id, ordinal, kind, text_content,coordinate_json
			from source_evidence_units where revision_id=$1 and source_id=$2 and notebook_id=$3 order by ordinal
		`, evidence.RevisionID, evidence.SourceID, scope.NotebookID)
		if err != nil {
			return nil, err
		}
		units := make([]retrieval.Unit, 0)
		coordinates := make(map[string]retrieval.EvidenceCoordinate)
		for rows.Next() {
			var unit retrieval.Unit
			var coordinateJSON []byte
			if err := rows.Scan(&unit.ID, &unit.Ordinal, &unit.Kind, &unit.Text, &coordinateJSON); err != nil {
				rows.Close()
				return nil, err
			}
			units = append(units, unit)
			if len(coordinateJSON) > 0 {
				var coordinate retrieval.EvidenceCoordinate
				if json.Unmarshal(coordinateJSON, &coordinate) != nil || strings.TrimSpace(coordinate.Kind) == "" {
					rows.Close()
					return nil, fmt.Errorf("%w: invalid Evidence coordinate", retrieval.ErrRetrievalUnavailable)
				}
				coordinates[unit.ID] = coordinate
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		chunks, err := retrieval.BuildChunks(scope.Version.ID, evidence.RevisionID, units, scope.Version.Config.Chunk)
		if err != nil {
			return nil, err
		}
		for _, chunk := range chunks {
			if _, ok := wanted[chunk.ID]; !ok {
				continue
			}
			if scope.ResolvedScope != nil && scope.ResolvedScope.EntryID != "" &&
				!chunkOverlapsResolvedEntry(chunk, coordinates, *scope.ResolvedScope) {
				continue
			}
			result = append(result, retrieval.EvidenceCandidate{
				ID: chunk.ID, SourceID: evidence.SourceID, RevisionID: evidence.RevisionID,
				SourceTitle: evidence.Title, Preview: chunk.Text, UnitRefs: append([]retrieval.UnitRef(nil), chunk.UnitRefs...),
				Coordinates: evidenceCoordinatesForRefs(chunk.UnitRefs, coordinates),
			})
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func chunkOverlapsResolvedEntry(chunk retrieval.Chunk, coordinates map[string]retrieval.EvidenceCoordinate, scope EvidenceSearchResolvedScope) bool {
	for _, ref := range chunk.UnitRefs {
		coordinate, ok := coordinates[ref.UnitID]
		if ok && coordinate.Kind == "pdf_region" && coordinate.Page >= scope.PageStart && coordinate.Page <= scope.PageEnd {
			return true
		}
	}
	return false
}

func evidenceCoordinatesForRefs(refs []retrieval.UnitRef, byUnit map[string]retrieval.EvidenceCoordinate) []retrieval.EvidenceCoordinate {
	result := make([]retrieval.EvidenceCoordinate, 0)
	seen := make(map[retrieval.EvidenceCoordinate]struct{})
	for _, ref := range refs {
		coordinate, ok := byUnit[ref.UnitID]
		if !ok {
			continue
		}
		if _, duplicate := seen[coordinate]; duplicate {
			continue
		}
		seen[coordinate] = struct{}{}
		result = append(result, coordinate)
	}
	return result
}

func (s *EvidenceSearchService) workerTx(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func validSearchIndexConfig(config retrieval.IndexConfig) bool {
	return config.Chunk.MaxRunes > 0 && config.Chunk.OverlapRunes >= 0 && config.Chunk.OverlapRunes < config.Chunk.MaxRunes &&
		strings.TrimSpace(config.AnalyzerID) != "" && config.BM25K1 > 0 && config.BM25AverageDocumentLength > 0 &&
		strings.TrimSpace(config.EmbeddingModel) != "" && config.EmbeddingDimensions > 0 && retrieval.IsEmbeddingProfileID(config.EmbeddingProfileID) &&
		config.DenseCandidates > 0 && config.SparseCandidates > 0 && config.RRFK > 0 &&
		strings.TrimSpace(config.RerankerID) != "" && config.RerankCandidates > 0 &&
		config.RerankRelevanceThreshold >= 0 && !math.IsNaN(config.RerankRelevanceThreshold)
}

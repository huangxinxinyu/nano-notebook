package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/qdrantstore"
	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxSearchEvidenceCandidates = 8

type evidenceVectorSearcher interface {
	SearchDense(context.Context, []float32, qdrantstore.Scope, int) ([]retrieval.Candidate, error)
	SearchSparse(context.Context, retrieval.SparseVector, qdrantstore.Scope, int) ([]retrieval.Candidate, error)
}

type evidenceModelCapabilities interface {
	Embed(context.Context, models.EmbeddingRequest) (models.EmbeddingOutcome, error)
	Rerank(context.Context, models.RerankRequest) (models.RerankOutcome, error)
}

type EvidenceSearchService struct {
	pool    *pgxpool.Pool
	vectors evidenceVectorSearcher
	models  evidenceModelCapabilities
}

type pinnedEvidence struct {
	SourceID   string
	RevisionID string
	Title      string
}

type pinnedSearchScope struct {
	NotebookID string
	Version    retrieval.IndexVersion
	Evidence   []pinnedEvidence
}

func NewEvidenceSearchService(pool *pgxpool.Pool, vectors evidenceVectorSearcher, modelCapabilities evidenceModelCapabilities) *EvidenceSearchService {
	return &EvidenceSearchService{pool: pool, vectors: vectors, models: modelCapabilities}
}

func (s *EvidenceSearchService) SearchEvidence(ctx context.Context, attempt Attempt, query, _ string) (retrieval.SearchResult, error) {
	if s == nil || s.pool == nil || s.vectors == nil || s.models == nil || strings.TrimSpace(query) == "" {
		return retrieval.SearchResult{}, ErrSearchEvidenceUnavailable
	}
	scope, err := s.loadPinnedScope(ctx, attempt)
	if err != nil {
		return retrieval.SearchResult{}, err
	}
	embedded, err := s.models.Embed(ctx, models.EmbeddingRequest{
		Model: scope.Version.Config.EmbeddingModel, Inputs: []string{query}, Dimensions: scope.Version.Config.EmbeddingDimensions,
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
	qdrantScope := qdrantstore.Scope{NotebookID: scope.NotebookID, IndexVersionID: scope.Version.ID, Evidence: make([]qdrantstore.EvidenceRef, 0, len(scope.Evidence))}
	sourceIDs := make([]string, 0, len(scope.Evidence))
	revisionIDs := make([]string, 0, len(scope.Evidence))
	for _, item := range scope.Evidence {
		qdrantScope.Evidence = append(qdrantScope.Evidence, qdrantstore.EvidenceRef{SourceID: item.SourceID, RevisionID: item.RevisionID})
		sourceIDs = append(sourceIDs, item.SourceID)
		revisionIDs = append(revisionIDs, item.RevisionID)
	}
	rerankLimit := scope.Version.Config.RerankCandidates
	if rerankLimit > maxSearchEvidenceCandidates {
		rerankLimit = maxSearchEvidenceCandidates
	}
	pipeline := retrieval.Pipeline{
		Dense: func(ctx context.Context, _ retrieval.SearchRequest) ([]retrieval.Candidate, error) {
			return s.vectors.SearchDense(ctx, embedded.Vectors[0], qdrantScope, scope.Version.Config.DenseCandidates)
		},
		Sparse: func(ctx context.Context, _ retrieval.SearchRequest) ([]retrieval.Candidate, error) {
			return s.vectors.SearchSparse(ctx, sparse, qdrantScope, scope.Version.Config.SparseCandidates)
		},
		Reload: func(ctx context.Context, _ retrieval.Scope, ids []string) ([]retrieval.EvidenceCandidate, error) {
			return s.reloadCandidates(ctx, scope, ids)
		},
		Rerank: func(ctx context.Context, query string, candidates []retrieval.EvidenceCandidate) ([]string, error) {
			items := make([]models.RerankCandidate, 0, len(candidates))
			for _, candidate := range candidates {
				items = append(items, models.RerankCandidate{ID: candidate.ID, Text: candidate.Preview})
			}
			outcome, err := s.models.Rerank(ctx, models.RerankRequest{
				Model: scope.Version.Config.RerankerID, Query: query, Candidates: items, TopN: len(items),
			})
			return outcome.CandidateIDs, err
		},
	}
	return pipeline.Search(ctx, retrieval.SearchRequest{
		Query: query, Scope: retrieval.Scope{NotebookID: scope.NotebookID, SourceIDs: sourceIDs, RevisionIDs: revisionIDs},
		DenseLimit: scope.Version.Config.DenseCandidates, SparseLimit: scope.Version.Config.SparseCandidates,
		RerankLimit: rerankLimit, MinimumSurvivors: 1, RRFK: scope.Version.Config.RRFK,
	})
}

func (s *EvidenceSearchService) loadPinnedScope(ctx context.Context, attempt Attempt) (pinnedSearchScope, error) {
	tx, err := s.workerTx(ctx)
	if err != nil {
		return pinnedSearchScope{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var selectedCount int
	err = tx.QueryRow(ctx, `
		select r.selected_source_count
		from agent_runs r join agent_jobs j on j.run_id=r.id
		where r.id=$1 and j.id=$2 and j.lease_token=$3::uuid and j.attempt_no=$4
			and r.status='running' and r.output_message_id is null and r.deadline_at > now()
			and j.status='running' and j.lease_expires_at > now()
	`, attempt.RunID, attempt.JobID, attempt.LeaseToken, attempt.AttemptNo).Scan(&selectedCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return pinnedSearchScope{}, ErrLeaseLost
	}
	if err != nil {
		return pinnedSearchScope{}, err
	}
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
	`, attempt.RunID)
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
	if err := tx.Commit(ctx); err != nil {
		return pinnedSearchScope{}, err
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
			select id, ordinal, kind, text_content
			from source_evidence_units where revision_id=$1 and source_id=$2 and notebook_id=$3 order by ordinal
		`, evidence.RevisionID, evidence.SourceID, scope.NotebookID)
		if err != nil {
			return nil, err
		}
		units := make([]retrieval.Unit, 0)
		for rows.Next() {
			var unit retrieval.Unit
			if err := rows.Scan(&unit.ID, &unit.Ordinal, &unit.Kind, &unit.Text); err != nil {
				rows.Close()
				return nil, err
			}
			units = append(units, unit)
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
			result = append(result, retrieval.EvidenceCandidate{
				ID: chunk.ID, SourceID: evidence.SourceID, RevisionID: evidence.RevisionID,
				SourceTitle: evidence.Title, Preview: chunk.Text, UnitRefs: append([]retrieval.UnitRef(nil), chunk.UnitRefs...),
			})
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
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
		strings.TrimSpace(config.EmbeddingModel) != "" && config.EmbeddingDimensions > 0 &&
		config.DenseCandidates > 0 && config.SparseCandidates > 0 && config.RRFK > 0 &&
		strings.TrimSpace(config.RerankerID) != "" && config.RerankCandidates > 0
}

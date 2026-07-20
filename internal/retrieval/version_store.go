package retrieval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrEvalGate        = errors.New("Retrieval Index promotion requires a passing Eval Run")
	ErrVersionNotFound = errors.New("Retrieval Index Version not found")
)

type IndexConfig struct {
	Chunk                     ChunkConfig `json:"chunk"`
	AnalyzerID                string      `json:"analyzer_id"`
	BM25K1                    float64     `json:"bm25_k1"`
	BM25B                     float64     `json:"bm25_b"`
	BM25AverageDocumentLength float64     `json:"bm25_average_document_length"`
	EmbeddingModel            string      `json:"embedding_model"`
	EmbeddingDimensions       int         `json:"embedding_dimensions"`
	DenseCandidates           int         `json:"dense_candidates"`
	SparseCandidates          int         `json:"sparse_candidates"`
	RRFK                      int         `json:"rrf_k"`
	RerankerID                string      `json:"reranker_id"`
	RerankCandidates          int         `json:"rerank_candidates"`
	DegradationPolicyID       string      `json:"degradation_policy_id"`
}

type VersionStatus string

const (
	VersionCandidate VersionStatus = "candidate"
	VersionActive    VersionStatus = "active"
	VersionRetired   VersionStatus = "retired"
)

type IndexVersion struct {
	ID                  string
	Config              IndexConfig
	ConfigSHA256        string
	Status              VersionStatus
	PromotedByEvalRunID string
	CreatedAt           time.Time
	PromotedAt          *time.Time
}

type EvalStatus string

const (
	EvalPassed EvalStatus = "passed"
	EvalFailed EvalStatus = "failed"
)

type EvalRun struct {
	ID                 string
	IndexVersionID     string
	FixtureSuiteSHA256 string
	Status             EvalStatus
	MetricsJSON        []byte
}

type VersionStore struct {
	pool *pgxpool.Pool
}

func NewVersionStore(pool *pgxpool.Pool) *VersionStore {
	return &VersionStore{pool: pool}
}

func (s *VersionStore) CreateCandidate(ctx context.Context, id string, config IndexConfig) (IndexVersion, error) {
	if s == nil || s.pool == nil || strings.TrimSpace(id) == "" || !validIndexConfig(config) {
		return IndexVersion{}, errors.New("invalid Retrieval Index candidate")
	}
	configJSON, err := json.Marshal(config)
	if err != nil {
		return IndexVersion{}, err
	}
	digest := sha256.Sum256(configJSON)
	tx, err := s.workerTx(ctx)
	if err != nil {
		return IndexVersion{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var version IndexVersion
	var rawConfig []byte
	err = tx.QueryRow(ctx, `
		insert into retrieval_index_versions(id, config_json, config_sha256, status)
		values ($1, $2, $3, 'candidate')
		returning id, config_json, config_sha256, status, coalesce(promoted_by_eval_run_id,''), created_at, promoted_at
	`, id, configJSON, hex.EncodeToString(digest[:])).Scan(
		&version.ID, &rawConfig, &version.ConfigSHA256, &version.Status,
		&version.PromotedByEvalRunID, &version.CreatedAt, &version.PromotedAt,
	)
	if err != nil {
		return IndexVersion{}, err
	}
	if err := json.Unmarshal(rawConfig, &version.Config); err != nil {
		return IndexVersion{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IndexVersion{}, err
	}
	return version, nil
}

func (s *VersionStore) RecordEval(ctx context.Context, run EvalRun) error {
	if s == nil || s.pool == nil || strings.TrimSpace(run.ID) == "" || strings.TrimSpace(run.IndexVersionID) == "" ||
		len(run.FixtureSuiteSHA256) != 64 || (run.Status != EvalPassed && run.Status != EvalFailed) || !json.Valid(run.MetricsJSON) {
		return errors.New("invalid Retrieval Eval Run")
	}
	tx, err := s.workerTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		insert into retrieval_eval_runs(id, index_version_id, fixture_suite_sha256, status, metrics_json)
		values ($1, $2, $3, $4, $5)
	`, run.ID, run.IndexVersionID, run.FixtureSuiteSHA256, run.Status, run.MetricsJSON); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *VersionStore) Promote(ctx context.Context, versionID, evalRunID string) (IndexVersion, error) {
	tx, err := s.workerTx(ctx)
	if err != nil {
		return IndexVersion{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	version, err := versionByID(ctx, tx, versionID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return IndexVersion{}, ErrVersionNotFound
	}
	if err != nil {
		return IndexVersion{}, err
	}
	var passed bool
	if err := tx.QueryRow(ctx, `
		select exists(select 1 from retrieval_eval_runs where id=$1 and index_version_id=$2 and status='passed')
	`, evalRunID, versionID).Scan(&passed); err != nil {
		return IndexVersion{}, err
	}
	if !passed {
		return IndexVersion{}, ErrEvalGate
	}
	if version.Status == VersionActive && version.PromotedByEvalRunID == evalRunID {
		if err := tx.Commit(ctx); err != nil {
			return IndexVersion{}, err
		}
		return version, nil
	}
	if version.Status != VersionCandidate {
		return IndexVersion{}, ErrEvalGate
	}
	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `update retrieval_index_versions set status='retired' where status='active'`); err != nil {
		return IndexVersion{}, err
	}
	if _, err := tx.Exec(ctx, `
		update retrieval_index_versions
		set status='active', promoted_by_eval_run_id=$2, promoted_at=$3
		where id=$1
	`, versionID, evalRunID, now); err != nil {
		return IndexVersion{}, err
	}
	version, err = versionByID(ctx, tx, versionID, false)
	if err != nil {
		return IndexVersion{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IndexVersion{}, err
	}
	return version, nil
}

func (s *VersionStore) Active(ctx context.Context) (IndexVersion, error) {
	return s.one(ctx, `select id from retrieval_index_versions where status='active'`)
}

func (s *VersionStore) ByID(ctx context.Context, id string) (IndexVersion, error) {
	return s.one(ctx, `select id from retrieval_index_versions where id=$1`, id)
}

func (s *VersionStore) one(ctx context.Context, query string, args ...any) (IndexVersion, error) {
	tx, err := s.workerTx(ctx)
	if err != nil {
		return IndexVersion{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	if err := tx.QueryRow(ctx, query, args...).Scan(&id); errors.Is(err, pgx.ErrNoRows) {
		return IndexVersion{}, ErrVersionNotFound
	} else if err != nil {
		return IndexVersion{}, err
	}
	version, err := versionByID(ctx, tx, id, false)
	if err != nil {
		return IndexVersion{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IndexVersion{}, err
	}
	return version, nil
}

func (s *VersionStore) workerTx(ctx context.Context) (pgx.Tx, error) {
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

func versionByID(ctx context.Context, tx pgx.Tx, id string, lock bool) (IndexVersion, error) {
	query := `
		select id, config_json, config_sha256, status, coalesce(promoted_by_eval_run_id,''), created_at, promoted_at
		from retrieval_index_versions where id=$1`
	if lock {
		query += ` for update`
	}
	var version IndexVersion
	var rawConfig []byte
	err := tx.QueryRow(ctx, query, id).Scan(
		&version.ID, &rawConfig, &version.ConfigSHA256, &version.Status,
		&version.PromotedByEvalRunID, &version.CreatedAt, &version.PromotedAt,
	)
	if err == nil {
		err = json.Unmarshal(rawConfig, &version.Config)
	}
	return version, err
}

func validIndexConfig(config IndexConfig) bool {
	return config.Chunk.MaxRunes > 0 && config.Chunk.OverlapRunes >= 0 && config.Chunk.OverlapRunes < config.Chunk.MaxRunes &&
		strings.TrimSpace(config.AnalyzerID) != "" && config.BM25K1 > 0 && !math.IsNaN(config.BM25K1) &&
		config.BM25B >= 0 && config.BM25B <= 1 && !math.IsNaN(config.BM25B) &&
		config.BM25AverageDocumentLength > 0 && !math.IsNaN(config.BM25AverageDocumentLength) && !math.IsInf(config.BM25AverageDocumentLength, 0) &&
		strings.TrimSpace(config.EmbeddingModel) != "" && config.EmbeddingDimensions > 0 &&
		config.DenseCandidates > 0 && config.SparseCandidates > 0 && config.RRFK > 0 &&
		strings.TrimSpace(config.RerankerID) != "" && config.RerankCandidates > 0 &&
		config.RerankCandidates <= config.DenseCandidates+config.SparseCandidates &&
		strings.TrimSpace(config.DegradationPolicyID) != ""
}

package sourceprojection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

type ReindexSummary struct {
	Revisions int
	Points    int
}

type Reindexer struct {
	pool     *pgxpool.Pool
	vectors  vectorStore
	embedder embedder
}

type activeRevision struct {
	id         string
	sourceID   string
	notebookID string
	units      []retrieval.Unit
}

func NewReindexer(pool *pgxpool.Pool, vectors vectorStore, embedder embedder) *Reindexer {
	return &Reindexer{pool: pool, vectors: vectors, embedder: embedder}
}

func (r *Reindexer) ReindexVersion(ctx context.Context, versionID string) (ReindexSummary, error) {
	if r == nil || r.pool == nil || r.vectors == nil || r.embedder == nil || strings.TrimSpace(versionID) == "" {
		return ReindexSummary{}, errors.New("invalid candidate reindex")
	}
	version, err := retrieval.NewVersionStore(r.pool).ByID(ctx, versionID)
	if err != nil {
		return ReindexSummary{}, err
	}
	if version.Status != retrieval.VersionCandidate {
		return ReindexSummary{}, errors.New("candidate reindex requires a candidate Retrieval Index Version")
	}
	revisions, err := r.loadActiveRevisions(ctx)
	if err != nil {
		return ReindexSummary{}, err
	}
	summary := ReindexSummary{}
	for _, revision := range revisions {
		points, checksum, err := r.buildPoints(ctx, version, revision)
		if err != nil {
			return summary, fmt.Errorf("reindex revision %s: %w", revision.id, err)
		}
		for start := 0; start < len(points); start += 256 {
			end := start + 256
			if end > len(points) {
				end = len(points)
			}
			if err := r.vectors.Upsert(ctx, points[start:end]); err != nil {
				return summary, fmt.Errorf("reindex revision %s: %w", revision.id, err)
			}
		}
		scope := qdrantstore.Scope{
			NotebookID: revision.notebookID, IndexVersionID: version.ID,
			Evidence: []qdrantstore.EvidenceRef{{SourceID: revision.sourceID, RevisionID: revision.id}},
		}
		count, err := r.vectors.Count(ctx, scope)
		if err != nil {
			return summary, fmt.Errorf("verify revision %s: %w", revision.id, err)
		}
		if count != len(points) {
			return summary, fmt.Errorf("verify revision %s: Qdrant point count=%d, want %d", revision.id, count, len(points))
		}
		if err := r.persistVerified(ctx, version.ID, revision, len(points), checksum); err != nil {
			return summary, fmt.Errorf("verify revision %s: %w", revision.id, err)
		}
		summary.Revisions++
		summary.Points += len(points)
	}
	return summary, nil
}

func (r *Reindexer) loadActiveRevisions(ctx context.Context) ([]activeRevision, error) {
	tx, err := r.workerTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		select r.id, r.source_id, r.notebook_id
		from source_evidence_revisions r
		join source_sources s on s.id=r.source_id
		where r.status='active' and s.state='ready'
		order by r.notebook_id, r.source_id, r.id
	`)
	if err != nil {
		return nil, err
	}
	revisions := make([]activeRevision, 0)
	for rows.Next() {
		var revision activeRevision
		if err := rows.Scan(&revision.id, &revision.sourceID, &revision.notebookID); err != nil {
			rows.Close()
			return nil, err
		}
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for index := range revisions {
		unitRows, err := tx.Query(ctx, `
			select id, ordinal, kind, text_content
			from source_evidence_units where revision_id=$1 order by ordinal
		`, revisions[index].id)
		if err != nil {
			return nil, err
		}
		for unitRows.Next() {
			var unit retrieval.Unit
			if err := unitRows.Scan(&unit.ID, &unit.Ordinal, &unit.Kind, &unit.Text); err != nil {
				unitRows.Close()
				return nil, err
			}
			revisions[index].units = append(revisions[index].units, unit)
		}
		if err := unitRows.Err(); err != nil {
			unitRows.Close()
			return nil, err
		}
		unitRows.Close()
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return revisions, nil
}

func (r *Reindexer) buildPoints(ctx context.Context, version retrieval.IndexVersion, revision activeRevision) ([]qdrantstore.Point, string, error) {
	chunks, err := retrieval.BuildChunks(version.ID, revision.id, revision.units, version.Config.Chunk)
	if err != nil {
		return nil, "", err
	}
	texts := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		texts = append(texts, chunk.Text)
	}
	dense, err := embedTexts(ctx, r.embedder, version, texts)
	if err != nil {
		return nil, "", err
	}
	sparseEncoder, err := retrieval.NewSparseEncoder(
		retrieval.NewMixedAnalyzer(version.Config.AnalyzerID), version.Config.BM25K1, version.Config.BM25B,
		version.Config.BM25AverageDocumentLength,
	)
	if err != nil {
		return nil, "", err
	}
	points := make([]qdrantstore.Point, 0, len(chunks))
	for index, chunk := range chunks {
		sparse, err := sparseEncoder.Document(chunk.Text)
		if err != nil {
			return nil, "", err
		}
		unitIDs := make([]string, 0, len(chunk.UnitRefs))
		seenUnits := make(map[string]struct{}, len(chunk.UnitRefs))
		for _, ref := range chunk.UnitRefs {
			if _, seen := seenUnits[ref.UnitID]; seen {
				continue
			}
			seenUnits[ref.UnitID] = struct{}{}
			unitIDs = append(unitIDs, ref.UnitID)
		}
		point := qdrantstore.Point{
			ChunkID: chunk.ID, NotebookID: revision.notebookID, SourceID: revision.sourceID, RevisionID: revision.id,
			IndexVersionID: version.ID, UnitIDs: unitIDs, Dense: dense[index], Sparse: sparse,
		}
		point.Checksum = pointChecksum(chunk, point.Dense, point.Sparse)
		points = append(points, point)
	}
	projectionJSON, err := json.Marshal(points)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(projectionJSON)
	return points, hex.EncodeToString(digest[:]), nil
}

func (r *Reindexer) persistVerified(ctx context.Context, versionID string, revision activeRevision, expectedPoints int, checksum string) error {
	tx, err := r.workerTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('retrieval-index-promotion', 0))`); err != nil {
		return err
	}
	commandTag, err := tx.Exec(ctx, `
		insert into retrieval_source_index_builds(
			revision_id, index_version_id, source_id, notebook_id, expected_points, projection_sha256, status, verified_at
		)
		select $1,$2,$3,$4,$5,$6,'verified',now()
		where exists(select 1 from retrieval_index_versions where id=$2 and status='candidate')
			and exists(
				select 1 from source_evidence_revisions r
				join source_sources s on s.id=r.source_id and s.state='ready'
				where r.id=$1 and r.source_id=$3 and r.notebook_id=$4 and r.status='active'
			)
		on conflict (revision_id, index_version_id) do update set
			expected_points=excluded.expected_points,
			projection_sha256=excluded.projection_sha256,
			status='verified',
			verified_at=now()
		where retrieval_source_index_builds.source_id=excluded.source_id
			and retrieval_source_index_builds.notebook_id=excluded.notebook_id
	`, revision.id, versionID, revision.sourceID, revision.notebookID, expectedPoints, checksum)
	if err != nil {
		return err
	}
	if commandTag.RowsAffected() != 1 {
		return errors.New("candidate or active Evidence Revision changed during reindex")
	}
	return tx.Commit(ctx)
}

func (r *Reindexer) workerTx(ctx context.Context) (pgx.Tx, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func embedTexts(ctx context.Context, client embedder, version retrieval.IndexVersion, texts []string) ([][]float32, error) {
	vectors := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += 64 {
		end := start + 64
		if end > len(texts) {
			end = len(texts)
		}
		outcome, err := client.Embed(ctx, models.EmbeddingRequest{
			Model: version.Config.EmbeddingModel, Inputs: texts[start:end], Dimensions: version.Config.EmbeddingDimensions,
		})
		if err != nil {
			return nil, err
		}
		if len(outcome.Vectors) != end-start {
			return nil, errors.New("Embedding response count does not match Retrieval Chunks")
		}
		for _, vector := range outcome.Vectors {
			if len(vector) != version.Config.EmbeddingDimensions {
				return nil, errors.New("Embedding dimensions do not match Retrieval Index Version")
			}
			vectors = append(vectors, vector)
		}
	}
	return vectors, nil
}

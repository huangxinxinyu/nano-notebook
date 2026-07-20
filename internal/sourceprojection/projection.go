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
	"github.com/huangxinxinyu/nano-notebook/internal/sourceprocessing"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type vectorStore interface {
	Upsert(context.Context, []qdrantstore.Point) error
	Count(context.Context, qdrantstore.Scope) (int, error)
}

type embedder interface {
	Embed(context.Context, models.EmbeddingRequest) (models.EmbeddingOutcome, error)
}

type Projection struct {
	pool     *pgxpool.Pool
	vectors  vectorStore
	embedder embedder
}

type buildRecord struct {
	revisionID     string
	indexVersionID string
	sourceID       string
	notebookID     string
	expectedPoints int
	checksum       string
}

func New(pool *pgxpool.Pool, vectors vectorStore, embedder embedder) *Projection {
	return &Projection{pool: pool, vectors: vectors, embedder: embedder}
}

func (p *Projection) Build(ctx context.Context, command sourceprocessing.ProjectionCommand) error {
	if err := p.validate(command); err != nil {
		return err
	}
	version, err := retrieval.NewVersionStore(p.pool).Active(ctx)
	if err != nil {
		return err
	}
	sourceID, notebookID, units, err := p.loadUnits(ctx, command)
	if err != nil {
		return err
	}
	chunks, err := retrieval.BuildChunks(version.ID, command.RevisionID, units, version.Config.Chunk)
	if err != nil {
		return fmt.Errorf("%w: %v", sourceprocessing.ErrProjectionInvalid, err)
	}
	texts := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		texts = append(texts, chunk.Text)
	}
	dense, err := p.embed(ctx, version, texts)
	if err != nil {
		return err
	}
	sparseEncoder, err := retrieval.NewSparseEncoder(
		retrieval.NewMixedAnalyzer(version.Config.AnalyzerID), version.Config.BM25K1, version.Config.BM25B,
		version.Config.BM25AverageDocumentLength,
	)
	if err != nil {
		return fmt.Errorf("%w: %v", sourceprocessing.ErrProjectionInvalid, err)
	}
	points := make([]qdrantstore.Point, 0, len(chunks))
	for index, chunk := range chunks {
		sparse, err := sparseEncoder.Document(chunk.Text)
		if err != nil {
			return fmt.Errorf("%w: %v", sourceprocessing.ErrProjectionInvalid, err)
		}
		unitIDs := make([]string, 0, len(chunk.UnitRefs))
		seenUnits := make(map[string]struct{}, len(chunk.UnitRefs))
		for _, ref := range chunk.UnitRefs {
			if _, seen := seenUnits[ref.UnitID]; !seen {
				seenUnits[ref.UnitID] = struct{}{}
				unitIDs = append(unitIDs, ref.UnitID)
			}
		}
		point := qdrantstore.Point{
			ChunkID: chunk.ID, NotebookID: notebookID, SourceID: sourceID, RevisionID: command.RevisionID,
			IndexVersionID: version.ID, UnitIDs: unitIDs, Dense: dense[index], Sparse: sparse,
		}
		point.Checksum = pointChecksum(chunk, point.Dense, point.Sparse)
		points = append(points, point)
	}
	for start := 0; start < len(points); start += 256 {
		end := start + 256
		if end > len(points) {
			end = len(points)
		}
		if err := p.vectors.Upsert(ctx, points[start:end]); err != nil {
			return err
		}
	}
	projectionJSON, err := json.Marshal(points)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(projectionJSON)
	return p.persistBuild(ctx, command, buildRecord{
		revisionID: command.RevisionID, indexVersionID: version.ID, sourceID: sourceID, notebookID: notebookID,
		expectedPoints: len(points), checksum: hex.EncodeToString(digest[:]),
	})
}

func (p *Projection) Verify(ctx context.Context, command sourceprocessing.ProjectionCommand) error {
	if err := p.validate(command); err != nil {
		return err
	}
	record, err := p.loadBuild(ctx, command)
	if err != nil {
		return err
	}
	scope := qdrantstore.Scope{
		NotebookID: record.notebookID, IndexVersionID: record.indexVersionID,
		Evidence: []qdrantstore.EvidenceRef{{SourceID: record.sourceID, RevisionID: record.revisionID}},
	}
	count, err := p.vectors.Count(ctx, scope)
	if err != nil {
		return err
	}
	if count != record.expectedPoints {
		return fmt.Errorf("%w: Qdrant point count=%d, want %d", sourceprocessing.ErrProjectionInvalid, count, record.expectedPoints)
	}
	return p.markVerified(ctx, command, record)
}

func (p *Projection) validate(command sourceprocessing.ProjectionCommand) error {
	if p == nil || p.pool == nil || p.vectors == nil || p.embedder == nil || strings.TrimSpace(command.RevisionID) == "" ||
		strings.TrimSpace(command.Lease.ID) == "" || strings.TrimSpace(command.Lease.SourceID) == "" ||
		strings.TrimSpace(command.Lease.NotebookID) == "" || strings.TrimSpace(command.Lease.LeaseToken) == "" ||
		command.Artifact.SourceID != command.Lease.SourceID {
		return errors.New("invalid Source Projection")
	}
	return nil
}

func (p *Projection) loadUnits(ctx context.Context, command sourceprocessing.ProjectionCommand) (string, string, []retrieval.Unit, error) {
	tx, err := p.workerTx(ctx)
	if err != nil {
		return "", "", nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var sourceID, notebookID string
	err = tx.QueryRow(ctx, `
		select r.source_id, r.notebook_id
		from source_evidence_revisions r
		join source_sources s on s.id=r.source_id
		join source_processing_jobs j on j.source_id=s.id
		where r.id=$1 and r.status='building' and s.state='segmenting'
			and j.id=$2 and j.status='running' and j.lease_token=$3::uuid and j.lease_expires_at > now()
	`, command.RevisionID, command.Lease.ID, command.Lease.LeaseToken).Scan(&sourceID, &notebookID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil, sourceprocessing.ErrProjectionInvalid
	}
	if err != nil {
		return "", "", nil, err
	}
	if sourceID != command.Lease.SourceID || notebookID != command.Lease.NotebookID {
		return "", "", nil, sourceprocessing.ErrProjectionInvalid
	}
	rows, err := tx.Query(ctx, `
		select id, ordinal, kind, text_content
		from source_evidence_units where revision_id=$1 order by ordinal
	`, command.RevisionID)
	if err != nil {
		return "", "", nil, err
	}
	defer rows.Close()
	units := make([]retrieval.Unit, 0)
	for rows.Next() {
		var unit retrieval.Unit
		if err := rows.Scan(&unit.ID, &unit.Ordinal, &unit.Kind, &unit.Text); err != nil {
			return "", "", nil, err
		}
		units = append(units, unit)
	}
	if err := rows.Err(); err != nil {
		return "", "", nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", "", nil, err
	}
	return sourceID, notebookID, units, nil
}

func (p *Projection) embed(ctx context.Context, version retrieval.IndexVersion, texts []string) ([][]float32, error) {
	vectors, err := embedTexts(ctx, p.embedder, version, texts)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", sourceprocessing.ErrProjectionInvalid, err)
	}
	return vectors, nil
}

func (p *Projection) persistBuild(ctx context.Context, command sourceprocessing.ProjectionCommand, record buildRecord) error {
	tx, err := p.workerTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('retrieval-index-promotion', 0))`); err != nil {
		return err
	}
	var allowed bool
	if err := tx.QueryRow(ctx, `
		select exists(
			select 1 from source_processing_jobs j
			join source_sources s on s.id=j.source_id
			join retrieval_index_versions v on v.id=$4 and v.status='active'
			where j.id=$1 and j.lease_token=$2::uuid and j.status='running' and j.lease_expires_at > now()
				and j.source_id=$3 and s.state='segmenting'
		)
	`, command.Lease.ID, command.Lease.LeaseToken, record.sourceID, record.indexVersionID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return sourceprocessing.ErrProjectionInvalid
	}
	if _, err := tx.Exec(ctx, `
		insert into retrieval_source_index_builds(
			revision_id, index_version_id, source_id, notebook_id, expected_points, projection_sha256, status
		) values ($1,$2,$3,$4,$5,$6,'building')
		on conflict (revision_id, index_version_id) do nothing
	`, record.revisionID, record.indexVersionID, record.sourceID, record.notebookID, record.expectedPoints, record.checksum); err != nil {
		return err
	}
	var existing buildRecord
	var status string
	if err := tx.QueryRow(ctx, `
		select revision_id, index_version_id, source_id, notebook_id, expected_points, projection_sha256, status
		from retrieval_source_index_builds where revision_id=$1 and index_version_id=$2
	`, record.revisionID, record.indexVersionID).Scan(
		&existing.revisionID, &existing.indexVersionID, &existing.sourceID, &existing.notebookID,
		&existing.expectedPoints, &existing.checksum, &status,
	); err != nil {
		return err
	}
	if existing != record || (status != "building" && status != "verified") {
		return sourceprocessing.ErrProjectionInvalid
	}
	return tx.Commit(ctx)
}

func (p *Projection) loadBuild(ctx context.Context, command sourceprocessing.ProjectionCommand) (buildRecord, error) {
	tx, err := p.workerTx(ctx)
	if err != nil {
		return buildRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var record buildRecord
	var status string
	err = tx.QueryRow(ctx, `
		select b.revision_id, b.index_version_id, b.source_id, b.notebook_id, b.expected_points, b.projection_sha256, b.status
		from retrieval_source_index_builds b
		join retrieval_index_versions v on v.id=b.index_version_id and v.status='active'
		join source_processing_jobs j on j.source_id=b.source_id
		join source_sources s on s.id=b.source_id and s.state='indexing'
		where b.revision_id=$1 and j.id=$2 and j.status='running' and j.lease_token=$3::uuid and j.lease_expires_at > now()
	`, command.RevisionID, command.Lease.ID, command.Lease.LeaseToken).Scan(
		&record.revisionID, &record.indexVersionID, &record.sourceID, &record.notebookID,
		&record.expectedPoints, &record.checksum, &status,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return buildRecord{}, sourceprocessing.ErrProjectionInvalid
	}
	if err != nil {
		return buildRecord{}, err
	}
	if status != "building" && status != "verified" {
		return buildRecord{}, sourceprocessing.ErrProjectionInvalid
	}
	if err := tx.Commit(ctx); err != nil {
		return buildRecord{}, err
	}
	return record, nil
}

func (p *Projection) markVerified(ctx context.Context, command sourceprocessing.ProjectionCommand, record buildRecord) error {
	tx, err := p.workerTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('retrieval-index-promotion', 0))`); err != nil {
		return err
	}
	commandTag, err := tx.Exec(ctx, `
		update retrieval_source_index_builds b
		set status='verified', verified_at=coalesce(verified_at, now())
		where b.revision_id=$1 and b.index_version_id=$2
			and exists(select 1 from retrieval_index_versions v where v.id=b.index_version_id and v.status='active')
			and exists(
				select 1 from source_processing_jobs j join source_sources s on s.id=j.source_id
				where j.id=$3 and j.source_id=b.source_id and j.status='running' and j.lease_token=$4::uuid
					and j.lease_expires_at > now() and s.state='indexing'
			)
	`, record.revisionID, record.indexVersionID, command.Lease.ID, command.Lease.LeaseToken)
	if err != nil {
		return err
	}
	if commandTag.RowsAffected() != 1 {
		return sourceprocessing.ErrProjectionInvalid
	}
	return tx.Commit(ctx)
}

func (p *Projection) workerTx(ctx context.Context) (pgx.Tx, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func pointChecksum(chunk retrieval.Chunk, dense []float32, sparse retrieval.SparseVector) string {
	payload, _ := json.Marshal(struct {
		Chunk  retrieval.Chunk        `json:"chunk"`
		Dense  []float32              `json:"dense"`
		Sparse retrieval.SparseVector `json:"sparse"`
	}{chunk, dense, sparse})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

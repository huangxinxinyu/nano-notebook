package evidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"strings"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/normalize"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrLeaseLost           = errors.New("Evidence publication lease lost")
	ErrPublicationConflict = errors.New("Evidence publication conflict")
)

type RevisionStatus string

const (
	RevisionBuilding RevisionStatus = "building"
	RevisionActive   RevisionStatus = "active"
)

type Revision struct {
	ID                 string
	SourceID           string
	NotebookID         string
	RevisionNo         int
	ExtractionConfigID string
	ArtifactObjectKey  string
	ArtifactSHA256     string
	Status             RevisionStatus
	CreatedAt          time.Time
}

type PublishCommand struct {
	RevisionID      string
	JobID           string
	LeaseToken      string
	Artifact        normalize.Artifact
	ViewerArtifacts []ViewerArtifact
}

type ViewerArtifact struct {
	Ordinal        int
	Width          int
	Height         int
	MediaType      string
	Bytes          int64
	SHA256         string
	Filename       string
	RenderConfigID string
	Payload        []byte
}

type objectWriter interface {
	Put(context.Context, string, []byte) error
}

type Publisher struct {
	pool    *pgxpool.Pool
	objects objectWriter
}

func NewPublisher(pool *pgxpool.Pool, objects objectWriter) *Publisher {
	return &Publisher{pool: pool, objects: objects}
}

func (p *Publisher) Publish(ctx context.Context, command PublishCommand) (Revision, bool, error) {
	if p == nil || p.pool == nil || p.objects == nil || command.RevisionID == "" || command.JobID == "" || command.LeaseToken == "" {
		return Revision{}, false, errors.New("invalid Evidence Publisher")
	}
	if err := normalize.Validate(command.Artifact); err != nil {
		return Revision{}, false, err
	}
	if err := validateViewerArtifacts(command.Artifact, command.ViewerArtifacts); err != nil {
		return Revision{}, false, err
	}
	if err := p.authorizeLease(ctx, command.JobID, command.LeaseToken, command.Artifact.SourceID); err != nil {
		return Revision{}, false, err
	}
	artifactKey := "sources/" + command.Artifact.SourceID + "/evidence/" + command.RevisionID + "/normalized.json"
	if err := p.objects.Put(ctx, artifactKey, command.Artifact.CanonicalJSON); err != nil {
		return Revision{}, false, err
	}
	for _, viewer := range command.ViewerArtifacts {
		objectKey := viewerArtifactKey(command.Artifact.SourceID, command.RevisionID, viewer.Filename)
		if err := p.objects.Put(ctx, objectKey, viewer.Payload); err != nil {
			return Revision{}, false, err
		}
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return Revision{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return Revision{}, false, err
	}
	var sourceID, notebookID string
	var sourceState string
	err = tx.QueryRow(ctx, `
		select j.source_id, j.notebook_id, s.state
		from source_processing_jobs j join source_sources s on s.id=j.source_id
		where j.id=$1 and j.status='running' and j.lease_token=$2::uuid and j.lease_expires_at > now()
		for update of j, s
	`, command.JobID, command.LeaseToken).Scan(&sourceID, &notebookID, &sourceState)
	if errors.Is(err, pgx.ErrNoRows) {
		return Revision{}, false, ErrLeaseLost
	}
	if err != nil {
		return Revision{}, false, err
	}
	if sourceID != command.Artifact.SourceID {
		return Revision{}, false, ErrLeaseLost
	}
	existing, err := revisionByID(ctx, tx, command.RevisionID)
	if err == nil {
		if existing.SourceID != sourceID || existing.ArtifactSHA256 != command.Artifact.SHA256 ||
			existing.ExtractionConfigID != command.Artifact.ExtractionConfigID {
			return Revision{}, false, ErrPublicationConflict
		}
		if err := existingViewerArtifactsMatch(ctx, tx, existing, command.ViewerArtifacts); err != nil {
			return Revision{}, false, err
		}
		if sourceState == "normalizing" && existing.Status == RevisionBuilding {
			if _, err := tx.Exec(ctx, `update source_sources set state='segmenting', updated_at=now() where id=$1`, sourceID); err != nil {
				return Revision{}, false, err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return Revision{}, false, err
		}
		return existing, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Revision{}, false, err
	}
	if sourceState != "normalizing" {
		return Revision{}, false, ErrPublicationConflict
	}
	var revisionNo int
	if err := tx.QueryRow(ctx, `select coalesce(max(revision_no), 0)+1 from source_evidence_revisions where source_id=$1`, sourceID).Scan(&revisionNo); err != nil {
		return Revision{}, false, err
	}
	var created Revision
	err = tx.QueryRow(ctx, `
		insert into source_evidence_revisions(
			id, source_id, notebook_id, revision_no, extraction_config_id,
			artifact_schema_version, artifact_object_key, artifact_sha256, status
		) values ($1, $2, $3, $4, $5, $6, $7, $8, 'building')
		returning id, source_id, notebook_id, revision_no, extraction_config_id,
			artifact_object_key, artifact_sha256, status, created_at
	`, command.RevisionID, sourceID, notebookID, revisionNo, command.Artifact.ExtractionConfigID,
		command.Artifact.SchemaVersion, artifactKey, command.Artifact.SHA256).Scan(
		&created.ID, &created.SourceID, &created.NotebookID, &created.RevisionNo,
		&created.ExtractionConfigID, &created.ArtifactObjectKey, &created.ArtifactSHA256,
		&created.Status, &created.CreatedAt,
	)
	if err != nil {
		return Revision{}, false, err
	}
	if _, err := tx.Exec(ctx, `
		insert into source_evidence_coverage(revision_id, status, total_runes) values ($1, $2, $3)
	`, created.ID, command.Artifact.Coverage.Status, command.Artifact.Coverage.TotalRunes); err != nil {
		return Revision{}, false, err
	}
	for ordinal, gap := range command.Artifact.Coverage.Gaps {
		var startRune, endRune, coordinateJSON any
		if gap.StartRune > 0 || gap.EndRune > 0 {
			startRune, endRune = gap.StartRune, gap.EndRune
		}
		if gap.Coordinate != nil {
			coordinateJSON, err = json.Marshal(gap.Coordinate)
			if err != nil {
				return Revision{}, false, err
			}
		}
		if _, err := tx.Exec(ctx, `
			insert into source_evidence_coverage_gaps(
				revision_id, ordinal, start_rune, end_rune, reason, impact, coordinate_json
			) values ($1, $2, $3, $4, $5, $6, $7)
		`, created.ID, ordinal, startRune, endRune, gap.Reason, gap.Impact, coordinateJSON); err != nil {
			return Revision{}, false, err
		}
	}
	for _, block := range command.Artifact.Blocks {
		unitID := stableUnitID(created.ID, block.ID)
		var headingLevel any
		if block.HeadingLevel > 0 {
			headingLevel = block.HeadingLevel
		}
		var coordinateJSON any
		if block.Coordinate != nil {
			coordinateJSON, err = json.Marshal(block.Coordinate)
			if err != nil {
				return Revision{}, false, err
			}
		}
		if _, err := tx.Exec(ctx, `
			insert into source_evidence_units(
				id, revision_id, source_id, notebook_id, ordinal, kind, text_content,
				start_rune, end_rune, heading_level, coordinate_json
			) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		`, unitID, created.ID, sourceID, notebookID, block.Ordinal, block.Kind, block.Text,
			block.StartRune, block.EndRune, headingLevel, coordinateJSON); err != nil {
			return Revision{}, false, err
		}
	}
	for _, viewer := range command.ViewerArtifacts {
		if _, err := tx.Exec(ctx, `
			insert into source_viewer_artifacts(
				revision_id,source_id,notebook_id,ordinal,width,height,media_type,byte_size,
				content_sha256,filename,object_key,render_config_id
			) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		`, created.ID, sourceID, notebookID, viewer.Ordinal, viewer.Width, viewer.Height, viewer.MediaType, viewer.Bytes,
			viewer.SHA256, viewer.Filename, viewerArtifactKey(sourceID, created.ID, viewer.Filename), viewer.RenderConfigID); err != nil {
			return Revision{}, false, err
		}
	}
	if _, err := tx.Exec(ctx, `update source_sources set state='segmenting', updated_at=now() where id=$1`, sourceID); err != nil {
		return Revision{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Revision{}, false, err
	}
	return created, false, nil
}

func validateViewerArtifacts(artifact normalize.Artifact, viewers []ViewerArtifact) error {
	if artifact.Format != "pdf" && artifact.Format != "pptx" {
		if len(viewers) != 0 {
			return errors.New("Viewer artifacts are unsupported for normalized Source format")
		}
		return nil
	}
	if len(viewers) < 1 || len(viewers) > 500 {
		return errors.New("PDF and PPTX evidence requires bounded Viewer artifacts")
	}
	prefix := "page"
	coordinateKind := "pdf_region"
	if artifact.Format == "pptx" {
		prefix = "slide"
		coordinateKind = "slide_region"
	}
	maxCoordinate := 0
	for _, block := range artifact.Blocks {
		if block.Coordinate == nil || block.Coordinate.Kind != coordinateKind {
			continue
		}
		value := block.Coordinate.Page
		if artifact.Format == "pptx" {
			value = block.Coordinate.Slide
		}
		if value > maxCoordinate {
			maxCoordinate = value
		}
	}
	if maxCoordinate > len(viewers) {
		return errors.New("Viewer artifacts do not cover normalized coordinates")
	}
	var totalBytes int64
	for index, viewer := range viewers {
		expectedFilename := fmt.Sprintf("%s-%06d.png", prefix, index+1)
		configuration, err := png.DecodeConfig(bytes.NewReader(viewer.Payload))
		digest := sha256.Sum256(viewer.Payload)
		if viewer.Ordinal != index+1 || viewer.Width < 1 || viewer.Height < 1 || int64(viewer.Width) > 100_000_000/int64(viewer.Height) ||
			viewer.MediaType != "image/png" || viewer.Bytes != int64(len(viewer.Payload)) || viewer.Bytes < 1 ||
			viewer.SHA256 != hex.EncodeToString(digest[:]) || viewer.Filename != expectedFilename ||
			strings.TrimSpace(viewer.RenderConfigID) == "" || len(viewer.RenderConfigID) > 255 || err != nil ||
			configuration.Width != viewer.Width || configuration.Height != viewer.Height || totalBytes > 256*1024*1024-viewer.Bytes {
			return errors.New("invalid Viewer artifact")
		}
		totalBytes += viewer.Bytes
	}
	return nil
}

func viewerArtifactKey(sourceID, revisionID, filename string) string {
	return "sources/" + sourceID + "/evidence/" + revisionID + "/viewer/" + filename
}

func existingViewerArtifactsMatch(ctx context.Context, tx pgx.Tx, revision Revision, viewers []ViewerArtifact) error {
	rows, err := tx.Query(ctx, `
		select ordinal,width,height,media_type,byte_size,content_sha256,filename,object_key,render_config_id
		from source_viewer_artifacts where revision_id=$1 order by ordinal
	`, revision.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		if index >= len(viewers) {
			return ErrPublicationConflict
		}
		var ordinal, width, height int
		var mediaType, sha, filename, objectKey, configID string
		var byteSize int64
		if err := rows.Scan(&ordinal, &width, &height, &mediaType, &byteSize, &sha, &filename, &objectKey, &configID); err != nil {
			return err
		}
		viewer := viewers[index]
		if ordinal != viewer.Ordinal || width != viewer.Width || height != viewer.Height || mediaType != viewer.MediaType ||
			byteSize != viewer.Bytes || sha != viewer.SHA256 || filename != viewer.Filename || configID != viewer.RenderConfigID ||
			objectKey != viewerArtifactKey(revision.SourceID, revision.ID, viewer.Filename) {
			return ErrPublicationConflict
		}
		index++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if index != len(viewers) {
		return ErrPublicationConflict
	}
	return nil
}

func (p *Publisher) authorizeLease(ctx context.Context, jobID, leaseToken, sourceID string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return err
	}
	var allowed bool
	if err := tx.QueryRow(ctx, `
		select exists(
			select 1 from source_processing_jobs
			where id=$1 and source_id=$2 and status='running' and lease_token=$3::uuid and lease_expires_at > now()
		)
	`, jobID, sourceID, leaseToken).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return ErrLeaseLost
	}
	return tx.Commit(ctx)
}

func revisionByID(ctx context.Context, tx pgx.Tx, id string) (Revision, error) {
	var revision Revision
	err := tx.QueryRow(ctx, `
		select id, source_id, notebook_id, revision_no, extraction_config_id,
			artifact_object_key, artifact_sha256, status, created_at
		from source_evidence_revisions where id=$1
	`, id).Scan(
		&revision.ID, &revision.SourceID, &revision.NotebookID, &revision.RevisionNo,
		&revision.ExtractionConfigID, &revision.ArtifactObjectKey, &revision.ArtifactSHA256,
		&revision.Status, &revision.CreatedAt,
	)
	return revision, err
}

func stableUnitID(revisionID, blockID string) string {
	digest := sha256.Sum256([]byte(revisionID + "\x00" + blockID))
	return fmt.Sprintf("unit_%s", hex.EncodeToString(digest[:16]))
}

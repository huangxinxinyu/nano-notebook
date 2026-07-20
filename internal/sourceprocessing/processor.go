package sourceprocessing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/huangxinxinyu/nano-notebook/internal/evidence"
	"github.com/huangxinxinyu/nano-notebook/internal/normalize"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/source"
	"github.com/huangxinxinyu/nano-notebook/internal/sourcejobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrProjectionInvalid = errors.New("Source projection verification failed")

type Config struct {
	ExtractionConfigID string
	MaxSourceBytes     int64
	MaxNormalizedRunes int
}

type ProjectionCommand struct {
	Lease      sourcejobs.Lease
	RevisionID string
	Artifact   normalize.Artifact
}

type Projection interface {
	Build(context.Context, ProjectionCommand) error
	Verify(context.Context, ProjectionCommand) error
}

type queue interface {
	Advance(context.Context, string, string, source.State, source.State) error
	CompleteEvidence(context.Context, string, string, string) error
	Fail(context.Context, string, string, string) error
}

type publisher interface {
	Publish(context.Context, evidence.PublishCommand) (evidence.Revision, bool, error)
}

type objectReader interface {
	Get(context.Context, string, int64) ([]byte, error)
}

type Processor struct {
	pool       *pgxpool.Pool
	queue      queue
	publisher  publisher
	objects    objectReader
	projection Projection
	config     Config
}

func NewProcessor(pool *pgxpool.Pool, queue queue, publisher publisher, objects objectReader, projection Projection, config Config) *Processor {
	return &Processor{pool: pool, queue: queue, publisher: publisher, objects: objects, projection: projection, config: config}
}

func (p *Processor) ProcessLease(ctx context.Context, lease sourcejobs.Lease) error {
	if err := p.validate(lease); err != nil {
		return err
	}
	item, err := p.loadSource(ctx, lease)
	if err != nil {
		return err
	}
	payload, err := p.objects.Get(ctx, item.OriginalObjectKey, p.config.MaxSourceBytes)
	if errors.Is(err, objectstore.ErrObjectTooLarge) {
		return p.fail(ctx, lease, "processing_budget_exceeded")
	}
	if errors.Is(err, objectstore.ErrNotFound) {
		return p.fail(ctx, lease, "source_object_missing")
	}
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	if int64(len(payload)) != item.ByteSize || hex.EncodeToString(digest[:]) != item.ContentSHA256 {
		return p.fail(ctx, lease, "source_integrity_mismatch")
	}

	if item.State == source.StateUploaded {
		if err := p.queue.Advance(ctx, lease.ID, lease.LeaseToken, source.StateUploaded, source.StateValidating); err != nil {
			return err
		}
		item.State = source.StateValidating
	}
	if item.State == source.StateValidating {
		if err := p.queue.Advance(ctx, lease.ID, lease.LeaseToken, source.StateValidating, source.StateNormalizing); err != nil {
			return err
		}
		item.State = source.StateNormalizing
	}

	artifact, err := p.normalize(item, payload)
	if err != nil {
		return p.fail(ctx, lease, "extraction_invalid")
	}
	if artifact.Coverage.TotalRunes > p.config.MaxNormalizedRunes {
		return p.fail(ctx, lease, "processing_budget_exceeded")
	}
	revisionID := stableRevisionID(item.ID, p.config.ExtractionConfigID)
	if item.State == source.StateNormalizing {
		if _, _, err := p.publisher.Publish(ctx, evidence.PublishCommand{
			RevisionID: revisionID, JobID: lease.ID, LeaseToken: lease.LeaseToken, Artifact: artifact,
		}); err != nil {
			return err
		}
		item.State = source.StateSegmenting
	}
	command := ProjectionCommand{Lease: lease, RevisionID: revisionID, Artifact: artifact}
	if item.State == source.StateSegmenting {
		if err := p.projection.Build(ctx, command); err != nil {
			if errors.Is(err, ErrProjectionInvalid) {
				return p.fail(ctx, lease, "projection_invalid")
			}
			return err
		}
		if err := p.queue.Advance(ctx, lease.ID, lease.LeaseToken, source.StateSegmenting, source.StateIndexing); err != nil {
			return err
		}
		item.State = source.StateIndexing
	}
	if item.State == source.StateIndexing {
		if err := p.projection.Verify(ctx, command); err != nil {
			if errors.Is(err, ErrProjectionInvalid) {
				return p.fail(ctx, lease, "projection_invalid")
			}
			return err
		}
		if err := p.queue.Advance(ctx, lease.ID, lease.LeaseToken, source.StateIndexing, source.StateVerifying); err != nil {
			return err
		}
		item.State = source.StateVerifying
	}
	if item.State != source.StateVerifying {
		return fmt.Errorf("unsupported resumable Source state %q", item.State)
	}
	return p.queue.CompleteEvidence(ctx, lease.ID, lease.LeaseToken, revisionID)
}

func (p *Processor) validate(lease sourcejobs.Lease) error {
	if p == nil || p.pool == nil || p.queue == nil || p.publisher == nil || p.objects == nil || p.projection == nil ||
		strings.TrimSpace(p.config.ExtractionConfigID) == "" || p.config.MaxSourceBytes <= 0 || p.config.MaxNormalizedRunes <= 0 ||
		strings.TrimSpace(lease.ID) == "" || strings.TrimSpace(lease.SourceID) == "" || strings.TrimSpace(lease.NotebookID) == "" || strings.TrimSpace(lease.LeaseToken) == "" {
		return errors.New("invalid Source Processor")
	}
	return nil
}

func (p *Processor) loadSource(ctx context.Context, lease sourcejobs.Lease) (source.Source, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return source.Source{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return source.Source{}, err
	}
	var item source.Source
	err = tx.QueryRow(ctx, `
		select s.id, s.notebook_id, s.title, s.format, s.media_type, s.byte_size,
			s.content_sha256, s.original_object_key, s.state, s.created_at, s.updated_at
		from source_sources s join source_processing_jobs j on j.source_id=s.id
		where s.id=$1 and s.notebook_id=$2 and j.id=$3 and j.status='running'
			and j.lease_token=$4::uuid and j.lease_expires_at > now()
	`, lease.SourceID, lease.NotebookID, lease.ID, lease.LeaseToken).Scan(
		&item.ID, &item.NotebookID, &item.Title, &item.Format, &item.MediaType, &item.ByteSize,
		&item.ContentSHA256, &item.OriginalObjectKey, &item.State, &item.CreatedAt, &item.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return source.Source{}, sourcejobs.ErrLeaseLost
	}
	if err != nil {
		return source.Source{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return source.Source{}, err
	}
	return item, nil
}

func (p *Processor) normalize(item source.Source, payload []byte) (normalize.Artifact, error) {
	input := normalize.Input{
		SourceID: item.ID, ExtractionConfigID: p.config.ExtractionConfigID,
		Format: string(item.Format), Payload: payload,
	}
	switch item.Format {
	case source.FormatTXT, source.FormatMarkdown:
		return normalize.Text(input)
	case source.FormatPDF:
		return normalize.PDF(input)
	default:
		return normalize.Artifact{}, errors.New("Extractor Adapter is not configured for Source format")
	}
}

func (p *Processor) fail(ctx context.Context, lease sourcejobs.Lease, code string) error {
	return p.queue.Fail(ctx, lease.ID, lease.LeaseToken, code)
}

func stableRevisionID(sourceID, extractionConfigID string) string {
	digest := sha256.Sum256([]byte(sourceID + "\x00" + extractionConfigID))
	return "evr_" + hex.EncodeToString(digest[:16])
}

package sourceprocessing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/huangxinxinyu/nano-notebook/internal/documentrender"
	"github.com/huangxinxinyu/nano-notebook/internal/evidence"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/normalize"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/source"
	"github.com/huangxinxinyu/nano-notebook/internal/sourcejobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrProjectionInvalid    = errors.New("Source projection verification failed")
	ErrRetrievalUnavailable = errors.New("Source retrieval authority is unavailable")
)

type Config struct {
	ExtractionConfigID     string
	ExtractorAdapterID     string
	MaxSourceBytes         int64
	MaxNormalizedRunes     int
	RenderConfigID         string
	RenderMaxPages         int
	RenderDPI              int
	RenderMaxPixelsPerPage int64
	RenderMaxOutputBytes   int64
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

type Extractor interface {
	Extract(context.Context, source.Source, []byte, string) (normalize.Artifact, error)
}

type RenderedExtractor interface {
	ExtractRendered(context.Context, source.Source, []byte, string, documentrender.Result) (normalize.Artifact, error)
}

type MediaModels interface {
	Transcribe(context.Context, models.TranscriptionRequest) (models.TranscriptionOutcome, error)
	DescribeImage(context.Context, models.VisionRequest) (models.VisionOutcome, error)
}

type NativeExtractorConfig struct {
	VisionModel         string
	TranscriptionModel  string
	VisionPromptVersion string
	MaxVisionPages      int
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
	extractor  Extractor
	renderer   documentrender.Adapter
	traceSink  TraceSink
	config     Config
}

func NewProcessor(pool *pgxpool.Pool, queue queue, publisher publisher, objects objectReader, projection Projection, config Config) *Processor {
	if strings.TrimSpace(config.ExtractorAdapterID) == "" {
		config.ExtractorAdapterID = "native-in-process"
	}
	return NewProcessorWithExtractor(pool, queue, publisher, objects, projection, NewNativeExtractor(nil, NativeExtractorConfig{}), config)
}

func NewProcessorWithExtractor(pool *pgxpool.Pool, queue queue, publisher publisher, objects objectReader, projection Projection, extractor Extractor, config Config) *Processor {
	return NewProcessorWithExtractorAndTrace(pool, queue, publisher, objects, projection, extractor, nil, config)
}

func NewProcessorWithExtractorAndTrace(pool *pgxpool.Pool, queue queue, publisher publisher, objects objectReader, projection Projection, extractor Extractor, traceSink TraceSink, config Config) *Processor {
	return NewProcessorWithExtractorTraceAndRenderer(pool, queue, publisher, objects, projection, extractor, nil, traceSink, config)
}

func NewProcessorWithExtractorTraceAndRenderer(pool *pgxpool.Pool, queue queue, publisher publisher, objects objectReader, projection Projection, extractor Extractor, renderer documentrender.Adapter, traceSink TraceSink, config Config) *Processor {
	return &Processor{pool: pool, queue: queue, publisher: publisher, objects: objects, projection: projection, extractor: extractor, renderer: renderer, traceSink: traceSink, config: config}
}

func (p *Processor) ProcessLease(ctx context.Context, lease sourcejobs.Lease) (resultErr error) {
	trace := newSourceProcessingTrace(p.traceSink, lease, p.config)
	defer func() { trace.finish(ctx, resultErr) }()
	if err := p.validate(lease); err != nil {
		return err
	}
	item, err := p.loadSource(ctx, lease)
	if err != nil {
		return err
	}
	trace.setFormat(string(item.Format))
	payload, err := p.objects.Get(ctx, item.OriginalObjectKey, p.config.MaxSourceBytes)
	if errors.Is(err, objectstore.ErrObjectTooLarge) {
		return p.failTraced(ctx, lease, trace, "processing_budget_exceeded")
	}
	if errors.Is(err, objectstore.ErrNotFound) {
		return p.failTraced(ctx, lease, trace, "source_object_missing")
	}
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	if int64(len(payload)) != item.ByteSize || hex.EncodeToString(digest[:]) != item.ContentSHA256 {
		return p.failTraced(ctx, lease, trace, "source_integrity_mismatch")
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

	trace.moveStage("source.normalizing")
	rendered, viewerArtifacts, err := p.renderViewerArtifacts(ctx, item, payload)
	if err != nil {
		return p.failTraced(ctx, lease, trace, "extraction_invalid")
	}
	var artifact normalize.Artifact
	if extractor, ok := p.extractor.(RenderedExtractor); ok && (item.Format == source.FormatPDF || item.Format == source.FormatPPTX) {
		artifact, err = extractor.ExtractRendered(ctx, item, payload, p.config.ExtractionConfigID, rendered)
	} else {
		artifact, err = p.extractor.Extract(ctx, item, payload, p.config.ExtractionConfigID)
	}
	if errors.Is(err, normalize.ErrProcessingBudget) {
		return p.failTraced(ctx, lease, trace, "processing_budget_exceeded")
	}
	if err != nil || normalize.Validate(artifact) != nil {
		return p.failTraced(ctx, lease, trace, "extraction_invalid")
	}
	trace.setCoverage(artifact)
	if artifact.Coverage.TotalRunes > p.config.MaxNormalizedRunes {
		return p.failTraced(ctx, lease, trace, "processing_budget_exceeded")
	}
	revisionID := stableRevisionID(item.ID, p.config.ExtractionConfigID)
	trace.moveStage("source.segmenting")
	if item.State == source.StateNormalizing {
		if _, _, err := p.publisher.Publish(ctx, evidence.PublishCommand{
			RevisionID: revisionID, JobID: lease.ID, LeaseToken: lease.LeaseToken, Artifact: artifact, ViewerArtifacts: viewerArtifacts,
		}); err != nil {
			return err
		}
		item.State = source.StateSegmenting
	}
	command := ProjectionCommand{Lease: lease, RevisionID: revisionID, Artifact: artifact}
	trace.moveStage("source.indexing")
	if item.State == source.StateSegmenting {
		if err := p.projection.Build(ctx, command); err != nil {
			if errors.Is(err, ErrRetrievalUnavailable) {
				return p.failTraced(ctx, lease, trace, "retrieval_unavailable")
			}
			if errors.Is(err, ErrProjectionInvalid) {
				return p.failTraced(ctx, lease, trace, "projection_invalid")
			}
			return err
		}
		if err := p.queue.Advance(ctx, lease.ID, lease.LeaseToken, source.StateSegmenting, source.StateIndexing); err != nil {
			return err
		}
		item.State = source.StateIndexing
	}
	trace.moveStage("source.verifying")
	if item.State == source.StateIndexing {
		if err := p.projection.Verify(ctx, command); err != nil {
			if errors.Is(err, ErrProjectionInvalid) {
				return p.failTraced(ctx, lease, trace, "projection_invalid")
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

func (p *Processor) renderViewerArtifacts(ctx context.Context, item source.Source, payload []byte) (documentrender.Result, []evidence.ViewerArtifact, error) {
	if item.Format != source.FormatPDF && item.Format != source.FormatPPTX {
		return documentrender.Result{}, nil, nil
	}
	if p.renderer == nil || strings.TrimSpace(p.config.RenderConfigID) == "" || p.config.RenderMaxPages < 1 || p.config.RenderDPI < 72 ||
		p.config.RenderMaxPixelsPerPage < 1 || p.config.RenderMaxOutputBytes < 1 {
		return documentrender.Result{}, nil, errors.New("document renderer is not configured")
	}
	format := documentrender.FormatPDF
	if item.Format == source.FormatPPTX {
		format = documentrender.FormatPPTX
	}
	result, err := p.renderer.Render(ctx, documentrender.Request{
		SchemaVersion: 1, SourceID: item.ID, Format: format, InputSHA256: item.ContentSHA256, InputBytes: item.ByteSize,
		RenderConfigID: p.config.RenderConfigID, MaxPages: p.config.RenderMaxPages, DPI: p.config.RenderDPI,
		MaxPixelsPerPage: p.config.RenderMaxPixelsPerPage, MaxOutputBytes: p.config.RenderMaxOutputBytes,
	}, payload)
	if err != nil {
		return documentrender.Result{}, nil, err
	}
	viewers := make([]evidence.ViewerArtifact, 0, len(result.Assets))
	for _, asset := range result.Assets {
		viewers = append(viewers, evidence.ViewerArtifact{
			Ordinal: asset.Page.Ordinal, Width: asset.Page.Width, Height: asset.Page.Height, MediaType: asset.Page.MediaType,
			Bytes: asset.Page.Bytes, SHA256: asset.Page.SHA256, Filename: asset.Page.Filename,
			RenderConfigID: result.Manifest.RenderConfigID, Payload: asset.Payload,
		})
	}
	return result, viewers, nil
}

func (p *Processor) validate(lease sourcejobs.Lease) error {
	if p == nil || p.pool == nil || p.queue == nil || p.publisher == nil || p.objects == nil || p.projection == nil ||
		p.extractor == nil ||
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

type NativeExtractor struct {
	media  MediaModels
	config NativeExtractorConfig
}

func NewNativeExtractor(media MediaModels, config NativeExtractorConfig) *NativeExtractor {
	config.VisionModel = strings.TrimSpace(config.VisionModel)
	config.TranscriptionModel = strings.TrimSpace(config.TranscriptionModel)
	config.VisionPromptVersion = strings.TrimSpace(config.VisionPromptVersion)
	if config.MaxVisionPages == 0 {
		config.MaxVisionPages = 20
	}
	return &NativeExtractor{media: media, config: config}
}

func (e *NativeExtractor) ExtractRendered(ctx context.Context, item source.Source, payload []byte, extractionConfigID string, rendered documentrender.Result) (normalize.Artifact, error) {
	if item.Format != source.FormatPDF && item.Format != source.FormatPPTX {
		return e.Extract(ctx, item, payload, extractionConfigID)
	}
	var missing []int
	var err error
	if item.Format == source.FormatPDF {
		missing, err = normalize.PDFPagesRequiringVision(payload)
	} else {
		missing, err = normalize.PPTXSlidesRequiringVision(payload)
	}
	if err != nil {
		return normalize.Artifact{}, err
	}
	if len(missing) == 0 {
		return e.Extract(ctx, item, payload, extractionConfigID)
	}
	if e == nil || e.media == nil || e.config.VisionModel == "" || e.config.VisionPromptVersion == "" ||
		e.config.MaxVisionPages < 1 || len(missing) > e.config.MaxVisionPages {
		return normalize.Artifact{}, normalize.ErrProcessingBudget
	}
	assets := make(map[int]documentrender.Asset, len(rendered.Assets))
	for _, asset := range rendered.Assets {
		assets[asset.Page.Ordinal] = asset
	}
	visualPages := make([]normalize.VisualPage, 0, len(missing))
	for _, ordinal := range missing {
		asset, ok := assets[ordinal]
		if !ok {
			return normalize.Artifact{}, errors.New("rendered PDF page is missing")
		}
		outcome, err := e.media.DescribeImage(ctx, models.VisionRequest{
			Model: e.config.VisionModel, MediaType: "image/png", Image: asset.Payload,
			Width: asset.Page.Width, Height: asset.Page.Height, PromptVersion: e.config.VisionPromptVersion,
		})
		if err != nil {
			return normalize.Artifact{}, err
		}
		regions := make([]normalize.ImageRegion, 0, len(outcome.Regions))
		for _, region := range outcome.Regions {
			regions = append(regions, normalize.ImageRegion{Text: region.Text, X: region.X, Y: region.Y, Width: region.Width, Height: region.Height})
		}
		visualPages = append(visualPages, normalize.VisualPage{
			Ordinal: ordinal, Width: asset.Page.Width, Height: asset.Page.Height, Regions: regions,
		})
	}
	input := normalize.Input{
		SourceID: item.ID, ExtractionConfigID: extractionConfigID, Format: string(item.Format), Payload: payload,
	}
	if item.Format == source.FormatPPTX {
		return normalize.PPTXWithVisualSlides(input, visualPages)
	}
	return normalize.PDFWithVisualPages(input, visualPages)
}

func (e *NativeExtractor) Extract(ctx context.Context, item source.Source, payload []byte, extractionConfigID string) (normalize.Artifact, error) {
	input := normalize.Input{
		SourceID: item.ID, ExtractionConfigID: extractionConfigID,
		Format: string(item.Format), Payload: payload,
	}
	switch item.Format {
	case source.FormatTXT, source.FormatMarkdown:
		return normalize.Text(input)
	case source.FormatPDF:
		return normalize.PDF(input)
	case source.FormatDOCX, source.FormatPPTX:
		return normalize.OOXML(input)
	case source.FormatHTML:
		return normalize.HTML(input)
	case source.FormatPNG, source.FormatJPEG, source.FormatWebP:
		if e == nil || e.media == nil || e.config.VisionModel == "" || e.config.VisionPromptVersion == "" {
			return normalize.Artifact{}, errors.New("vision Extractor Adapter is not configured")
		}
		width, height, err := normalize.ImageDimensions(string(item.Format), payload)
		if err != nil {
			return normalize.Artifact{}, err
		}
		outcome, err := e.media.DescribeImage(ctx, models.VisionRequest{
			Model: e.config.VisionModel, MediaType: item.MediaType, Image: payload,
			Width: width, Height: height, PromptVersion: e.config.VisionPromptVersion,
		})
		if err != nil {
			return normalize.Artifact{}, err
		}
		regions := make([]normalize.ImageRegion, 0, len(outcome.Regions))
		for _, region := range outcome.Regions {
			regions = append(regions, normalize.ImageRegion{
				Text: region.Text, X: region.X, Y: region.Y, Width: region.Width, Height: region.Height,
			})
		}
		return normalize.Image(input, regions)
	case source.FormatMP3, source.FormatWAV, source.FormatM4A:
		if e == nil || e.media == nil || e.config.TranscriptionModel == "" {
			return normalize.Artifact{}, errors.New("transcription Extractor Adapter is not configured")
		}
		outcome, err := e.media.Transcribe(ctx, models.TranscriptionRequest{
			Model: e.config.TranscriptionModel, Filename: item.Title, MediaType: item.MediaType, Audio: payload,
		})
		if err != nil {
			return normalize.Artifact{}, err
		}
		segments := make([]normalize.TranscriptSegment, 0, len(outcome.Segments))
		for _, segment := range outcome.Segments {
			segments = append(segments, normalize.TranscriptSegment{StartMS: segment.StartMS, EndMS: segment.EndMS, Text: segment.Text})
		}
		return normalize.Transcript(input, segments)
	case source.FormatYouTube:
		return normalize.YouTube(input)
	default:
		return normalize.Artifact{}, errors.New("Extractor Adapter is not configured for Source format")
	}
}

func (p *Processor) fail(ctx context.Context, lease sourcejobs.Lease, code string) error {
	return p.queue.Fail(ctx, lease.ID, lease.LeaseToken, code)
}

func (p *Processor) failTraced(ctx context.Context, lease sourcejobs.Lease, trace *sourceProcessingTrace, code string) error {
	trace.markFailure(code)
	return p.fail(ctx, lease, code)
}

func stableRevisionID(sourceID, extractionConfigID string) string {
	digest := sha256.Sum256([]byte(sourceID + "\x00" + extractionConfigID))
	return "evr_" + hex.EncodeToString(digest[:16])
}

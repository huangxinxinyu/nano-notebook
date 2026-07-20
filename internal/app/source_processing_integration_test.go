package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/evidence"
	"github.com/huangxinxinyu/nano-notebook/internal/normalize"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/source"
	"github.com/huangxinxinyu/nano-notebook/internal/sourcejobs"
	"github.com/huangxinxinyu/nano-notebook/internal/sourceprocessing"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTextSourceProcessorPublishesActiveEvidenceAndReadyAtomically(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "source-processing@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-processing")
	ownerID := sourceTestUserID(t, api, "source-processing@example.com")
	payload := []byte("# Evidence\n\nFirst 段落.\n")
	objectKey := seedProcessableSource(t, api, ownerID, notebookID, "src_processing", "srcjob_processing", source.FormatMarkdown, payload)

	objects := objectstore.NewMemoryStore()
	if err := objects.Put(context.Background(), objectKey, payload); err != nil {
		t.Fatal(err)
	}
	queue := sourcejobs.NewQueue(api.db.Pool(), time.Minute)
	lease, ok, err := queue.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("Claim=%+v ok=%v err=%v", lease, ok, err)
	}
	projection := newRecordingEvidenceProjection(t, api)
	processor := sourceprocessing.NewProcessor(api.db.Pool(), queue, evidence.NewPublisher(api.db.Pool(), objects), objects, projection, sourceprocessing.Config{
		ExtractionConfigID: "extract-text-v1", MaxSourceBytes: 1 << 20, MaxNormalizedRunes: 10_000,
	})
	if err := processor.ProcessLease(context.Background(), lease); err != nil {
		t.Fatalf("ProcessLease: %v", err)
	}

	var sourceState source.State
	var jobState, revisionState string
	var activeRevisionCount int
	if err := api.db.Pool().QueryRow(context.Background(), `
		select s.state, j.status,
			(select count(*) from source_evidence_revisions r where r.source_id=s.id and r.status='active'),
			(select status from source_evidence_revisions r where r.source_id=s.id order by revision_no desc limit 1)
		from source_sources s join source_processing_jobs j on j.source_id=s.id
		where s.id='src_processing'
	`).Scan(&sourceState, &jobState, &activeRevisionCount, &revisionState); err != nil {
		t.Fatal(err)
	}
	if sourceState != source.StateReady || jobState != "succeeded" || activeRevisionCount != 1 || revisionState != "active" {
		t.Fatalf("source=%s job=%s active=%d revision=%s", sourceState, jobState, activeRevisionCount, revisionState)
	}
	if projection.builds != 1 || projection.verifications != 1 || projection.revisionID == "" || projection.unitCount != 2 {
		t.Fatalf("projection = %+v", projection)
	}
	if objects.Len() != 2 {
		t.Fatalf("object count=%d, want original plus normalized artifact", objects.Len())
	}
}

func TestPDFSourceProcessorPublishesCoordinateEvidenceAndReady(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "source-processing-pdf@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-processing-pdf")
	ownerID := sourceTestUserID(t, api, "source-processing-pdf@example.com")
	payload := evidenceTestPDF("PDF pipeline evidence.")
	objectKey := seedProcessableSource(t, api, ownerID, notebookID, "src_processing_pdf", "srcjob_processing_pdf", source.FormatPDF, payload)
	objects := objectstore.NewMemoryStore()
	if err := objects.Put(context.Background(), objectKey, payload); err != nil {
		t.Fatal(err)
	}
	queue := sourcejobs.NewQueue(api.db.Pool(), time.Minute)
	lease, ok, err := queue.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("Claim=%+v ok=%v err=%v", lease, ok, err)
	}
	processor := sourceprocessing.NewProcessor(
		api.db.Pool(), queue, evidence.NewPublisher(api.db.Pool(), objects), objects, newRecordingEvidenceProjection(t, api),
		sourceprocessing.Config{ExtractionConfigID: "extract-native-v1", MaxSourceBytes: 1 << 20, MaxNormalizedRunes: 10_000},
	)
	if err := processor.ProcessLease(context.Background(), lease); err != nil {
		t.Fatalf("ProcessLease: %v", err)
	}
	var state source.State
	var coordinateKind string
	if err := api.db.Pool().QueryRow(context.Background(), `
		select s.state, u.coordinate_json->>'kind'
		from source_sources s
		join source_evidence_revisions r on r.source_id=s.id and r.status='active'
		join source_evidence_units u on u.revision_id=r.id
		where s.id='src_processing_pdf'
	`).Scan(&state, &coordinateKind); err != nil {
		t.Fatal(err)
	}
	if state != source.StateReady || coordinateKind != "pdf_region" {
		t.Fatalf("state=%q coordinate=%q", state, coordinateKind)
	}
}

func TestSourceProcessorPublishesOnlyBoundedNonPrimaryCoverageGaps(t *testing.T) {
	t.Run("bounded gap reaches Ready", func(t *testing.T) {
		api := newTestAPI(t)
		owner := api.register(t, "source-processing-gap@example.com")
		notebookID := createSourceTestNotebook(t, api, owner, "source-processing-gap")
		ownerID := sourceTestUserID(t, api, "source-processing-gap@example.com")
		payload := []byte("bounded-pdf-fixture")
		objectKey := seedProcessableSource(t, api, ownerID, notebookID, "src_processing_gap", "srcjob_processing_gap", source.FormatPDF, payload)
		objects := objectstore.NewMemoryStore()
		if err := objects.Put(context.Background(), objectKey, payload); err != nil {
			t.Fatal(err)
		}
		artifact := processingGapArtifact(t, "src_processing_gap")
		queue := sourcejobs.NewQueue(api.db.Pool(), time.Minute)
		lease, ok, err := queue.Claim(context.Background())
		if err != nil || !ok {
			t.Fatalf("Claim=%+v ok=%v err=%v", lease, ok, err)
		}
		processor := sourceprocessing.NewProcessorWithExtractor(
			api.db.Pool(), queue, evidence.NewPublisher(api.db.Pool(), objects), objects, newRecordingEvidenceProjection(t, api),
			fixedExtractor{artifact: artifact},
			sourceprocessing.Config{ExtractionConfigID: "extract-gap-v1", MaxSourceBytes: 1 << 20, MaxNormalizedRunes: 10_000},
		)
		if err := processor.ProcessLease(context.Background(), lease); err != nil {
			t.Fatal(err)
		}
		assertSourceJobState(t, api, "src_processing_gap", "srcjob_processing_gap", source.StateReady, "succeeded", "")
	})

	t.Run("unknown gap fails before publication", func(t *testing.T) {
		api := newTestAPI(t)
		owner := api.register(t, "source-processing-unknown-gap@example.com")
		notebookID := createSourceTestNotebook(t, api, owner, "source-processing-unknown-gap")
		ownerID := sourceTestUserID(t, api, "source-processing-unknown-gap@example.com")
		payload := []byte("unknown-pdf-fixture")
		objectKey := seedProcessableSource(t, api, ownerID, notebookID, "src_processing_unknown_gap", "srcjob_processing_unknown_gap", source.FormatPDF, payload)
		objects := objectstore.NewMemoryStore()
		if err := objects.Put(context.Background(), objectKey, payload); err != nil {
			t.Fatal(err)
		}
		artifact := processingGapArtifact(t, "src_processing_unknown_gap")
		artifact.Coverage.Gaps[0].Coordinate = nil
		queue := sourcejobs.NewQueue(api.db.Pool(), time.Minute)
		lease, ok, err := queue.Claim(context.Background())
		if err != nil || !ok {
			t.Fatalf("Claim=%+v ok=%v err=%v", lease, ok, err)
		}
		processor := sourceprocessing.NewProcessorWithExtractor(
			api.db.Pool(), queue, evidence.NewPublisher(api.db.Pool(), objects), objects, &recordingEvidenceProjection{},
			fixedExtractor{artifact: artifact},
			sourceprocessing.Config{ExtractionConfigID: "extract-gap-v1", MaxSourceBytes: 1 << 20, MaxNormalizedRunes: 10_000},
		)
		if err := processor.ProcessLease(context.Background(), lease); err != nil {
			t.Fatal(err)
		}
		assertSourceJobState(t, api, "src_processing_unknown_gap", "srcjob_processing_unknown_gap", source.StateFailed, "failed", "extraction_invalid")
	})
}

func TestTextSourceProcessorTerminallyFailsIntegrityMismatch(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "source-processing-mismatch@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-processing-mismatch")
	ownerID := sourceTestUserID(t, api, "source-processing-mismatch@example.com")
	expected := []byte("right")
	objectKey := seedProcessableSource(t, api, ownerID, notebookID, "src_processing_bad", "srcjob_processing_bad", source.FormatTXT, expected)

	objects := objectstore.NewMemoryStore()
	if err := objects.Put(context.Background(), objectKey, []byte("wrong")); err != nil {
		t.Fatal(err)
	}
	queue := sourcejobs.NewQueue(api.db.Pool(), time.Minute)
	lease, ok, err := queue.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("Claim=%+v ok=%v err=%v", lease, ok, err)
	}
	processor := sourceprocessing.NewProcessor(api.db.Pool(), queue, evidence.NewPublisher(api.db.Pool(), objects), objects, &recordingEvidenceProjection{}, sourceprocessing.Config{
		ExtractionConfigID: "extract-text-v1", MaxSourceBytes: 1 << 20, MaxNormalizedRunes: 10_000,
	})
	if err := processor.ProcessLease(context.Background(), lease); err != nil {
		t.Fatalf("handled permanent failure returned error: %v", err)
	}
	assertSourceJobState(t, api, "src_processing_bad", "srcjob_processing_bad", source.StateFailed, "failed", "source_integrity_mismatch")
}

func TestTextSourceProcessorResumesFromPublishedEvidenceBoundary(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "source-processing-resume@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-processing-resume")
	ownerID := sourceTestUserID(t, api, "source-processing-resume@example.com")
	payload := []byte("One.\n\nTwo.\n")
	objectKey := seedProcessableSource(t, api, ownerID, notebookID, "src_processing_resume", "srcjob_processing_resume", source.FormatTXT, payload)
	objects := objectstore.NewMemoryStore()
	if err := objects.Put(context.Background(), objectKey, payload); err != nil {
		t.Fatal(err)
	}
	queue := sourcejobs.NewQueue(api.db.Pool(), time.Minute)
	lease, ok, err := queue.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("Claim=%+v ok=%v err=%v", lease, ok, err)
	}
	projection := newRecordingEvidenceProjection(t, api)
	projection.buildError = errors.New("Qdrant temporarily unavailable")
	processor := sourceprocessing.NewProcessor(api.db.Pool(), queue, evidence.NewPublisher(api.db.Pool(), objects), objects, projection, sourceprocessing.Config{
		ExtractionConfigID: "extract-text-v1", MaxSourceBytes: 1 << 20, MaxNormalizedRunes: 10_000,
	})
	if err := processor.ProcessLease(context.Background(), lease); err == nil {
		t.Fatal("first processing attempt unexpectedly succeeded")
	}
	assertSourceJobState(t, api, "src_processing_resume", "srcjob_processing_resume", source.StateSegmenting, "running", "")
	projection.buildError = nil
	if err := processor.ProcessLease(context.Background(), lease); err != nil {
		t.Fatalf("resumed ProcessLease: %v", err)
	}
	assertSourceJobState(t, api, "src_processing_resume", "srcjob_processing_resume", source.StateReady, "succeeded", "")
	if objects.Len() != 2 || projection.builds != 2 || projection.verifications != 1 {
		t.Fatalf("objects=%d projection=%+v", objects.Len(), projection)
	}
}

type recordingEvidenceProjection struct {
	builds         int
	verifications  int
	revisionID     string
	unitCount      int
	buildError     error
	pool           *pgxpool.Pool
	indexVersionID string
}

type fixedExtractor struct {
	artifact normalize.Artifact
}

func (f fixedExtractor) Extract(_ source.Source, _ []byte, _ string) (normalize.Artifact, error) {
	return f.artifact, nil
}

func processingGapArtifact(t *testing.T, sourceID string) normalize.Artifact {
	t.Helper()
	artifact, err := normalize.Finalize(normalize.Artifact{
		SchemaVersion: "nano.normalized-source.v1", SourceID: sourceID,
		ExtractionConfigID: "extract-gap-v1", Format: "pdf", Text: "Primary evidence.",
		Blocks: []normalize.Block{{
			ID: "block_000001", Ordinal: 0, Kind: "paragraph", Text: "Primary evidence.", StartRune: 0, EndRune: 17,
			Coordinate: &normalize.SourceCoordinate{Kind: "pdf_region", Page: 1, X: 72, Y: 700, Width: 110, Height: 12},
		}},
		Coverage: normalize.Coverage{Status: "partial", TotalRunes: 17, Gaps: []normalize.Gap{{
			Reason: "decorative_visual_skipped", Impact: "non_primary",
			Coordinate: &normalize.SourceCoordinate{Kind: "pdf_region", Page: 1, X: 300, Y: 500, Width: 80, Height: 60},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func (p *recordingEvidenceProjection) Build(_ context.Context, command sourceprocessing.ProjectionCommand) error {
	p.builds++
	p.revisionID = command.RevisionID
	p.unitCount = len(command.Artifact.Blocks)
	return p.buildError
}

func (p *recordingEvidenceProjection) Verify(_ context.Context, command sourceprocessing.ProjectionCommand) error {
	p.verifications++
	if command.RevisionID != p.revisionID {
		return sourceprocessing.ErrProjectionInvalid
	}
	if p.pool == nil {
		return nil
	}
	_, err := p.pool.Exec(context.Background(), `
		insert into retrieval_source_index_builds(
			revision_id, index_version_id, source_id, notebook_id, expected_points, projection_sha256, status, verified_at
		) values ($1,$2,$3,$4,$5,$6,'verified',now())
	`, command.RevisionID, p.indexVersionID, command.Lease.SourceID, command.Lease.NotebookID, p.unitCount, strings.Repeat("d", 64))
	return err
}

func newRecordingEvidenceProjection(t *testing.T, api *testAPI) *recordingEvidenceProjection {
	t.Helper()
	const versionID = "riv_recording_projection"
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into retrieval_index_versions(
			id, config_json, config_sha256, status, promoted_by_eval_run_id, promoted_at
		) values ($1, '{}'::jsonb, $2, 'active', 'eval_recording_projection', now())
	`, versionID, strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	return &recordingEvidenceProjection{pool: api.db.Pool(), indexVersionID: versionID}
}

func seedProcessableSource(t *testing.T, api *testAPI, ownerID, notebookID, sourceID, jobID string, format source.Format, payload []byte) string {
	t.Helper()
	digest := sha256.Sum256(payload)
	sha := hex.EncodeToString(digest[:])
	objectKey := "sources/" + sourceID + "/original/" + sha
	err := api.db.WithRequestPrincipal(context.Background(), ownerID, func(tx pgx.Tx) error {
		created, err := source.NewStore(tx).CreateUploaded(context.Background(), source.CreateUploadedCommand{
			ID: sourceID, NotebookID: notebookID, Title: sourceID + "." + string(format), Format: format,
			MediaType: sourceProcessingMediaType(format), ByteSize: int64(len(payload)), ContentSHA256: sha,
			OriginalObjectKey: objectKey,
		})
		if err != nil {
			return err
		}
		_, err = tx.Exec(context.Background(), `
			insert into source_processing_jobs(id, source_id, notebook_id, status)
			values ($1, $2, $3, 'queued')
		`, jobID, created.ID, created.NotebookID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return objectKey
}

func sourceProcessingMediaType(format source.Format) string {
	if format == source.FormatMarkdown {
		return "text/markdown"
	}
	if format == source.FormatPDF {
		return "application/pdf"
	}
	return "text/plain"
}

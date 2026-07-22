package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentbatch"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/huangxinxinyu/nano-notebook/internal/documentrender"
	"github.com/huangxinxinyu/nano-notebook/internal/evidence"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/normalize"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/qdrantstore"
	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
	"github.com/huangxinxinyu/nano-notebook/internal/source"
	"github.com/huangxinxinyu/nano-notebook/internal/sourcejobs"
	"github.com/huangxinxinyu/nano-notebook/internal/sourceprocessing"
	"github.com/huangxinxinyu/nano-notebook/internal/sourceprojection"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFreshDatabaseBootstrapProcessesFirstTextSourceToReady(t *testing.T) {
	api := newTestAPI(t)
	config := testRetrievalIndexConfig()
	config.EmbeddingModel = "test/embed"
	config.EmbeddingDimensions = 3
	version, created, err := retrieval.NewVersionStore(api.db.Pool()).BootstrapDevelopment(
		context.Background(), "riv_dev_baseline_v1", "dev-bootstrap-v1", config,
	)
	if err != nil || !created {
		t.Fatalf("BootstrapDevelopment version=%+v created=%t err=%v", version, created, err)
	}

	owner := api.register(t, "fresh-bootstrap-source@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "fresh-bootstrap-source")
	ownerID := sourceTestUserID(t, api, "fresh-bootstrap-source@example.com")
	payload := []byte("The first Source can become searchable on a clean development database.")
	objectKey := seedProcessableSource(t, api, ownerID, notebookID, "src_fresh_bootstrap", "srcjob_fresh_bootstrap", source.FormatTXT, payload)
	objects := objectstore.NewMemoryStore()
	if err := objects.Put(context.Background(), objectKey, payload); err != nil {
		t.Fatal(err)
	}
	queue := sourcejobs.NewQueue(api.db.Pool(), time.Minute)
	lease, ok, err := queue.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("Claim=%+v ok=%v err=%v", lease, ok, err)
	}
	vectors := &memoryVectorStore{}
	projection := sourceprojection.New(api.db.Pool(), vectors, deterministicEmbedder{})
	processor := sourceprocessing.NewProcessor(api.db.Pool(), queue, evidence.NewPublisher(api.db.Pool(), objects), objects, projection, sourceprocessing.Config{
		ExtractionConfigID: "extract-text-v1", MaxSourceBytes: 1 << 20, MaxNormalizedRunes: 10_000,
	})
	if err := processor.ProcessLease(context.Background(), lease); err != nil {
		t.Fatalf("ProcessLease: %v", err)
	}
	assertSourceJobState(t, api, "src_fresh_bootstrap", "srcjob_fresh_bootstrap", source.StateReady, "succeeded", "")
	if len(vectors.points) == 0 {
		t.Fatal("fresh Source produced no Retrieval points")
	}
}

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

func TestSourceProcessorEmitsSafeWorkloadAndStageTraceMetadata(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "source-processing-trace@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-processing-trace")
	ownerID := sourceTestUserID(t, api, "source-processing-trace@example.com")
	payload := []byte("Trace-visible coverage, never Trace-visible body.")
	objectKey := seedProcessableSource(t, api, ownerID, notebookID, "src_processing_trace", "srcjob_processing_trace", source.FormatTXT, payload)
	objects := objectstore.NewMemoryStore()
	if err := objects.Put(context.Background(), objectKey, payload); err != nil {
		t.Fatal(err)
	}
	queue := sourcejobs.NewQueue(api.db.Pool(), time.Minute)
	lease, ok, err := queue.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("Claim=%+v ok=%v err=%v", lease, ok, err)
	}
	sink := &recordingSourceTraceSink{err: errors.New("Collector unavailable")}
	processor := sourceprocessing.NewProcessorWithExtractorAndTrace(
		api.db.Pool(), queue, evidence.NewPublisher(api.db.Pool(), objects), objects, newRecordingEvidenceProjection(t, api),
		sourceprocessing.NewNativeExtractor(nil, sourceprocessing.NativeExtractorConfig{}), sink,
		sourceprocessing.Config{ExtractionConfigID: "extract-trace-v1", ExtractorAdapterID: "native-in-process", MaxSourceBytes: 1 << 20, MaxNormalizedRunes: 10_000},
	)
	if err := processor.ProcessLease(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	envelopes := sink.snapshot()
	if len(envelopes) != 12 {
		t.Fatalf("Trace records=%d, want root plus five complete stages: %#v", len(envelopes), envelopes)
	}
	descriptor := envelopes[0].Trace
	if descriptor.WorkloadKind != collector.WorkloadSourceProcessing || descriptor.WorkloadID != "srcjob_processing_trace/attempt-1" ||
		descriptor.RunID != "" || descriptor.ChatID != "" || descriptor.NotebookID != notebookID {
		t.Fatalf("Source Trace descriptor=%#v", descriptor)
	}
	names := make(map[string]int)
	for _, envelope := range envelopes {
		if err := envelope.Record.Validate(); err != nil {
			t.Fatalf("invalid Source Trace record: %v (%#v)", err, envelope.Record)
		}
		names[envelope.Record.Name]++
		if envelope.Trace != descriptor {
			t.Fatal("Source Trace descriptor changed within one invocation")
		}
	}
	for _, name := range []string{"source.processing", "source.validating", "source.normalizing", "source.segmenting", "source.indexing", "source.verifying"} {
		if names[name] != 2 {
			t.Fatalf("Span %q record count=%d; names=%v", name, names[name], names)
		}
	}
	terminal := envelopes[len(envelopes)-1].Record
	if terminal.Status != agentobs.StatusOK || traceAttribute(terminal, "source.format") != "txt" ||
		traceAttribute(terminal, "source.extractor.adapter_id") != "native-in-process" ||
		traceAttribute(terminal, "source.extraction_config_id") != "extract-trace-v1" ||
		traceAttribute(terminal, "source.coverage.status") != "complete" || traceAttribute(terminal, "source.coverage.gap_count") != "0" {
		t.Fatalf("Source Trace terminal=%#v", terminal)
	}
	encoded := fmt.Sprintf("%#v", envelopes)
	for _, forbidden := range []string{string(payload), objectKey, "src_processing_trace.txt"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("Source Trace leaked %q", forbidden)
		}
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
	page := evidenceViewerPNG(t, 64, 32)
	processor := sourceprocessing.NewProcessorWithExtractorTraceAndRenderer(
		api.db.Pool(), queue, evidence.NewPublisher(api.db.Pool(), objects), objects, newRecordingEvidenceProjection(t, api),
		sourceprocessing.NewNativeExtractor(nil, sourceprocessing.NativeExtractorConfig{}), fixedDocumentRenderer{page: page}, nil,
		sourceprocessing.Config{ExtractionConfigID: "extract-native-v1", MaxSourceBytes: 1 << 20, MaxNormalizedRunes: 10_000,
			RenderConfigID: "pdfium-v1", RenderMaxPages: 10, RenderDPI: 144, RenderMaxPixelsPerPage: 1_000_000, RenderMaxOutputBytes: 1 << 20},
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

func TestPDFSourceProcessorCallsVisionOnlyForNativeTextlessPages(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "source-processing-mixed-pdf@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-processing-mixed-pdf")
	ownerID := sourceTestUserID(t, api, "source-processing-mixed-pdf@example.com")
	payload := evidenceTestPDF("Native page evidence.", "")
	objectKey := seedProcessableSource(t, api, ownerID, notebookID, "src_processing_mixed_pdf", "srcjob_processing_mixed_pdf", source.FormatPDF, payload)
	objects := objectstore.NewMemoryStore()
	if err := objects.Put(context.Background(), objectKey, payload); err != nil {
		t.Fatal(err)
	}
	queue := sourcejobs.NewQueue(api.db.Pool(), time.Minute)
	lease, ok, err := queue.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("Claim=%+v ok=%v err=%v", lease, ok, err)
	}
	media := &recordingMediaModels{}
	pageOne, pageTwo := evidenceViewerPNG(t, 64, 32), evidenceViewerPNG(t, 64, 32)
	processor := sourceprocessing.NewProcessorWithExtractorTraceAndRenderer(
		api.db.Pool(), queue, evidence.NewPublisher(api.db.Pool(), objects), objects, newRecordingEvidenceProjection(t, api),
		sourceprocessing.NewNativeExtractor(media, sourceprocessing.NativeExtractorConfig{VisionModel: "vision-model", VisionPromptVersion: "vision-v1"}),
		fixedDocumentRenderer{pages: [][]byte{pageOne, pageTwo}}, nil,
		sourceprocessing.Config{ExtractionConfigID: "extract-mixed-v1", MaxSourceBytes: 1 << 20, MaxNormalizedRunes: 10_000,
			RenderConfigID: "pdfium-v1", RenderMaxPages: 10, RenderDPI: 144, RenderMaxPixelsPerPage: 1_000_000, RenderMaxOutputBytes: 1 << 20},
	)
	if err := processor.ProcessLease(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	var state source.State
	var texts []string
	rows, err := api.db.Pool().Query(context.Background(), `
		select s.state,u.text_content from source_sources s
		join source_evidence_revisions r on r.source_id=s.id and r.status='active'
		join source_evidence_units u on u.revision_id=r.id
		where s.id='src_processing_mixed_pdf' order by u.ordinal
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var text string
		if err := rows.Scan(&state, &text); err != nil {
			t.Fatal(err)
		}
		texts = append(texts, text)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if state != source.StateReady || !reflect.DeepEqual(texts, []string{"Native page evidence.", "One pixel evidence."}) || media.calls != 1 {
		t.Fatalf("state=%q texts=%v vision calls=%d", state, texts, media.calls)
	}
}

func TestDOCXSourceProcessorPublishesStructuralEvidenceAndReady(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "source-processing-docx@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-processing-docx")
	ownerID := sourceTestUserID(t, api, "source-processing-docx@example.com")
	payload := evidenceTestDOCX("DOCX pipeline evidence.")
	objectKey := seedProcessableSource(t, api, ownerID, notebookID, "src_processing_docx", "srcjob_processing_docx", source.FormatDOCX, payload)
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
		where s.id='src_processing_docx'
	`).Scan(&state, &coordinateKind); err != nil {
		t.Fatal(err)
	}
	if state != source.StateReady || coordinateKind != "document_block" {
		t.Fatalf("state=%q coordinate=%q", state, coordinateKind)
	}
}

func TestHTMLSnapshotProcessorPublishesPrimaryEvidenceAndReady(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "source-processing-html@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-processing-html")
	ownerID := sourceTestUserID(t, api, "source-processing-html@example.com")
	payload := []byte(`<html><body><nav>Noise</nav><main><h1>Snapshot</h1><p>Primary web evidence.</p></main></body></html>`)
	objectKey := seedProcessableSource(t, api, ownerID, notebookID, "src_processing_html", "srcjob_processing_html", source.FormatTXT, payload)
	if _, err := api.db.Pool().Exec(context.Background(), `
		update source_sources set format='html', media_type='text/html' where id='src_processing_html'
	`); err != nil {
		t.Fatal(err)
	}
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
		t.Fatal(err)
	}
	var state source.State
	var evidenceText, coordinateKind string
	if err := api.db.Pool().QueryRow(context.Background(), `
		select s.state, string_agg(u.text_content, ' ' order by u.ordinal), min(u.coordinate_json->>'kind')
		from source_sources s
		join source_evidence_revisions r on r.source_id=s.id and r.status='active'
		join source_evidence_units u on u.revision_id=r.id
		where s.id='src_processing_html' group by s.state
	`).Scan(&state, &evidenceText, &coordinateKind); err != nil {
		t.Fatal(err)
	}
	if state != source.StateReady || evidenceText != "Snapshot Primary web evidence." || coordinateKind != "html_block" {
		t.Fatalf("state=%q evidence=%q coordinate=%q", state, evidenceText, coordinateKind)
	}
}

func TestMediaSourceProcessorPublishesModelOutputOnlyThroughNativeBounds(t *testing.T) {
	tests := []struct {
		name           string
		format         source.Format
		payload        []byte
		coordinateKind string
	}{
		{name: "image", format: source.FormatPNG, payload: mustDecodeBase64("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl2n0YAAAAASUVORK5CYII="), coordinateKind: "image_region"},
		{name: "audio", format: source.FormatMP3, payload: []byte("ID3-audio-fixture"), coordinateKind: "time_interval"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			api := newTestAPI(t)
			owner := api.register(t, "source-processing-media-"+test.name+"@example.com")
			notebookID := createSourceTestNotebook(t, api, owner, "source-processing-media-"+test.name)
			ownerID := sourceTestUserID(t, api, "source-processing-media-"+test.name+"@example.com")
			sourceID, jobID := "src_processing_media_"+test.name, "srcjob_processing_media_"+test.name
			objectKey := seedProcessableSource(t, api, ownerID, notebookID, sourceID, jobID, test.format, test.payload)
			objects := objectstore.NewMemoryStore()
			if err := objects.Put(context.Background(), objectKey, test.payload); err != nil {
				t.Fatal(err)
			}
			queue := sourcejobs.NewQueue(api.db.Pool(), time.Minute)
			lease, ok, err := queue.Claim(context.Background())
			if err != nil || !ok {
				t.Fatalf("Claim=%+v ok=%v err=%v", lease, ok, err)
			}
			media := &recordingMediaModels{}
			extractor := sourceprocessing.NewNativeExtractor(media, sourceprocessing.NativeExtractorConfig{
				VisionModel: "gemini/vision", TranscriptionModel: "openai/whisper-1", VisionPromptVersion: "vision-normalize-v1",
			})
			processor := sourceprocessing.NewProcessorWithExtractor(
				api.db.Pool(), queue, evidence.NewPublisher(api.db.Pool(), objects), objects, newRecordingEvidenceProjection(t, api), extractor,
				sourceprocessing.Config{ExtractionConfigID: "extract-media-v1", MaxSourceBytes: 1 << 20, MaxNormalizedRunes: 10_000},
			)
			if err := processor.ProcessLease(context.Background(), lease); err != nil {
				t.Fatal(err)
			}
			var state source.State
			var coordinateKind string
			if err := api.db.Pool().QueryRow(context.Background(), `
				select s.state, u.coordinate_json->>'kind'
				from source_sources s
				join source_evidence_revisions r on r.source_id=s.id and r.status='active'
				join source_evidence_units u on u.revision_id=r.id
				where s.id=$1
			`, sourceID).Scan(&state, &coordinateKind); err != nil {
				t.Fatal(err)
			}
			if state != source.StateReady || coordinateKind != test.coordinateKind || media.calls != 1 {
				t.Fatalf("state=%q coordinate=%q model calls=%d", state, coordinateKind, media.calls)
			}
		})
	}
}

func TestYouTubeCaptionSnapshotProcessorPublishesIntervalsWithoutRefetch(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "source-processing-youtube@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-processing-youtube")
	ownerID := sourceTestUserID(t, api, "source-processing-youtube@example.com")
	payload := []byte(`{"schema_version":"nano.youtube-captions.v1","video_id":"dQw4w9WgXcQ","language":"en","segments":[{"start_ms":0,"end_ms":1250,"text":"Immutable caption."}]}`)
	objectKey := seedProcessableSource(t, api, ownerID, notebookID, "src_processing_youtube", "srcjob_processing_youtube", source.FormatTXT, payload)
	if _, err := api.db.Pool().Exec(context.Background(), `
		update source_sources set format='youtube', media_type='application/vnd.nano.youtube-captions+json' where id='src_processing_youtube'
	`); err != nil {
		t.Fatal(err)
	}
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
		sourceprocessing.Config{ExtractionConfigID: "youtube-captions-v1", MaxSourceBytes: 1 << 20, MaxNormalizedRunes: 10_000},
	)
	if err := processor.ProcessLease(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	var state source.State
	var coordinateKind string
	if err := api.db.Pool().QueryRow(context.Background(), `
		select s.state, u.coordinate_json->>'kind'
		from source_sources s
		join source_evidence_revisions r on r.source_id=s.id and r.status='active'
		join source_evidence_units u on u.revision_id=r.id
		where s.id='src_processing_youtube'
	`).Scan(&state, &coordinateKind); err != nil {
		t.Fatal(err)
	}
	if state != source.StateReady || coordinateKind != "time_interval" {
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
		processor := sourceprocessing.NewProcessorWithExtractorTraceAndRenderer(
			api.db.Pool(), queue, evidence.NewPublisher(api.db.Pool(), objects), objects, newRecordingEvidenceProjection(t, api),
			fixedExtractor{artifact: artifact}, fixedDocumentRenderer{page: evidenceViewerPNG(t, 64, 32)}, nil,
			sourceprocessing.Config{ExtractionConfigID: "extract-gap-v1", MaxSourceBytes: 1 << 20, MaxNormalizedRunes: 10_000,
				RenderConfigID: "pdfium-v1", RenderMaxPages: 10, RenderDPI: 144, RenderMaxPixelsPerPage: 1_000_000, RenderMaxOutputBytes: 1 << 20},
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

func TestSourceProcessorClassifiesExtractorBudgetFailure(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "source-processing-budget@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-processing-budget")
	ownerID := sourceTestUserID(t, api, "source-processing-budget@example.com")
	payload := []byte("oversized expanded structure")
	objectKey := seedProcessableSource(t, api, ownerID, notebookID, "src_processing_budget", "srcjob_processing_budget", source.FormatDOCX, payload)
	objects := objectstore.NewMemoryStore()
	if err := objects.Put(context.Background(), objectKey, payload); err != nil {
		t.Fatal(err)
	}
	queue := sourcejobs.NewQueue(api.db.Pool(), time.Minute)
	lease, ok, err := queue.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("Claim=%+v ok=%v err=%v", lease, ok, err)
	}
	sink := &recordingSourceTraceSink{}
	processor := sourceprocessing.NewProcessorWithExtractorAndTrace(
		api.db.Pool(), queue, evidence.NewPublisher(api.db.Pool(), objects), objects, &recordingEvidenceProjection{},
		fixedExtractor{err: normalize.ErrProcessingBudget}, sink,
		sourceprocessing.Config{ExtractionConfigID: "extract-budget-v1", MaxSourceBytes: 1 << 20, MaxNormalizedRunes: 10_000},
	)
	if err := processor.ProcessLease(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	assertSourceJobState(t, api, "src_processing_budget", "srcjob_processing_budget", source.StateFailed, "failed", "processing_budget_exceeded")
	envelopes := sink.snapshot()
	terminal := envelopes[len(envelopes)-1].Record
	if terminal.Status != agentobs.StatusError || traceAttribute(terminal, "source.failure.code") != "processing_budget_exceeded" ||
		traceAttribute(terminal, "source.format") != "docx" {
		t.Fatalf("budget Trace terminal=%#v", terminal)
	}
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

func TestSourceProcessorTerminallyFailsMissingRetrievalAuthority(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "source-processing-retrieval-unavailable@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-processing-retrieval-unavailable")
	ownerID := sourceTestUserID(t, api, "source-processing-retrieval-unavailable@example.com")
	payload := []byte("Evidence awaiting retrieval authority.")
	objectKey := seedProcessableSource(t, api, ownerID, notebookID, "src_processing_retrieval_unavailable", "srcjob_processing_retrieval_unavailable", source.FormatTXT, payload)
	objects := objectstore.NewMemoryStore()
	if err := objects.Put(context.Background(), objectKey, payload); err != nil {
		t.Fatal(err)
	}
	queue := sourcejobs.NewQueue(api.db.Pool(), time.Minute)
	lease, ok, err := queue.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("Claim=%+v ok=%v err=%v", lease, ok, err)
	}
	projection := &recordingEvidenceProjection{buildError: sourceprocessing.ErrRetrievalUnavailable}
	processor := sourceprocessing.NewProcessor(api.db.Pool(), queue, evidence.NewPublisher(api.db.Pool(), objects), objects, projection, sourceprocessing.Config{
		ExtractionConfigID: "extract-text-v1", MaxSourceBytes: 1 << 20, MaxNormalizedRunes: 10_000,
	})
	if err := processor.ProcessLease(context.Background(), lease); err != nil {
		t.Fatalf("ProcessLease terminal failure: %v", err)
	}
	assertSourceJobState(t, api, "src_processing_retrieval_unavailable", "srcjob_processing_retrieval_unavailable", source.StateFailed, "failed", "retrieval_unavailable")
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

type memoryVectorStore struct {
	points []qdrantstore.Point
}

func (s *memoryVectorStore) Upsert(_ context.Context, points []qdrantstore.Point) error {
	s.points = append(s.points, points...)
	return nil
}

func (s *memoryVectorStore) Count(_ context.Context, scope qdrantstore.Scope) (int, error) {
	count := 0
	for _, point := range s.points {
		if point.NotebookID == scope.NotebookID && point.IndexVersionID == scope.IndexVersionID {
			count++
		}
	}
	return count, nil
}

type fixedExtractor struct {
	artifact normalize.Artifact
	err      error
}

type fixedDocumentRenderer struct {
	page  []byte
	pages [][]byte
	err   error
}

func (r fixedDocumentRenderer) Render(_ context.Context, request documentrender.Request, _ []byte) (documentrender.Result, error) {
	if r.err != nil {
		return documentrender.Result{}, r.err
	}
	pages := r.pages
	if len(pages) == 0 {
		pages = [][]byte{r.page}
	}
	prefix := "page"
	if request.Format == documentrender.FormatPPTX {
		prefix = "slide"
	}
	manifest := documentrender.Manifest{
		SchemaVersion: 1, SourceID: request.SourceID, Format: request.Format, InputSHA256: request.InputSHA256,
		RenderConfigID: request.RenderConfigID, Pages: make([]documentrender.Page, 0, len(pages)),
	}
	result := documentrender.Result{Assets: make([]documentrender.Asset, 0, len(pages))}
	for index, payload := range pages {
		digest := sha256.Sum256(payload)
		page := documentrender.Page{
			Ordinal: index + 1, Width: 64, Height: 32, MediaType: "image/png", Bytes: int64(len(payload)),
			SHA256: hex.EncodeToString(digest[:]), Filename: fmt.Sprintf("%s-%06d.png", prefix, index+1),
		}
		manifest.Pages = append(manifest.Pages, page)
		result.Assets = append(result.Assets, documentrender.Asset{Page: page, Payload: payload})
	}
	result.Manifest = manifest
	return result, nil
}

type recordingSourceTraceSink struct {
	mu        sync.Mutex
	envelopes []agentbatch.Envelope
	err       error
}

func (s *recordingSourceTraceSink) Offer(_ context.Context, envelope agentbatch.Envelope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.envelopes = append(s.envelopes, envelope)
	return s.err
}

func (s *recordingSourceTraceSink) snapshot() []agentbatch.Envelope {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]agentbatch.Envelope(nil), s.envelopes...)
}

func traceAttribute(record agentobs.Record, key string) string {
	for _, attribute := range record.Attributes {
		if attribute.Key != key {
			continue
		}
		switch attribute.Value.Kind {
		case agentobs.ValueString:
			return attribute.Value.String
		case agentobs.ValueInt64:
			return fmt.Sprint(attribute.Value.Int64)
		case agentobs.ValueBool:
			return fmt.Sprint(attribute.Value.Bool)
		}
	}
	return ""
}

type recordingMediaModels struct {
	calls int
}

func (m *recordingMediaModels) DescribeImage(_ context.Context, request models.VisionRequest) (models.VisionOutcome, error) {
	m.calls++
	return models.VisionOutcome{Regions: []models.VisionRegion{{Text: "One pixel evidence.", X: 0, Y: 0, Width: float64(request.Width), Height: float64(request.Height)}}}, nil
}

func (m *recordingMediaModels) Transcribe(context.Context, models.TranscriptionRequest) (models.TranscriptionOutcome, error) {
	m.calls++
	return models.TranscriptionOutcome{Segments: []models.TranscriptSegment{{StartMS: 0, EndMS: 1250, Text: "Audio evidence."}}}, nil
}

func mustDecodeBase64(value string) []byte {
	payload, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		panic(err)
	}
	return payload
}

func (f fixedExtractor) Extract(_ context.Context, _ source.Source, _ []byte, _ string) (normalize.Artifact, error) {
	return f.artifact, f.err
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
	if format == source.FormatDOCX {
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	}
	if format == source.FormatPNG {
		return "image/png"
	}
	if format == source.FormatMP3 {
		return "audio/mpeg"
	}
	return "text/plain"
}

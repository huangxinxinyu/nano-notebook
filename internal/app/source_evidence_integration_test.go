package app_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/evidence"
	"github.com/huangxinxinyu/nano-notebook/internal/normalize"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/source"
	"github.com/huangxinxinyu/nano-notebook/internal/sourcejobs"
)

func TestEvidencePublisherPersistsValidatedArtifactUnitsUnderLease(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "source-evidence@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-evidence")
	ownerID := sourceTestUserID(t, api, "source-evidence@example.com")
	seedSourceProcessingJob(t, api, ownerID, notebookID, "src_evidence", "srcjob_evidence", "2")
	queue := sourcejobs.NewQueue(api.db.Pool(), time.Minute)
	lease, ok, err := queue.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("Claim=%+v ok=%v err=%v", lease, ok, err)
	}
	if err := queue.Advance(context.Background(), lease.ID, lease.LeaseToken, source.StateUploaded, source.StateValidating); err != nil {
		t.Fatal(err)
	}
	if err := queue.Advance(context.Background(), lease.ID, lease.LeaseToken, source.StateValidating, source.StateNormalizing); err != nil {
		t.Fatal(err)
	}
	artifact, err := normalize.Text(normalize.Input{
		SourceID: lease.SourceID, ExtractionConfigID: "extract-text-v1", Format: "txt",
		Payload: []byte("First evidence.\n\nSecond evidence.\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	objects := objectstore.NewMemoryStore()
	publisher := evidence.NewPublisher(api.db.Pool(), objects)
	revision, reused, err := publisher.Publish(context.Background(), evidence.PublishCommand{
		RevisionID: "evr_evidence", JobID: lease.ID, LeaseToken: lease.LeaseToken, Artifact: artifact,
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if reused || revision.ID != "evr_evidence" || revision.SourceID != lease.SourceID ||
		revision.Status != evidence.RevisionBuilding || revision.ArtifactSHA256 != artifact.SHA256 {
		t.Fatalf("revision=%+v reused=%v", revision, reused)
	}
	var sourceState source.State
	var unitCount int
	if err := api.db.Pool().QueryRow(context.Background(), `select state from source_sources where id=$1`, lease.SourceID).Scan(&sourceState); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from source_evidence_units where revision_id=$1`, revision.ID).Scan(&unitCount); err != nil {
		t.Fatal(err)
	}
	if sourceState != source.StateSegmenting || unitCount != len(artifact.Blocks) || objects.Len() != 1 {
		t.Fatalf("published state=%q units=%d objects=%d", sourceState, unitCount, objects.Len())
	}
	retried, reused, err := publisher.Publish(context.Background(), evidence.PublishCommand{
		RevisionID: "evr_evidence", JobID: lease.ID, LeaseToken: lease.LeaseToken, Artifact: artifact,
	})
	if err != nil || !reused || retried.ID != revision.ID || objects.Len() != 1 {
		t.Fatalf("retry revision=%+v reused=%v err=%v objects=%d", retried, reused, err, objects.Len())
	}
	if _, _, err := publisher.Publish(context.Background(), evidence.PublishCommand{
		RevisionID: "evr_stale", JobID: lease.ID, LeaseToken: "00000000-0000-4000-8000-000000000099", Artifact: artifact,
	}); !errors.Is(err, evidence.ErrLeaseLost) {
		t.Fatalf("stale Publish error=%v, want lease lost", err)
	}
}

func TestEvidencePublisherPersistsPDFSourceCoordinates(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "source-evidence-pdf@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-evidence-pdf")
	ownerID := sourceTestUserID(t, api, "source-evidence-pdf@example.com")
	seedSourceProcessingJob(t, api, ownerID, notebookID, "src_evidence_pdf", "srcjob_evidence_pdf", "7")
	if _, err := api.db.Pool().Exec(context.Background(), `
		update source_sources set format='pdf', media_type='application/pdf' where id='src_evidence_pdf'
	`); err != nil {
		t.Fatal(err)
	}
	queue := sourcejobs.NewQueue(api.db.Pool(), time.Minute)
	lease, ok, err := queue.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("Claim=%+v ok=%v err=%v", lease, ok, err)
	}
	if err := queue.Advance(context.Background(), lease.ID, lease.LeaseToken, source.StateUploaded, source.StateValidating); err != nil {
		t.Fatal(err)
	}
	if err := queue.Advance(context.Background(), lease.ID, lease.LeaseToken, source.StateValidating, source.StateNormalizing); err != nil {
		t.Fatal(err)
	}
	artifact, err := normalize.PDF(normalize.Input{
		SourceID: lease.SourceID, ExtractionConfigID: "extract-native-v1", Format: "pdf",
		Payload: evidenceTestPDF("Coordinate evidence."),
	})
	if err != nil {
		t.Fatal(err)
	}
	page := evidenceViewerPNG(t, 64, 32)
	pageDigest := sha256.Sum256(page)
	objects := objectstore.NewMemoryStore()
	revision, _, err := evidence.NewPublisher(api.db.Pool(), objects).Publish(
		context.Background(), evidence.PublishCommand{
			RevisionID: "evr_evidence_pdf", JobID: lease.ID, LeaseToken: lease.LeaseToken, Artifact: artifact,
			ViewerArtifacts: []evidence.ViewerArtifact{{
				Ordinal: 1, Width: 64, Height: 32, MediaType: "image/png", Bytes: int64(len(page)),
				SHA256: hex.EncodeToString(pageDigest[:]), Filename: "page-000001.png", RenderConfigID: "pdfium-v1", Payload: page,
			}},
		})
	if err != nil {
		t.Fatal(err)
	}
	var kind, viewerKey, viewerSHA string
	var pageNumber, viewerWidth, viewerHeight int
	if err := api.db.Pool().QueryRow(context.Background(), `
		select coordinate_json->>'kind', (coordinate_json->>'page')::integer
		from source_evidence_units where revision_id=$1
	`, revision.ID).Scan(&kind, &pageNumber); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(context.Background(), `
		select object_key,content_sha256,width,height from source_viewer_artifacts where revision_id=$1 and ordinal=1
	`, revision.ID).Scan(&viewerKey, &viewerSHA, &viewerWidth, &viewerHeight); err != nil {
		t.Fatal(err)
	}
	if kind != "pdf_region" || pageNumber != 1 || viewerKey != "sources/src_evidence_pdf/evidence/evr_evidence_pdf/viewer/page-000001.png" ||
		viewerSHA != hex.EncodeToString(pageDigest[:]) || viewerWidth != 64 || viewerHeight != 32 || objects.Len() != 2 {
		t.Fatalf("coordinate kind=%q page=%d viewer=%q sha=%q dimensions=%dx%d objects=%d", kind, pageNumber, viewerKey, viewerSHA, viewerWidth, viewerHeight, objects.Len())
	}
}

func evidenceViewerPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	value := image.NewRGBA(image.Rect(0, 0, width, height))
	value.Set(0, 0, color.RGBA{R: 255, A: 255})
	var output bytes.Buffer
	if err := png.Encode(&output, value); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestEvidenceCompletionRejectsMissingVerifiedActiveProjection(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "source-evidence-projection@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-evidence-projection")
	ownerID := sourceTestUserID(t, api, "source-evidence-projection@example.com")
	seedSourceProcessingJob(t, api, ownerID, notebookID, "src_evidence_projection", "srcjob_evidence_projection", "3")
	queue := sourcejobs.NewQueue(api.db.Pool(), time.Minute)
	lease, ok, err := queue.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("Claim=%+v ok=%v err=%v", lease, ok, err)
	}
	if err := queue.Advance(context.Background(), lease.ID, lease.LeaseToken, source.StateUploaded, source.StateValidating); err != nil {
		t.Fatal(err)
	}
	if err := queue.Advance(context.Background(), lease.ID, lease.LeaseToken, source.StateValidating, source.StateNormalizing); err != nil {
		t.Fatal(err)
	}
	artifact, err := normalize.Text(normalize.Input{
		SourceID: lease.SourceID, ExtractionConfigID: "extract-text-v1", Format: "txt", Payload: []byte("Evidence."),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := evidence.NewPublisher(api.db.Pool(), objectstore.NewMemoryStore()).Publish(context.Background(), evidence.PublishCommand{
		RevisionID: "evr_missing_projection", JobID: lease.ID, LeaseToken: lease.LeaseToken, Artifact: artifact,
	}); err != nil {
		t.Fatal(err)
	}
	if err := queue.Advance(context.Background(), lease.ID, lease.LeaseToken, source.StateSegmenting, source.StateIndexing); err != nil {
		t.Fatal(err)
	}
	if err := queue.Advance(context.Background(), lease.ID, lease.LeaseToken, source.StateIndexing, source.StateVerifying); err != nil {
		t.Fatal(err)
	}
	if err := queue.CompleteEvidence(context.Background(), lease.ID, lease.LeaseToken, "evr_missing_projection"); !errors.Is(err, sourcejobs.ErrTransitionConflict) {
		t.Fatalf("CompleteEvidence without verified active projection = %v", err)
	}
}

func evidenceTestPDF(pageTexts ...string) []byte {
	objects := make([]string, 3+2*len(pageTexts))
	objects[0] = "<< /Type /Catalog /Pages 2 0 R >>"
	kids := make([]string, 0, len(pageTexts))
	for index := range pageTexts {
		kids = append(kids, fmt.Sprintf("%d 0 R", 4+index*2))
	}
	objects[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(kids))
	objects[2] = "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"
	for index, text := range pageTexts {
		pageObject := 4 + index*2
		contentObject := pageObject + 1
		objects[pageObject-1] = fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", contentObject)
		content := ""
		if text != "" {
			content = "BT /F1 12 Tf 72 720 Td (" + strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)").Replace(text) + ") Tj ET"
		}
		objects[contentObject-1] = fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)
	}
	var document bytes.Buffer
	document.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for index, object := range objects {
		offsets[index+1] = document.Len()
		fmt.Fprintf(&document, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xref := document.Len()
	fmt.Fprintf(&document, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for index := 1; index < len(offsets); index++ {
		fmt.Fprintf(&document, "%010d 00000 n \n", offsets[index])
	}
	fmt.Fprintf(&document, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return document.Bytes()
}

func evidenceTestDOCX(text string) []byte {
	var payload bytes.Buffer
	archive := zip.NewWriter(&payload)
	parts := map[string]string{
		"[Content_Types].xml": `<Types><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
		"word/document.xml":   `<w:document xmlns:w="w"><w:body><w:p><w:r><w:t>` + text + `</w:t></w:r></w:p></w:body></w:document>`,
	}
	for name, value := range parts {
		entry, err := archive.Create(name)
		if err != nil {
			panic(err)
		}
		if _, err := entry.Write([]byte(value)); err != nil {
			panic(err)
		}
	}
	if err := archive.Close(); err != nil {
		panic(err)
	}
	return payload.Bytes()
}

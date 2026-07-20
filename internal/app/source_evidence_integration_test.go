package app_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	revision, _, err := evidence.NewPublisher(api.db.Pool(), objectstore.NewMemoryStore()).Publish(
		context.Background(), evidence.PublishCommand{
			RevisionID: "evr_evidence_pdf", JobID: lease.ID, LeaseToken: lease.LeaseToken, Artifact: artifact,
		})
	if err != nil {
		t.Fatal(err)
	}
	var kind string
	var page int
	if err := api.db.Pool().QueryRow(context.Background(), `
		select coordinate_json->>'kind', (coordinate_json->>'page')::integer
		from source_evidence_units where revision_id=$1
	`, revision.ID).Scan(&kind, &page); err != nil {
		t.Fatal(err)
	}
	if kind != "pdf_region" || page != 1 {
		t.Fatalf("coordinate kind=%q page=%d", kind, page)
	}
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

func evidenceTestPDF(text string) []byte {
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [4 0 R] /Count 1 >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents 5 0 R >>",
	}
	content := "BT /F1 12 Tf 72 720 Td (" + strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)").Replace(text) + ") Tj ET"
	objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
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

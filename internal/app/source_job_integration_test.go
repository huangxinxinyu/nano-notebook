package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/source"
	"github.com/huangxinxinyu/nano-notebook/internal/sourcejobs"
	"github.com/jackc/pgx/v5"
)

func TestSourceProcessingQueueFencesExpiredLease(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "source-job-lease@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-job-lease")
	ownerID := sourceTestUserID(t, api, "source-job-lease@example.com")

	err := api.db.WithRequestPrincipal(context.Background(), ownerID, func(tx pgx.Tx) error {
		created, err := source.NewStore(tx).CreateUploaded(context.Background(), source.CreateUploadedCommand{
			ID: "src_job_lease", NotebookID: notebookID, Title: "lease.txt", Format: source.FormatTXT,
			MediaType: "text/plain", ByteSize: 5, ContentSHA256: strings.Repeat("9", 64),
			OriginalObjectKey: "sources/src_job_lease/original/" + strings.Repeat("9", 64),
		})
		if err != nil {
			return err
		}
		_, err = tx.Exec(context.Background(), `
			insert into source_processing_jobs(id, source_id, notebook_id, status)
			values ('srcjob_lease', $1, $2, 'queued')
		`, created.ID, created.NotebookID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	queue := sourcejobs.NewQueue(api.db.Pool(), 30*time.Second)
	first, ok, err := queue.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("first Claim = %+v, ok=%v, err=%v", first, ok, err)
	}
	if first.SourceID != "src_job_lease" || first.AttemptNo != 1 || first.LeaseToken == "" {
		t.Fatalf("first lease = %+v", first)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `
		update source_processing_jobs set lease_expires_at=now()-interval '1 second' where id='srcjob_lease'
	`); err != nil {
		t.Fatal(err)
	}

	second, ok, err := queue.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("second Claim = %+v, ok=%v, err=%v", second, ok, err)
	}
	if second.AttemptNo != 2 || second.LeaseToken == first.LeaseToken {
		t.Fatalf("second lease = %+v, first = %+v", second, first)
	}
	if err := queue.Advance(context.Background(), first.ID, first.LeaseToken, source.StateUploaded, source.StateValidating); !errors.Is(err, sourcejobs.ErrLeaseLost) {
		t.Fatalf("stale Advance error = %v, want lease lost", err)
	}
	if err := queue.Advance(context.Background(), second.ID, second.LeaseToken, source.StateUploaded, source.StateValidating); err != nil {
		t.Fatalf("current Advance: %v", err)
	}

	var state source.State
	if err := api.db.Pool().QueryRow(context.Background(), `select state from source_sources where id='src_job_lease'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != source.StateValidating {
		t.Fatalf("Source state = %q, want validating", state)
	}
}

func TestSourceProcessingQueuePublishesTerminalStateAtomically(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "source-job-terminal@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-job-terminal")
	ownerID := sourceTestUserID(t, api, "source-job-terminal@example.com")
	seedSourceProcessingJob(t, api, ownerID, notebookID, "src_job_success", "srcjob_success", "7")
	seedSourceProcessingJob(t, api, ownerID, notebookID, "src_job_failure", "srcjob_failure", "8")

	queue := sourcejobs.NewQueue(api.db.Pool(), 30*time.Second)
	lease, ok, err := queue.Claim(context.Background())
	if err != nil || !ok || lease.SourceID != "src_job_success" {
		t.Fatalf("success Claim = %+v, ok=%v, err=%v", lease, ok, err)
	}
	renewed, err := queue.Renew(context.Background(), lease.ID, lease.LeaseToken)
	if err != nil || !renewed.After(lease.LeaseExpiresAt) {
		t.Fatalf("Renew expiry=%v, initial=%v, err=%v", renewed, lease.LeaseExpiresAt, err)
	}
	lease.LeaseExpiresAt = renewed
	transitions := [][2]source.State{
		{source.StateUploaded, source.StateValidating},
		{source.StateValidating, source.StateNormalizing},
		{source.StateNormalizing, source.StateSegmenting},
		{source.StateSegmenting, source.StateIndexing},
		{source.StateIndexing, source.StateVerifying},
	}
	for _, transition := range transitions {
		if err := queue.Advance(context.Background(), lease.ID, lease.LeaseToken, transition[0], transition[1]); err != nil {
			t.Fatalf("Advance %s -> %s: %v", transition[0], transition[1], err)
		}
	}
	if err := queue.Complete(context.Background(), lease.ID, lease.LeaseToken); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	assertSourceJobState(t, api, "src_job_success", "srcjob_success", source.StateReady, "succeeded", "")
	if err := queue.Complete(context.Background(), lease.ID, lease.LeaseToken); !errors.Is(err, sourcejobs.ErrLeaseLost) {
		t.Fatalf("repeated Complete error = %v, want lease lost", err)
	}

	failedLease, ok, err := queue.Claim(context.Background())
	if err != nil || !ok || failedLease.SourceID != "src_job_failure" {
		t.Fatalf("failure Claim = %+v, ok=%v, err=%v", failedLease, ok, err)
	}
	if err := queue.Fail(context.Background(), failedLease.ID, failedLease.LeaseToken, "unsupported_encoding"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	assertSourceJobState(t, api, "src_job_failure", "srcjob_failure", source.StateFailed, "failed", "unsupported_encoding")
}

func TestSourceProcessingQueueFailsAnExpiredFinalAttempt(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "source-job-exhausted@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-job-exhausted")
	ownerID := sourceTestUserID(t, api, "source-job-exhausted@example.com")
	seedSourceProcessingJob(t, api, ownerID, notebookID, "src_job_exhausted", "srcjob_exhausted", "6")
	if _, err := api.db.Pool().Exec(context.Background(), `
		update source_processing_jobs
		set status='running', attempt_no=3,
			lease_token='00000000-0000-4000-8000-000000000006',
			lease_expires_at=now()-interval '1 second'
		where id='srcjob_exhausted'
	`); err != nil {
		t.Fatal(err)
	}

	queue := sourcejobs.NewQueue(api.db.Pool(), 30*time.Second)
	lease, ok, err := queue.Claim(context.Background())
	if err != nil || ok {
		t.Fatalf("Claim after final expiry = %+v, ok=%v, err=%v", lease, ok, err)
	}
	assertSourceJobState(t, api, "src_job_exhausted", "srcjob_exhausted", source.StateFailed, "failed", "retry_exhausted")
}

func seedSourceProcessingJob(t *testing.T, api *testAPI, ownerID, notebookID, sourceID, jobID, hashDigit string) {
	t.Helper()
	err := api.db.WithRequestPrincipal(context.Background(), ownerID, func(tx pgx.Tx) error {
		created, err := source.NewStore(tx).CreateUploaded(context.Background(), source.CreateUploadedCommand{
			ID: sourceID, NotebookID: notebookID, Title: sourceID + ".txt", Format: source.FormatTXT,
			MediaType: "text/plain", ByteSize: 5, ContentSHA256: strings.Repeat(hashDigit, 64),
			OriginalObjectKey: "sources/" + sourceID + "/original/" + strings.Repeat(hashDigit, 64),
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
}

func assertSourceJobState(t *testing.T, api *testAPI, sourceID, jobID string, wantSource source.State, wantJob, wantError string) {
	t.Helper()
	var sourceState source.State
	var jobState string
	var errorCode *string
	if err := api.db.Pool().QueryRow(context.Background(), `
		select s.state, j.status, j.last_error_code
		from source_sources s join source_processing_jobs j on j.source_id=s.id
		where s.id=$1 and j.id=$2
	`, sourceID, jobID).Scan(&sourceState, &jobState, &errorCode); err != nil {
		t.Fatal(err)
	}
	gotError := ""
	if errorCode != nil {
		gotError = *errorCode
	}
	if sourceState != wantSource || jobState != wantJob || gotError != wantError {
		t.Fatalf("Source/Job state = %q/%q error=%q, want %q/%q error=%q", sourceState, jobState, gotError, wantSource, wantJob, wantError)
	}
}

func sourceTestUserID(t *testing.T, api *testAPI, email string) string {
	t.Helper()
	var id string
	if err := api.db.Pool().QueryRow(context.Background(), `select id from identity_users where canonical_email=$1`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/app"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/source"
	"github.com/jackc/pgx/v5"
)

func TestSourceStoreCreatesAndListsAuthorizedUploadedSource(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "source-owner@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-authority")

	var ownerID string
	if err := api.db.Pool().QueryRow(context.Background(), `
		select id from identity_users where canonical_email = 'source-owner@example.com'
	`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	var created source.Source
	err := api.db.WithRequestPrincipal(ctx, ownerID, func(tx pgx.Tx) error {
		var err error
		created, err = source.NewStore(tx).CreateUploaded(ctx, source.CreateUploadedCommand{
			ID:                "src_authorized",
			NotebookID:        notebookID,
			Title:             "Research notes.txt",
			Format:            source.FormatTXT,
			MediaType:         "text/plain",
			ByteSize:          42,
			ContentSHA256:     strings.Repeat("a", 64),
			OriginalObjectKey: "sources/src_authorized/original",
		})
		return err
	})
	if err != nil {
		t.Fatalf("CreateUploaded: %v", err)
	}
	if created.ID != "src_authorized" || created.NotebookID != notebookID || created.State != source.StateUploaded {
		t.Fatalf("created Source = %+v", created)
	}

	var listed []source.Source
	err = api.db.WithRequestPrincipal(ctx, ownerID, func(tx pgx.Tx) error {
		var err error
		listed, err = source.NewStore(tx).ListForNotebook(ctx, notebookID)
		return err
	})
	if err != nil {
		t.Fatalf("ListForNotebook: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != created.ID || listed[0].ContentSHA256 != created.ContentSHA256 {
		t.Fatalf("listed Sources = %+v, want created %+v", listed, created)
	}
}

func TestSourceStoreDoesNotDiscloseAnotherUsersNotebook(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "source-private-owner@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-private")
	api.register(t, "source-intruder@example.com")

	var intruderID string
	if err := api.db.Pool().QueryRow(context.Background(), `
		select id from identity_users where canonical_email = 'source-intruder@example.com'
	`).Scan(&intruderID); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	err := api.db.WithRequestPrincipal(ctx, intruderID, func(tx pgx.Tx) error {
		_, err := source.NewStore(tx).ListForNotebook(ctx, notebookID)
		return err
	})
	if !errors.Is(err, source.ErrNotFound) {
		t.Fatalf("ListForNotebook error = %v, want Source not found", err)
	}

	err = api.db.WithRequestPrincipal(ctx, intruderID, func(tx pgx.Tx) error {
		_, err := source.NewStore(tx).CreateUploaded(ctx, source.CreateUploadedCommand{
			ID: "src_intruder", NotebookID: notebookID, Title: "intruder.txt",
			Format: source.FormatTXT, MediaType: "text/plain", ByteSize: 1,
			ContentSHA256: strings.Repeat("b", 64), OriginalObjectKey: "sources/src_intruder/original",
		})
		return err
	})
	if !errors.Is(err, source.ErrNotFound) {
		t.Fatalf("CreateUploaded error = %v, want Source not found", err)
	}
}

func TestSourceStoreRejectsFileDuplicateOnlyWithinSameNotebook(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "source-duplicate@example.com")
	firstNotebookID := createSourceTestNotebook(t, api, owner, "source-duplicate-first")
	secondNotebookID := createSourceTestNotebook(t, api, owner, "source-duplicate-second")

	var ownerID string
	if err := api.db.Pool().QueryRow(context.Background(), `
		select id from identity_users where canonical_email = 'source-duplicate@example.com'
	`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	hash := strings.Repeat("c", 64)
	create := func(id, notebookID string) error {
		return api.db.WithRequestPrincipal(ctx, ownerID, func(tx pgx.Tx) error {
			_, err := source.NewStore(tx).CreateUploaded(ctx, source.CreateUploadedCommand{
				ID: id, NotebookID: notebookID, Title: id + ".txt", Format: source.FormatTXT,
				MediaType: "text/plain", ByteSize: 7, ContentSHA256: hash,
				OriginalObjectKey: "sources/" + id + "/original",
			})
			return err
		})
	}
	if err := create("src_original", firstNotebookID); err != nil {
		t.Fatalf("create original: %v", err)
	}

	err := create("src_duplicate", firstNotebookID)
	var duplicate *source.DuplicateError
	if !errors.As(err, &duplicate) || duplicate.ExistingSourceID != "src_original" {
		t.Fatalf("same-Notebook duplicate error = %#v, want existing src_original", err)
	}

	if err := create("src_other_notebook", secondNotebookID); err != nil {
		t.Fatalf("same hash in another Notebook: %v", err)
	}
}

func TestSourceStoreEnforcesFiftySourceNotebookQuota(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "source-quota@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-quota")

	var ownerID string
	if err := api.db.Pool().QueryRow(context.Background(), `
		select id from identity_users where canonical_email = 'source-quota@example.com'
	`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	for index := 0; index < 50; index++ {
		err := api.db.WithRequestPrincipal(ctx, ownerID, func(tx pgx.Tx) error {
			_, err := source.NewStore(tx).CreateUploaded(ctx, source.CreateUploadedCommand{
				ID: fmt.Sprintf("src_quota_%02d", index), NotebookID: notebookID,
				Title: fmt.Sprintf("quota-%02d.txt", index), Format: source.FormatTXT,
				MediaType: "text/plain", ByteSize: 1,
				ContentSHA256:     fmt.Sprintf("%064x", index),
				OriginalObjectKey: fmt.Sprintf("sources/src_quota_%02d/original", index),
			})
			return err
		})
		if err != nil {
			t.Fatalf("create Source %d: %v", index, err)
		}
	}

	err := api.db.WithRequestPrincipal(ctx, ownerID, func(tx pgx.Tx) error {
		_, err := source.NewStore(tx).CreateUploaded(ctx, source.CreateUploadedCommand{
			ID: "src_quota_overflow", NotebookID: notebookID, Title: "overflow.txt",
			Format: source.FormatTXT, MediaType: "text/plain", ByteSize: 1,
			ContentSHA256: strings.Repeat("f", 64), OriginalObjectKey: "sources/src_quota_overflow/original",
		})
		return err
	})
	if !errors.Is(err, source.ErrQuotaReached) {
		t.Fatalf("51st Source error = %v, want quota reached", err)
	}
}

func TestSourceStoreCreatesIdempotentUploadIntent(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "source-intent@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-intent")

	var ownerID string
	if err := api.db.Pool().QueryRow(context.Background(), `
		select id from identity_users where canonical_email = 'source-intent@example.com'
	`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	expiresAt := time.Now().UTC().Truncate(time.Microsecond).Add(15 * time.Minute)
	command := source.CreateUploadIntentCommand{
		ID: "upl_intent", SourceID: "src_intent", NotebookID: notebookID,
		IdempotencyKey: "upload-item-1", RequestHash: strings.Repeat("1", 64),
		Title: "intent.txt", Format: source.FormatTXT, MediaType: "text/plain",
		ByteSize: 12, ContentSHA256: strings.Repeat("d", 64),
		ObjectKey: "sources/src_intent/original", ExpiresAt: expiresAt,
	}
	var created source.UploadIntent
	var reused bool
	err := api.db.WithRequestPrincipal(ctx, ownerID, func(tx pgx.Tx) error {
		var err error
		created, reused, err = source.NewStore(tx).CreateUploadIntent(ctx, command)
		return err
	})
	if err != nil {
		t.Fatalf("CreateUploadIntent: %v", err)
	}
	if reused || created.ID != command.ID || created.SourceID != command.SourceID ||
		created.State != source.UploadIntentPending || !created.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("created upload intent = %+v, reused=%v", created, reused)
	}

	retry := command
	retry.ID = "upl_retry_ignored"
	retry.SourceID = "src_retry_ignored"
	var retried source.UploadIntent
	err = api.db.WithRequestPrincipal(ctx, ownerID, func(tx pgx.Tx) error {
		var err error
		retried, reused, err = source.NewStore(tx).CreateUploadIntent(ctx, retry)
		return err
	})
	if err != nil {
		t.Fatalf("retry CreateUploadIntent: %v", err)
	}
	if !reused || retried.ID != created.ID || retried.SourceID != created.SourceID {
		t.Fatalf("retried upload intent = %+v, reused=%v; want %+v", retried, reused, created)
	}
}

func TestSourceStoreRenamesRetriesAndCreatesDurablePurgeIntent(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "source-lifecycle@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-lifecycle")
	ownerID := sourceTestUserID(t, api, "source-lifecycle@example.com")
	seedSourceProcessingJob(t, api, ownerID, notebookID, "src_lifecycle", "srcjob_lifecycle", "5")
	if _, err := api.db.Pool().Exec(context.Background(), `
		update source_sources set state='failed' where id='src_lifecycle';
		update source_processing_jobs set status='failed', last_error_code='invalid_text' where id='srcjob_lifecycle'
	`); err != nil {
		t.Fatal(err)
	}

	var renamed source.Source
	err := api.db.WithRequestPrincipal(context.Background(), ownerID, func(tx pgx.Tx) error {
		var err error
		renamed, err = source.NewStore(tx).Rename(context.Background(), "src_lifecycle", "Renamed evidence")
		return err
	})
	if err != nil || renamed.Title != "Renamed evidence" || renamed.State != source.StateFailed {
		t.Fatalf("Rename = %+v, err=%v", renamed, err)
	}

	err = api.db.WithRequestPrincipal(context.Background(), ownerID, func(tx pgx.Tx) error {
		return source.NewStore(tx).RetryFailed(context.Background(), "src_lifecycle")
	})
	if err != nil {
		t.Fatalf("RetryFailed: %v", err)
	}
	assertSourceJobState(t, api, "src_lifecycle", "srcjob_lifecycle", source.StateUploaded, "queued", "")

	var purge source.PurgeIntent
	err = api.db.WithRequestPrincipal(context.Background(), ownerID, func(tx pgx.Tx) error {
		var err error
		purge, err = source.NewStore(tx).Remove(context.Background(), "src_lifecycle", "srcpurge_lifecycle")
		return err
	})
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if purge.ID != "srcpurge_lifecycle" || purge.SourceID != "src_lifecycle" ||
		purge.OriginalObjectKey != "sources/src_lifecycle/original/"+strings.Repeat("5", 64) || purge.State != source.PurgePending {
		t.Fatalf("purge intent = %+v", purge)
	}
	var sourceCount, purgeCount int
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from source_sources where id='src_lifecycle'`).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from source_purge_jobs where id='srcpurge_lifecycle' and state='pending'`).Scan(&purgeCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 0 || purgeCount != 1 {
		t.Fatalf("after Remove sources=%d purge jobs=%d", sourceCount, purgeCount)
	}
}

func TestCreateSourceUploadIntentReturnsDirectUploadPolicyWithoutCreatingSource(t *testing.T) {
	api := newTestAPI(t)
	owner, csrf := api.registerWithCSRF(t, "source-upload-api@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-upload-api")
	uploads := &recordingSourceUploads{}
	api.server = app.NewServer(app.Config{CookieSecure: false, SourceUploads: uploads}, api.db)
	api.handler = api.server.Handler()

	response := api.postJSONWithCookieAndCSRF(t,
		"/api/v1/notebooks/"+notebookID+"/sources/upload-intents",
		map[string]any{
			"title": "API notes.txt", "format": "txt", "media_type": "text/plain",
			"byte_size": 12, "content_sha256": strings.Repeat("e", 64),
		}, owner, csrf, csrf.Value, "upload-api-item-1",
	)
	if response.Code != http.StatusCreated {
		t.Fatalf("create upload intent status = %d, body = %s", response.Code, response.Body.String())
	}
	var body struct {
		Intent source.UploadIntent      `json:"upload_intent"`
		Upload objectstore.UploadPolicy `json:"upload"`
	}
	decodeBody(t, response, &body)
	if body.Intent.ID == "" || body.Intent.SourceID == "" || body.Intent.State != source.UploadIntentPending {
		t.Fatalf("upload intent response = %+v", body.Intent)
	}
	if body.Upload.Method != http.MethodPost || body.Upload.URL != "https://uploads.test/direct" ||
		body.Upload.Fields["key"] != uploads.request.Key {
		t.Fatalf("upload policy response = %+v, recorded request = %+v", body.Upload, uploads.request)
	}
	if uploads.request.ByteSize != 12 || uploads.request.MediaType != "text/plain" ||
		uploads.request.ContentSHA256 != strings.Repeat("e", 64) || uploads.request.Key == "" {
		t.Fatalf("presign request = %+v", uploads.request)
	}
	var sourceCount int
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from source_sources`).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 0 {
		t.Fatalf("Source count before finalize = %d, want 0", sourceCount)
	}
}

func TestSourceOwnerAPIListsRenamesRetriesAndRemoves(t *testing.T) {
	api := newTestAPI(t)
	owner, csrf := api.registerWithCSRF(t, "source-owner-api@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-owner-api")
	ownerID := sourceTestUserID(t, api, "source-owner-api@example.com")
	seedSourceProcessingJob(t, api, ownerID, notebookID, "src_owner_api", "srcjob_owner_api", "4")
	intruder, intruderCSRF := api.registerWithCSRF(t, "source-owner-api-intruder@example.com")
	privateList := api.getWithCookie(t, "/api/v1/notebooks/"+notebookID+"/sources", intruder)
	if privateList.Code != http.StatusNotFound {
		t.Fatalf("intruder list status=%d body=%s", privateList.Code, privateList.Body.String())
	}
	privateRename := sourceAPIRequest(t, api, http.MethodPatch, "/api/v1/sources/src_owner_api", map[string]any{"title": "Stolen"}, intruder, intruderCSRF)
	if privateRename.Code != http.StatusNotFound {
		t.Fatalf("intruder rename status=%d body=%s", privateRename.Code, privateRename.Body.String())
	}

	listed := api.getWithCookie(t, "/api/v1/notebooks/"+notebookID+"/sources", owner)
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), "content_sha256") {
		t.Fatalf("list Sources status=%d body=%s", listed.Code, listed.Body.String())
	}
	var listBody struct {
		Sources []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			State string `json:"state"`
		} `json:"sources"`
	}
	decodeBody(t, listed, &listBody)
	if len(listBody.Sources) != 1 || listBody.Sources[0].ID != "src_owner_api" || listBody.Sources[0].State != "processing" {
		t.Fatalf("listed Sources = %+v", listBody.Sources)
	}

	renamed := sourceAPIRequest(t, api, http.MethodPatch, "/api/v1/sources/src_owner_api", map[string]any{"title": "API renamed"}, owner, csrf)
	if renamed.Code != http.StatusOK || !strings.Contains(renamed.Body.String(), "API renamed") {
		t.Fatalf("rename status=%d body=%s", renamed.Code, renamed.Body.String())
	}
	if _, err := api.db.Pool().Exec(context.Background(), `
		update source_sources set state='failed' where id='src_owner_api';
		update source_processing_jobs set status='failed', last_error_code='invalid_text' where id='srcjob_owner_api'
	`); err != nil {
		t.Fatal(err)
	}
	retried := sourceAPIRequest(t, api, http.MethodPost, "/api/v1/sources/src_owner_api/retry", map[string]any{}, owner, csrf)
	if retried.Code != http.StatusAccepted {
		t.Fatalf("retry status=%d body=%s", retried.Code, retried.Body.String())
	}
	assertSourceJobState(t, api, "src_owner_api", "srcjob_owner_api", source.StateUploaded, "queued", "")

	removed := sourceAPIRequest(t, api, http.MethodDelete, "/api/v1/sources/src_owner_api", nil, owner, csrf)
	if removed.Code != http.StatusNoContent {
		t.Fatalf("remove status=%d body=%s", removed.Code, removed.Body.String())
	}
	var sources, purges int
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from source_sources where id='src_owner_api'`).Scan(&sources); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from source_purge_jobs where source_id='src_owner_api' and state='pending'`).Scan(&purges); err != nil {
		t.Fatal(err)
	}
	if sources != 0 || purges != 1 {
		t.Fatalf("after API remove sources=%d purges=%d", sources, purges)
	}
}

func sourceAPIRequest(t *testing.T, api *testAPI, method, path string, payload map[string]any, session, csrf *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	body := []byte(nil)
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf.Value)
	request.AddCookie(session)
	request.AddCookie(csrf)
	response := httptest.NewRecorder()
	api.handler.ServeHTTP(response, request)
	return response
}

func TestFinalizeSourceUploadValidatesObjectAndAtomicallyQueuesProcessing(t *testing.T) {
	api := newTestAPI(t)
	owner, csrf := api.registerWithCSRF(t, "source-finalize-api@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-finalize-api")
	uploads := &recordingSourceUploads{}
	api.server = app.NewServer(app.Config{CookieSecure: false, SourceUploads: uploads}, api.db)
	api.handler = api.server.Handler()

	created := api.postJSONWithCookieAndCSRF(t,
		"/api/v1/notebooks/"+notebookID+"/sources/upload-intents",
		map[string]any{
			"title": "Finalize notes.txt", "format": "txt", "media_type": "text/plain",
			"byte_size": 12, "content_sha256": strings.Repeat("c", 64),
		}, owner, csrf, csrf.Value, "upload-finalize-item-1",
	)
	if created.Code != http.StatusCreated {
		t.Fatalf("create upload intent status = %d, body = %s", created.Code, created.Body.String())
	}
	var uploadBody struct {
		Intent source.UploadIntent `json:"upload_intent"`
	}
	decodeBody(t, created, &uploadBody)
	uploads.objectInfo = objectstore.ObjectInfo{Key: uploads.request.Key, Size: 12, ModifiedAt: time.Now().UTC()}

	finalized := api.postJSONWithCookieAndCSRF(t,
		"/api/v1/source-upload-intents/"+uploadBody.Intent.ID+"/finalize",
		map[string]any{}, owner, csrf, csrf.Value, "",
	)
	if finalized.Code != http.StatusCreated {
		t.Fatalf("finalize upload status = %d, body = %s", finalized.Code, finalized.Body.String())
	}
	var finalizedBody struct {
		Source source.Source `json:"source"`
	}
	decodeBody(t, finalized, &finalizedBody)
	if finalizedBody.Source.ID != uploadBody.Intent.SourceID || finalizedBody.Source.State != source.StateUploaded {
		t.Fatalf("finalized Source = %+v, intent = %+v", finalizedBody.Source, uploadBody.Intent)
	}
	if uploads.validationRequest != uploads.request {
		t.Fatalf("validation request = %+v, want signed request %+v", uploads.validationRequest, uploads.request)
	}
	wantOriginalKey := "sources/" + finalizedBody.Source.ID + "/original/" + strings.Repeat("c", 64)
	if uploads.promotionRequest != uploads.request || uploads.promotionDestination != wantOriginalKey {
		t.Fatalf("promotion request = %+v destination=%q, want request %+v destination=%q", uploads.promotionRequest, uploads.promotionDestination, uploads.request, wantOriginalKey)
	}

	var intentState source.UploadIntentState
	var queuedJobs int
	var originalKey string
	if err := api.db.Pool().QueryRow(context.Background(), `select state from source_upload_intents where id=$1`, uploadBody.Intent.ID).Scan(&intentState); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from source_processing_jobs where source_id=$1 and status='queued'`, finalizedBody.Source.ID).Scan(&queuedJobs); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(context.Background(), `select original_object_key from source_sources where id=$1`, finalizedBody.Source.ID).Scan(&originalKey); err != nil {
		t.Fatal(err)
	}
	if intentState != source.UploadIntentFinalized || queuedJobs != 1 || originalKey != wantOriginalKey {
		t.Fatalf("finalize authority: intent state=%q queued jobs=%d original key=%q", intentState, queuedJobs, originalKey)
	}

	retried := api.postJSONWithCookieAndCSRF(t,
		"/api/v1/source-upload-intents/"+uploadBody.Intent.ID+"/finalize",
		map[string]any{}, owner, csrf, csrf.Value, "",
	)
	if retried.Code != http.StatusOK {
		t.Fatalf("retry finalize status = %d, body = %s", retried.Code, retried.Body.String())
	}
	var retriedBody struct {
		Source source.Source `json:"source"`
	}
	decodeBody(t, retried, &retriedBody)
	if retriedBody.Source.ID != finalizedBody.Source.ID {
		t.Fatalf("retry Source = %+v, want %+v", retriedBody.Source, finalizedBody.Source)
	}
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from source_processing_jobs where source_id=$1`, finalizedBody.Source.ID).Scan(&queuedJobs); err != nil {
		t.Fatal(err)
	}
	if queuedJobs != 1 {
		t.Fatalf("jobs after retry = %d, want 1", queuedJobs)
	}
}

func TestFinalizeSourceUploadRejectsMismatchedObjectWithoutAuthorityRows(t *testing.T) {
	api := newTestAPI(t)
	owner, csrf := api.registerWithCSRF(t, "source-finalize-mismatch@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-finalize-mismatch")
	uploads := &recordingSourceUploads{validationErr: fmt.Errorf("checksum differs: %w", objectstore.ErrUploadMismatch)}
	api.server = app.NewServer(app.Config{CookieSecure: false, SourceUploads: uploads}, api.db)
	api.handler = api.server.Handler()

	created := api.postJSONWithCookieAndCSRF(t,
		"/api/v1/notebooks/"+notebookID+"/sources/upload-intents",
		map[string]any{
			"title": "Mismatch.txt", "format": "txt", "media_type": "text/plain",
			"byte_size": 8, "content_sha256": strings.Repeat("b", 64),
		}, owner, csrf, csrf.Value, "upload-mismatch-item-1",
	)
	var uploadBody struct {
		Intent source.UploadIntent `json:"upload_intent"`
	}
	decodeBody(t, created, &uploadBody)

	finalized := api.postJSONWithCookieAndCSRF(t,
		"/api/v1/source-upload-intents/"+uploadBody.Intent.ID+"/finalize",
		map[string]any{}, owner, csrf, csrf.Value, "",
	)
	if finalized.Code != http.StatusConflict || decodeError(t, finalized).Code != "source_upload_mismatch" {
		t.Fatalf("mismatched finalize status = %d, body = %s", finalized.Code, finalized.Body.String())
	}
	var sourceCount, jobCount int
	var intentState source.UploadIntentState
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from source_sources`).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from source_processing_jobs`).Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(context.Background(), `select state from source_upload_intents where id=$1`, uploadBody.Intent.ID).Scan(&intentState); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 0 || jobCount != 0 || intentState != source.UploadIntentPending {
		t.Fatalf("rejected finalize left sources=%d jobs=%d intent=%q", sourceCount, jobCount, intentState)
	}
}

type recordingSourceUploads struct {
	request              objectstore.UploadPolicyRequest
	validationRequest    objectstore.UploadPolicyRequest
	promotionRequest     objectstore.UploadPolicyRequest
	promotionDestination string
	objectInfo           objectstore.ObjectInfo
	validationErr        error
}

func (s *recordingSourceUploads) PresignUpload(_ context.Context, request objectstore.UploadPolicyRequest) (objectstore.UploadPolicy, error) {
	s.request = request
	return objectstore.UploadPolicy{
		Method: http.MethodPost, URL: "https://uploads.test/direct",
		Fields: map[string]string{"key": request.Key}, ExpiresAt: request.ExpiresAt,
	}, nil
}

func (s *recordingSourceUploads) ValidateUpload(_ context.Context, request objectstore.UploadPolicyRequest) (objectstore.ObjectInfo, error) {
	s.validationRequest = request
	return s.objectInfo, s.validationErr
}

func (s *recordingSourceUploads) PromoteUpload(_ context.Context, request objectstore.UploadPolicyRequest, destinationKey string) (objectstore.ObjectInfo, error) {
	s.validationRequest = request
	s.promotionRequest = request
	s.promotionDestination = destinationKey
	if s.validationErr != nil {
		return objectstore.ObjectInfo{}, s.validationErr
	}
	return objectstore.ObjectInfo{Key: destinationKey, Size: request.ByteSize, ModifiedAt: time.Now().UTC()}, nil
}

func createSourceTestNotebook(t *testing.T, api *testAPI, owner *http.Cookie, key string) string {
	t.Helper()
	response := api.postJSONWithCookie(t, "/api/v1/notebooks", map[string]any{"title": "Source Test"}, owner, key)
	if response.Code != http.StatusCreated {
		t.Fatalf("create Notebook status = %d, body = %s", response.Code, response.Body.String())
	}
	var body struct {
		Notebook struct {
			ID string `json:"id"`
		} `json:"notebook"`
	}
	decodeBody(t, response, &body)
	return body.Notebook.ID
}

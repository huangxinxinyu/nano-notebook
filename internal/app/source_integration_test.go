package app_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

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

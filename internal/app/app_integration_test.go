package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/app"
)

func TestPrimaryJourneyPersistsOwnedNotebookAndRevokesSession(t *testing.T) {
	api := newTestAPI(t)

	register := api.postJSON(t, "/api/v1/auth/register", map[string]any{
		"email":    " Researcher@Example.com ",
		"password": validTestPassword,
	}, "")
	if register.Code != http.StatusCreated {
		t.Fatalf("register status = %d, body = %s", register.Code, register.Body.String())
	}
	cookie := register.Result().Cookies()[0]

	create := api.postJSONWithCookie(t, "/api/v1/notebooks", map[string]any{"title": "Durable Research Notes"}, cookie, "create-one")
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", create.Code, create.Body.String())
	}
	var created struct {
		Notebook struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"notebook"`
	}
	decodeBody(t, create, &created)
	if created.Notebook.ID == "" || created.Notebook.Title != "Durable Research Notes" {
		t.Fatalf("unexpected notebook payload: %+v", created.Notebook)
	}

	retry := api.postJSONWithCookie(t, "/api/v1/notebooks", map[string]any{"title": "Durable Research Notes"}, cookie, "create-one")
	if retry.Code != http.StatusOK {
		t.Fatalf("idempotent retry status = %d, body = %s", retry.Code, retry.Body.String())
	}
	var retried struct {
		Notebook struct {
			ID string `json:"id"`
		} `json:"notebook"`
	}
	decodeBody(t, retry, &retried)
	if retried.Notebook.ID != created.Notebook.ID {
		t.Fatalf("idempotent retry returned %q, want %q", retried.Notebook.ID, created.Notebook.ID)
	}

	mismatch := api.postJSONWithCookie(t, "/api/v1/notebooks", map[string]any{"title": "Different"}, cookie, "create-one")
	if mismatch.Code != http.StatusConflict {
		t.Fatalf("idempotency mismatch status = %d, body = %s", mismatch.Code, mismatch.Body.String())
	}

	list := api.getWithCookie(t, "/api/v1/notebooks?query=research", cookie)
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", list.Code, list.Body.String())
	}
	var listed struct {
		Notebooks []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"notebooks"`
	}
	decodeBody(t, list, &listed)
	if len(listed.Notebooks) != 1 || listed.Notebooks[0].ID != created.Notebook.ID {
		t.Fatalf("search results = %+v", listed.Notebooks)
	}

	signOut := api.postJSONWithCookie(t, "/api/v1/auth/sign-out", map[string]any{}, cookie, "")
	if signOut.Code != http.StatusNoContent {
		t.Fatalf("sign out status = %d, body = %s", signOut.Code, signOut.Body.String())
	}
	afterSignOut := api.getWithCookie(t, "/api/v1/session", cookie)
	if afterSignOut.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session status = %d, body = %s", afterSignOut.Code, afterSignOut.Body.String())
	}
}

func TestNotebookAccessDoesNotLeakAcrossUsers(t *testing.T) {
	api := newTestAPI(t)

	owner := api.register(t, "owner@example.com")
	intruder := api.register(t, "intruder@example.com")
	create := api.postJSONWithCookie(t, "/api/v1/notebooks", map[string]any{"title": "Private Notebook"}, owner, "owner-create")
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", create.Code, create.Body.String())
	}
	var created struct {
		Notebook struct {
			ID string `json:"id"`
		} `json:"notebook"`
	}
	decodeBody(t, create, &created)

	crossUser := api.getWithCookie(t, "/api/v1/notebooks/"+created.Notebook.ID, intruder)
	if crossUser.Code != http.StatusNotFound {
		t.Fatalf("cross-user status = %d, body = %s", crossUser.Code, crossUser.Body.String())
	}
	missing := api.getWithCookie(t, "/api/v1/notebooks/nb_missing", intruder)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d, body = %s", missing.Code, missing.Body.String())
	}
	crossUserErr := decodeError(t, crossUser)
	missingErr := decodeError(t, missing)
	if crossUserErr.Code != missingErr.Code || crossUserErr.MessageKey != missingErr.MessageKey {
		t.Fatalf("inaccessible and missing responses differ: %+v %+v", crossUserErr, missingErr)
	}
}

func TestPasswordPolicyAndRateLimit(t *testing.T) {
	api := newTestAPI(t)

	weak := api.postJSON(t, "/api/v1/auth/register", map[string]any{
		"email":    "weak@example.com",
		"password": "password",
	}, "")
	if weak.Code != http.StatusBadRequest {
		t.Fatalf("weak password status = %d, body = %s", weak.Code, weak.Body.String())
	}

	api.register(t, "rate@example.com")
	for i := 0; i < 5; i++ {
		resp := api.postJSON(t, "/api/v1/auth/sign-in", map[string]any{
			"email":    "rate@example.com",
			"password": "wrong wrong wrong",
		}, "")
		if i < 4 && resp.Code != http.StatusUnauthorized {
			t.Fatalf("bad credentials attempt %d status = %d", i+1, resp.Code)
		}
	}
	limited := api.postJSON(t, "/api/v1/auth/sign-in", map[string]any{
		"email":    "rate@example.com",
		"password": "wrong wrong wrong",
	}, "")
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("rate limit status = %d, body = %s", limited.Code, limited.Body.String())
	}
}

type testAPI struct {
	handler http.Handler
	db      *app.DB
}

func newTestAPI(t *testing.T) *testAPI {
	t.Helper()
	dsn := os.Getenv("NANO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("NANO_TEST_DATABASE_URL is required for real PostgreSQL integration tests")
	}
	ctx := context.Background()
	db, err := app.OpenDB(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := app.ResetForTests(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := app.RunMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	server := app.NewServer(app.Config{CookieSecure: false}, db)
	return &testAPI{handler: server.Handler(), db: db}
}

func (api *testAPI) register(t *testing.T, email string) *http.Cookie {
	t.Helper()
	resp := api.postJSON(t, "/api/v1/auth/register", map[string]any{
		"email":    email,
		"password": validTestPassword,
	}, "")
	if resp.Code != http.StatusCreated {
		t.Fatalf("register %s status = %d, body = %s", email, resp.Code, resp.Body.String())
	}
	return resp.Result().Cookies()[0]
}

func (api *testAPI) postJSON(t *testing.T, path string, payload map[string]any, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()
	return api.postJSONWithCookie(t, path, payload, nil, idempotencyKey)
}

func (api *testAPI) postJSONWithCookie(t *testing.T, path string, payload map[string]any, cookie *http.Cookie, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", "test-csrf-token")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	api.handler.ServeHTTP(rec, req)
	return rec
}

func (api *testAPI) getWithCookie(t *testing.T, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	api.handler.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), target); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

const validTestPassword = "unique local sprint phrase 2026"

type errorEnvelope struct {
	Error struct {
		Code       string `json:"code"`
		MessageKey string `json:"message_key"`
	} `json:"error"`
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) struct {
	Code       string
	MessageKey string
} {
	t.Helper()
	var envelope errorEnvelope
	decodeBody(t, rec, &envelope)
	return struct {
		Code       string
		MessageKey string
	}{Code: envelope.Error.Code, MessageKey: envelope.Error.MessageKey}
}

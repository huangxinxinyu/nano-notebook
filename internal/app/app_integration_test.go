package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
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

func TestCookieMutationsRequireMatchingCSRFCookieAndHeader(t *testing.T) {
	api := newTestAPI(t)
	sessionCookie, csrfCookie := api.registerWithCSRF(t, "csrf@example.com")

	missingCreate := api.postJSONWithCookieAndCSRF(t, "/api/v1/notebooks", map[string]any{"title": "Blocked Create"}, sessionCookie, nil, "", "csrf-missing")
	if missingCreate.Code != http.StatusForbidden {
		t.Fatalf("missing create csrf status = %d, body = %s", missingCreate.Code, missingCreate.Body.String())
	}
	badCreate := api.postJSONWithCookieAndCSRF(t, "/api/v1/notebooks", map[string]any{"title": "Blocked Create"}, sessionCookie, csrfCookie, "wrong-token", "csrf-bad")
	if badCreate.Code != http.StatusForbidden {
		t.Fatalf("bad create csrf status = %d, body = %s", badCreate.Code, badCreate.Body.String())
	}
	okCreate := api.postJSONWithCookieAndCSRF(t, "/api/v1/notebooks", map[string]any{"title": "Allowed Create"}, sessionCookie, csrfCookie, csrfCookie.Value, "csrf-ok")
	if okCreate.Code != http.StatusCreated {
		t.Fatalf("matching create csrf status = %d, body = %s", okCreate.Code, okCreate.Body.String())
	}

	missingSignOut := api.postJSONWithCookieAndCSRF(t, "/api/v1/auth/sign-out", map[string]any{}, sessionCookie, nil, "", "")
	if missingSignOut.Code != http.StatusForbidden {
		t.Fatalf("missing sign-out csrf status = %d, body = %s", missingSignOut.Code, missingSignOut.Body.String())
	}
	badSignOut := api.postJSONWithCookieAndCSRF(t, "/api/v1/auth/sign-out", map[string]any{}, sessionCookie, csrfCookie, "wrong-token", "")
	if badSignOut.Code != http.StatusForbidden {
		t.Fatalf("bad sign-out csrf status = %d, body = %s", badSignOut.Code, badSignOut.Body.String())
	}
	okSignOut := api.postJSONWithCookieAndCSRF(t, "/api/v1/auth/sign-out", map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if okSignOut.Code != http.StatusNoContent {
		t.Fatalf("matching sign-out csrf status = %d, body = %s", okSignOut.Code, okSignOut.Body.String())
	}
}

func TestConcurrentNotebookQuotaDoesNotExceedOneHundred(t *testing.T) {
	api := newTestAPI(t)
	sessionCookie, csrfCookie := api.registerWithCSRF(t, "quota@example.com")
	for i := 0; i < 99; i++ {
		resp := api.postJSONWithCookieAndCSRF(t, "/api/v1/notebooks", map[string]any{"title": "Seed"}, sessionCookie, csrfCookie, csrfCookie.Value, fmt.Sprintf("quota-seed-%d", i+1))
		if resp.Code != http.StatusCreated {
			t.Fatalf("seed notebook %d status = %d, body = %s", i, resp.Code, resp.Body.String())
		}
	}

	start := make(chan struct{})
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			resp := api.postJSONWithCookieAndCSRF(t, "/api/v1/notebooks", map[string]any{"title": "Concurrent Quota"}, sessionCookie, csrfCookie, csrfCookie.Value, fmt.Sprintf("quota-race-%d", i+1))
			statuses <- resp.Code
		}(i)
	}
	close(start)
	wg.Wait()
	close(statuses)

	created := 0
	quota := 0
	for status := range statuses {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			quota++
		default:
			t.Fatalf("unexpected concurrent quota status = %d", status)
		}
	}
	if created != 1 || quota != 1 {
		t.Fatalf("concurrent quota statuses created=%d quota=%d, want 1/1", created, quota)
	}

	count := api.ownedNotebookCount(t, sessionCookie)
	if count != 100 {
		t.Fatalf("owned notebook count = %d, want 100", count)
	}
}

func TestConcurrentIdempotentCreateConvergesOnSameNotebook(t *testing.T) {
	api := newTestAPI(t)
	sessionCookie, csrfCookie := api.registerWithCSRF(t, "idempotent@example.com")

	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- api.postJSONWithCookieAndCSRF(t, "/api/v1/notebooks", map[string]any{"title": "Race Safe"}, sessionCookie, csrfCookie, csrfCookie.Value, "same-key")
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	ids := map[string]bool{}
	for resp := range results {
		if resp.Code != http.StatusCreated && resp.Code != http.StatusOK {
			t.Fatalf("idempotent race status = %d, body = %s", resp.Code, resp.Body.String())
		}
		var body struct {
			Notebook struct {
				ID string `json:"id"`
			} `json:"notebook"`
		}
		decodeBody(t, resp, &body)
		ids[body.Notebook.ID] = true
	}
	if len(ids) != 1 {
		t.Fatalf("idempotent concurrent create returned different notebooks: %+v", ids)
	}
}

func TestMigrationsReapplyAndInstallRLSBoundary(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	if err := app.RunMigrations(ctx, api.db); err != nil {
		t.Fatalf("second migration run failed: %v", err)
	}
	sessionCookie, csrfCookie := api.registerWithCSRF(t, "rls@example.com")
	create := api.postJSONWithCookieAndCSRF(t, "/api/v1/notebooks", map[string]any{"title": "RLS Notebook"}, sessionCookie, csrfCookie, csrfCookie.Value, "rls-create")
	if create.Code != http.StatusCreated {
		t.Fatalf("create for rls status = %d, body = %s", create.Code, create.Body.String())
	}
	session := api.getWithCookie(t, "/api/v1/session", sessionCookie)
	var sessionBody struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	decodeBody(t, session, &sessionBody)

	var policies int
	err := api.db.Pool().QueryRow(ctx, `
		select count(*)
		from pg_policies
		where schemaname = 'public'
		  and tablename in ('identity_users', 'identity_sessions', 'notebook_notebooks', 'notebook_memberships')
	`).Scan(&policies)
	if err != nil {
		t.Fatal(err)
	}
	if policies < 4 {
		t.Fatalf("RLS policy count = %d, want at least 4", policies)
	}

	tx, err := api.db.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `set local role nano_app`); err != nil {
		t.Fatal(err)
	}
	var visibleWithoutPrincipal int
	if err := tx.QueryRow(ctx, `select count(*) from notebook_notebooks`).Scan(&visibleWithoutPrincipal); err != nil {
		t.Fatal(err)
	}
	if visibleWithoutPrincipal != 0 {
		t.Fatalf("visible notebooks without principal = %d, want 0", visibleWithoutPrincipal)
	}
	if _, err := tx.Exec(ctx, `select set_config('app.principal_id', $1, true)`, sessionBody.User.ID); err != nil {
		t.Fatal(err)
	}
	var visibleWithPrincipal int
	if err := tx.QueryRow(ctx, `select count(*) from notebook_notebooks`).Scan(&visibleWithPrincipal); err != nil {
		t.Fatal(err)
	}
	if visibleWithPrincipal != 1 {
		t.Fatalf("visible notebooks with owner principal = %d, want 1", visibleWithPrincipal)
	}
}

type testAPI struct {
	handler       http.Handler
	db            *app.DB
	csrfBySession map[string]*http.Cookie
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
	return &testAPI{handler: server.Handler(), db: db, csrfBySession: map[string]*http.Cookie{}}
}

func (api *testAPI) register(t *testing.T, email string) *http.Cookie {
	t.Helper()
	sessionCookie, _ := api.registerWithCSRF(t, email)
	return sessionCookie
}

func (api *testAPI) registerWithCSRF(t *testing.T, email string) (*http.Cookie, *http.Cookie) {
	t.Helper()
	resp := api.postJSON(t, "/api/v1/auth/register", map[string]any{
		"email":    email,
		"password": validTestPassword,
	}, "")
	if resp.Code != http.StatusCreated {
		t.Fatalf("register %s status = %d, body = %s", email, resp.Code, resp.Body.String())
	}
	sessionCookie := cookieNamed(t, resp, "nn_session")
	csrfCookie := cookieNamed(t, resp, "nn_csrf")
	api.csrfBySession[sessionCookie.Value] = csrfCookie
	return sessionCookie, csrfCookie
}

func (api *testAPI) postJSON(t *testing.T, path string, payload map[string]any, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()
	return api.postJSONWithCookie(t, path, payload, nil, idempotencyKey)
}

func (api *testAPI) postJSONWithCookie(t *testing.T, path string, payload map[string]any, cookie *http.Cookie, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()
	var csrfCookie *http.Cookie
	csrfHeader := "test-csrf-token"
	if cookie != nil {
		csrfCookie = api.csrfBySession[cookie.Value]
		if csrfCookie == nil {
			csrfCookie = &http.Cookie{Name: "nn_csrf", Value: "test-csrf-token"}
		}
		csrfHeader = csrfCookie.Value
	}
	return api.postJSONWithCookieAndCSRF(t, path, payload, cookie, csrfCookie, csrfHeader, idempotencyKey)
}

func (api *testAPI) postJSONWithCookieAndCSRF(t *testing.T, path string, payload map[string]any, cookie *http.Cookie, csrfCookie *http.Cookie, csrfHeader string, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if csrfHeader != "" {
		req.Header.Set("X-CSRF-Token", csrfHeader)
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if csrfCookie != nil {
		req.AddCookie(csrfCookie)
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

func cookieNamed(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("missing cookie %q in %+v", name, rec.Result().Cookies())
	return nil
}

func (api *testAPI) ownedNotebookCount(t *testing.T, cookie *http.Cookie) int {
	t.Helper()
	list := api.getWithCookie(t, "/api/v1/notebooks", cookie)
	if list.Code != http.StatusOK {
		t.Fatalf("list for count status = %d, body = %s", list.Code, list.Body.String())
	}
	var body struct {
		Notebooks []struct {
			ID string `json:"id"`
		} `json:"notebooks"`
	}
	decodeBody(t, list, &body)
	return len(body.Notebooks)
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

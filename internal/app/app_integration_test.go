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
	"time"

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

func TestNotebookCreateIdempotencyUsesNormalizedRequest(t *testing.T) {
	api := newTestAPI(t)
	sessionCookie, csrfCookie := api.registerWithCSRF(t, "canonical-idempotency@example.com")

	create := api.postRawJSONWithCookieAndCSRF(t, "/api/v1/notebooks", `{"title":"Retry Notes"}`, sessionCookie, csrfCookie, csrfCookie.Value, "canonical-create")
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
	if created.Notebook.Title != "Retry Notes" {
		t.Fatalf("created title = %q, want normalized Retry Notes", created.Notebook.Title)
	}

	retry := api.postRawJSONWithCookieAndCSRF(t, "/api/v1/notebooks", `{"title": "  Retry Notes  "}`, sessionCookie, csrfCookie, csrfCookie.Value, "canonical-create")
	if retry.Code != http.StatusOK {
		t.Fatalf("canonical retry status = %d, body = %s", retry.Code, retry.Body.String())
	}
	var retried struct {
		Notebook struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"notebook"`
	}
	decodeBody(t, retry, &retried)
	if retried.Notebook.ID != created.Notebook.ID || retried.Notebook.Title != "Retry Notes" {
		t.Fatalf("canonical retry returned %+v, want original %+v", retried.Notebook, created.Notebook)
	}

	mismatch := api.postRawJSONWithCookieAndCSRF(t, "/api/v1/notebooks", `{"title":"Different Retry Notes"}`, sessionCookie, csrfCookie, csrfCookie.Value, "canonical-create")
	if mismatch.Code != http.StatusConflict {
		t.Fatalf("canonical mismatch status = %d, body = %s", mismatch.Code, mismatch.Body.String())
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

func TestSessionUnauthorizedDistinguishesMissingAndExpired(t *testing.T) {
	api := newTestAPI(t)

	missing := api.getWithCookie(t, "/api/v1/session", nil)
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("missing session status = %d, body = %s", missing.Code, missing.Body.String())
	}
	missingErr := decodeError(t, missing)
	if missingErr.Code != "session_missing" || missingErr.MessageKey != "error.session_missing" {
		t.Fatalf("missing session error = %+v", missingErr)
	}

	expired := api.getWithCookie(t, "/api/v1/session", &http.Cookie{Name: "nn_session", Value: "stale-token"})
	if expired.Code != http.StatusUnauthorized {
		t.Fatalf("expired session status = %d, body = %s", expired.Code, expired.Body.String())
	}
	expiredErr := decodeError(t, expired)
	if expiredErr.Code != "session_expired" || expiredErr.MessageKey != "error.session_expired" {
		t.Fatalf("expired session error = %+v", expiredErr)
	}
}

func TestSessionCookiesCarryRequiredAttributesAndClearOnSignOut(t *testing.T) {
	api := newTestAPI(t)
	sessionCookie, csrfCookie := api.registerWithCSRF(t, "cookies@example.com")
	assertCookieAttrs(t, sessionCookie, true, false, 0)
	assertCookieAttrs(t, csrfCookie, false, false, 0)

	signOut := api.postJSONWithCookieAndCSRF(t, "/api/v1/auth/sign-out", map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if signOut.Code != http.StatusNoContent {
		t.Fatalf("sign-out status = %d, body = %s", signOut.Code, signOut.Body.String())
	}
	expiredSession := cookieNamed(t, signOut, "nn_session")
	expiredCSRF := cookieNamed(t, signOut, "nn_csrf")
	assertCookieAttrs(t, expiredSession, true, false, -1)
	assertCookieAttrs(t, expiredCSRF, false, false, -1)
}

func TestAnonymousNotebookReadAndMutationAreRejected(t *testing.T) {
	api := newTestAPI(t)

	list := api.getWithCookie(t, "/api/v1/notebooks", nil)
	if list.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list status = %d, body = %s", list.Code, list.Body.String())
	}
	read := api.getWithCookie(t, "/api/v1/notebooks/nb_missing", nil)
	if read.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous read status = %d, body = %s", read.Code, read.Body.String())
	}
	create := api.postJSONWithCookieAndCSRF(t, "/api/v1/notebooks", map[string]any{"title": "Anonymous"}, nil, nil, "", "anon-create")
	if create.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous create status = %d, body = %s", create.Code, create.Body.String())
	}
}

func TestRecentOrderingAndSearchClearThroughApplicationPath(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	sessionCookie, csrfCookie := api.registerWithCSRF(t, "recent@example.com")

	first := api.postJSONWithCookieAndCSRF(t, "/api/v1/notebooks", map[string]any{"title": "Alpha Research"}, sessionCookie, csrfCookie, csrfCookie.Value, "recent-alpha")
	second := api.postJSONWithCookieAndCSRF(t, "/api/v1/notebooks", map[string]any{"title": "Beta Notes"}, sessionCookie, csrfCookie, csrfCookie.Value, "recent-beta")
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated {
		t.Fatalf("create statuses = %d/%d bodies = %s/%s", first.Code, second.Code, first.Body.String(), second.Body.String())
	}
	var firstBody, secondBody struct {
		Notebook struct {
			ID string `json:"id"`
		} `json:"notebook"`
	}
	decodeBody(t, first, &firstBody)
	decodeBody(t, second, &secondBody)
	if _, err := api.db.Pool().Exec(ctx, `
		update notebook_notebooks
		set recent_at = case id
			when $1 then now() - interval '1 hour'
			when $2 then now()
			else recent_at
		end
		where id in ($1, $2)`, firstBody.Notebook.ID, secondBody.Notebook.ID); err != nil {
		t.Fatal(err)
	}

	search := api.getWithCookie(t, "/api/v1/notebooks?query=alpha", sessionCookie)
	var searched struct {
		Notebooks []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"notebooks"`
	}
	decodeBody(t, search, &searched)
	if len(searched.Notebooks) != 1 || searched.Notebooks[0].Title != "Alpha Research" {
		t.Fatalf("filtered search results = %+v", searched.Notebooks)
	}

	cleared := api.getWithCookie(t, "/api/v1/notebooks?query=", sessionCookie)
	var listed struct {
		Notebooks []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"notebooks"`
	}
	decodeBody(t, cleared, &listed)
	if len(listed.Notebooks) != 2 || listed.Notebooks[0].Title != "Beta Notes" || listed.Notebooks[1].Title != "Alpha Research" {
		t.Fatalf("cleared search ordering = %+v", listed.Notebooks)
	}
}

func TestRegistrationAndSignInRetryPathsRemainPreAuthPrivileged(t *testing.T) {
	api := newTestAPI(t)

	weak := api.postJSON(t, "/api/v1/auth/register", map[string]any{
		"email":    "retry@example.com",
		"password": "short",
	}, "")
	if weak.Code != http.StatusBadRequest || len(weak.Result().Cookies()) != 0 {
		t.Fatalf("weak register status/cookies = %d/%+v", weak.Code, weak.Result().Cookies())
	}
	created := api.postJSON(t, "/api/v1/auth/register", map[string]any{
		"email":    "retry@example.com",
		"password": validTestPassword,
	}, "")
	if created.Code != http.StatusCreated {
		t.Fatalf("retry register status = %d, body = %s", created.Code, created.Body.String())
	}
	sessionCookie := cookieNamed(t, created, "nn_session")
	csrfCookie := cookieNamed(t, created, "nn_csrf")
	signOut := api.postJSONWithCookieAndCSRF(t, "/api/v1/auth/sign-out", map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if signOut.Code != http.StatusNoContent {
		t.Fatalf("sign-out before sign-in retry status = %d, body = %s", signOut.Code, signOut.Body.String())
	}

	badSignIn := api.postJSON(t, "/api/v1/auth/sign-in", map[string]any{
		"email":    "retry@example.com",
		"password": "wrong wrong wrong",
	}, "")
	if badSignIn.Code != http.StatusUnauthorized || len(badSignIn.Result().Cookies()) != 0 {
		t.Fatalf("bad sign-in status/cookies = %d/%+v", badSignIn.Code, badSignIn.Result().Cookies())
	}
	goodSignIn := api.postJSON(t, "/api/v1/auth/sign-in", map[string]any{
		"email":    "retry@example.com",
		"password": validTestPassword,
	}, "")
	if goodSignIn.Code != http.StatusOK {
		t.Fatalf("retry sign-in status = %d, body = %s", goodSignIn.Code, goodSignIn.Body.String())
	}
	assertCookieAttrs(t, cookieNamed(t, goodSignIn, "nn_session"), true, false, 0)
	assertCookieAttrs(t, cookieNamed(t, goodSignIn, "nn_csrf"), false, false, 0)
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
	var agentPolicies int
	if err := api.db.Pool().QueryRow(ctx, `
		select count(distinct tablename)
		from pg_policies
		where schemaname = 'public'
		  and tablename in ('chat_chats', 'chat_messages', 'agent_runs', 'agent_jobs')
	`).Scan(&agentPolicies); err != nil {
		t.Fatal(err)
	}
	if agentPolicies != 4 {
		t.Fatalf("Sprint 2A RLS table coverage = %d, want 4", agentPolicies)
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

func TestMigrationsInstallSprint3RunConfiguration(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "migration-sprint3-config@example.com")
	ctx := context.Background()

	wantColumns := []string{
		"time_zone",
		"deadline_at",
		"action_decision_limit",
		"final_decision_limit",
		"action_limit",
		"action_batch_limit",
		"action_result_byte_limit",
		"action_results_byte_limit",
	}
	rows, err := api.db.Pool().Query(ctx, `
		select column_name
		from information_schema.columns
		where table_schema = 'public' and table_name = 'agent_runs'
			and column_name = any($1::text[])
		order by column_name`, wantColumns)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := make(map[string]bool, len(wantColumns))
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, name := range wantColumns {
		if !found[name] {
			t.Errorf("Sprint 3 agent_runs column %q is missing", name)
		}
	}
	if t.Failed() {
		return
	}

	admittedAfter := time.Now().UTC()
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c036")
	var timeZone string
	var deadlineAt time.Time
	var actionDecisionLimit, finalDecisionLimit, actionLimit, actionBatchLimit int
	var actionResultByteLimit, actionResultsByteLimit int
	if err := api.db.Pool().QueryRow(ctx, `
		select time_zone, deadline_at, action_decision_limit, final_decision_limit,
			action_limit, action_batch_limit, action_result_byte_limit, action_results_byte_limit
		from agent_runs where id = $1`, runID).Scan(
		&timeZone, &deadlineAt, &actionDecisionLimit, &finalDecisionLimit,
		&actionLimit, &actionBatchLimit, &actionResultByteLimit, &actionResultsByteLimit,
	); err != nil {
		t.Fatal(err)
	}
	if timeZone != "UTC" || actionDecisionLimit != 4 || finalDecisionLimit != 1 || actionLimit != 8 || actionBatchLimit != 4 || actionResultByteLimit != 16*1024 || actionResultsByteLimit != 64*1024 {
		t.Fatalf("unexpected Sprint 3 defaults: zone=%q decisions=%d+%d actions=%d batch=%d bytes=%d/%d", timeZone, actionDecisionLimit, finalDecisionLimit, actionLimit, actionBatchLimit, actionResultByteLimit, actionResultsByteLimit)
	}
	if deadlineAt.Before(admittedAfter.Add(9*time.Minute+50*time.Second)) || deadlineAt.After(admittedAfter.Add(10*time.Minute+10*time.Second)) {
		t.Fatalf("deadline_at = %s, want approximately ten minutes after admission", deadlineAt)
	}
}

func TestMigrationsUpgradePopulatedSprint2BRunConfiguration(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "migration-sprint2b@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c046")
	ctx := context.Background()
	if _, err := api.db.Pool().Exec(ctx, `
		alter table agent_runs drop column time_zone;
		alter table agent_runs drop column deadline_at;
		alter table agent_runs drop column action_decision_limit;
		alter table agent_runs drop column final_decision_limit;
		alter table agent_runs drop column action_limit;
		alter table agent_runs drop column action_batch_limit;
		alter table agent_runs drop column action_result_byte_limit;
		alter table agent_runs drop column action_results_byte_limit;`); err != nil {
		t.Fatal(err)
	}

	migrationStarted := time.Now().UTC()
	if err := app.RunMigrations(ctx, api.db); err != nil {
		t.Fatalf("Sprint 2B upgrade migration: %v", err)
	}
	var runStatus, jobStatus, timeZone string
	var deadlineAt time.Time
	var actionDecisionLimit, finalDecisionLimit, actionLimit, actionBatchLimit int
	var actionResultByteLimit, actionResultsByteLimit int
	if err := api.db.Pool().QueryRow(ctx, `
		select r.status, j.status, r.time_zone, r.deadline_at,
			r.action_decision_limit, r.final_decision_limit, r.action_limit,
			r.action_batch_limit, r.action_result_byte_limit, r.action_results_byte_limit
		from agent_runs r join agent_jobs j on j.run_id = r.id
		where r.id = $1`, runID).Scan(
		&runStatus, &jobStatus, &timeZone, &deadlineAt,
		&actionDecisionLimit, &finalDecisionLimit, &actionLimit,
		&actionBatchLimit, &actionResultByteLimit, &actionResultsByteLimit,
	); err != nil {
		t.Fatal(err)
	}
	if runStatus != "queued" || jobStatus != "queued" || timeZone != "UTC" {
		t.Fatalf("upgraded active state run=%q job=%q zone=%q", runStatus, jobStatus, timeZone)
	}
	if actionDecisionLimit != 4 || finalDecisionLimit != 1 || actionLimit != 8 || actionBatchLimit != 4 || actionResultByteLimit != 16*1024 || actionResultsByteLimit != 64*1024 {
		t.Fatalf("upgraded budgets=%d+%d/%d/%d/%d/%d", actionDecisionLimit, finalDecisionLimit, actionLimit, actionBatchLimit, actionResultByteLimit, actionResultsByteLimit)
	}
	if deadlineAt.Before(migrationStarted.Add(9*time.Minute+50*time.Second)) || deadlineAt.After(migrationStarted.Add(10*time.Minute+10*time.Second)) {
		t.Fatalf("upgraded deadline_at = %s, want approximately ten minutes after migration", deadlineAt)
	}
	var messages int
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from chat_messages where chat_id = $1`, chatID).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if messages != 1 {
		t.Fatalf("preserved chat message count = %d, want 1", messages)
	}
}

func TestMigrationsInstallInternalCheckpointSchema(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	var exists bool
	if err := api.db.Pool().QueryRow(ctx, `select to_regclass('public.agent_run_checkpoints') is not null`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("agent_run_checkpoints table is missing")
	}

	wantColumns := []string{
		"run_id",
		"sequence_no",
		"identity_key",
		"kind",
		"decision_no",
		"action_index",
		"action_id",
		"payload_version",
		"payload",
		"payload_sha256",
		"created_at",
	}
	var columns int
	if err := api.db.Pool().QueryRow(ctx, `
		select count(*)
		from information_schema.columns
		where table_schema = 'public' and table_name = 'agent_run_checkpoints'
			and column_name = any($1::text[])`, wantColumns).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != len(wantColumns) {
		t.Fatalf("checkpoint schema columns = %d, want %d", columns, len(wantColumns))
	}

	var rls, workerSelect, workerInsert, workerUpdate, workerDelete, appSelect bool
	if err := api.db.Pool().QueryRow(ctx, `
		select c.relrowsecurity,
			has_table_privilege('nano_worker', c.oid, 'SELECT'),
			has_table_privilege('nano_worker', c.oid, 'INSERT'),
			has_table_privilege('nano_worker', c.oid, 'UPDATE'),
			has_table_privilege('nano_worker', c.oid, 'DELETE'),
			has_table_privilege('nano_app', c.oid, 'SELECT')
		from pg_class c
		join pg_namespace n on n.oid = c.relnamespace
		where n.nspname = 'public' and c.relname = 'agent_run_checkpoints'`).Scan(
		&rls, &workerSelect, &workerInsert, &workerUpdate, &workerDelete, &appSelect,
	); err != nil {
		t.Fatal(err)
	}
	if !rls || !workerSelect || !workerInsert || workerUpdate || workerDelete || appSelect {
		t.Fatalf("checkpoint access rls=%t worker=%t/%t/%t/%t app_select=%t", rls, workerSelect, workerInsert, workerUpdate, workerDelete, appSelect)
	}
}

func TestMigrationsRetireSprint2BSingleCallFieldsWithoutLosingHistory(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "migration-retire-fields@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c056")
	ctx := context.Background()
	const messageID = "msg_legacy_answer"
	if _, err := api.db.Pool().Exec(ctx, `
		alter table chat_messages add column if not exists answer_mode text;
		alter table agent_runs add column if not exists iteration_count integer not null default 0;
		alter table agent_runs add column if not exists finish_reason text;
		alter table agent_runs add column if not exists prompt_tokens integer;
		alter table agent_runs add column if not exists completion_tokens integer;
		alter table agent_runs add column if not exists total_tokens integer;`); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(ctx, `
		insert into chat_messages(id, chat_id, role, content, answer_mode)
		values($1, $2, 'assistant', 'Preserved historical answer.', 'model_knowledge')`, messageID, chatID); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(ctx, `
		update agent_runs
		set output_message_id = $1, status = 'completed', iteration_count = 1,
			finish_reason = 'stop', prompt_tokens = 12, completion_tokens = 8,
			total_tokens = 20, finished_at = now(), updated_at = now()
		where id = $2`, messageID, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(ctx, `
		update agent_jobs
		set status = 'succeeded', finished_at = now(), updated_at = now()
		where run_id = $1`, runID); err != nil {
		t.Fatal(err)
	}

	if err := app.RunMigrations(ctx, api.db); err != nil {
		t.Fatalf("retire Sprint 2B fields migration: %v", err)
	}
	var legacyColumns int
	if err := api.db.Pool().QueryRow(ctx, `
		select count(*)
		from information_schema.columns
		where table_schema = 'public' and (
			(table_name = 'chat_messages' and column_name = 'answer_mode')
			or (table_name = 'agent_runs' and column_name in (
				'iteration_count', 'finish_reason', 'prompt_tokens', 'completion_tokens', 'total_tokens'
			))
		)`).Scan(&legacyColumns); err != nil {
		t.Fatal(err)
	}
	if legacyColumns != 0 {
		t.Fatalf("obsolete Sprint 2B columns remaining = %d, want 0", legacyColumns)
	}

	var content, runStatus, jobStatus string
	if err := api.db.Pool().QueryRow(ctx, `
		select m.content, r.status, j.status
		from chat_messages m
		join agent_runs r on r.output_message_id = m.id
		join agent_jobs j on j.run_id = r.id
		where m.id = $1`, messageID).Scan(&content, &runStatus, &jobStatus); err != nil {
		t.Fatal(err)
	}
	if content != "Preserved historical answer." || runStatus != "completed" || jobStatus != "succeeded" {
		t.Fatalf("history after migration content=%q run=%q job=%q", content, runStatus, jobStatus)
	}
}

func TestMigrationsUpgradeAPopulatedSprint2ADatabase(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "migration-2a@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c026")
	ctx := context.Background()
	if _, err := api.db.Pool().Exec(ctx, `
		drop index if exists agent_jobs_expired_lease_idx;
		drop index if exists agent_runs_one_active_per_input_idx;
		drop index if exists agent_runs_one_completed_per_input_idx;
		alter table agent_jobs drop constraint if exists agent_jobs_execution_state_check;
		alter table agent_jobs drop constraint if exists agent_jobs_status_check;
		alter table agent_jobs drop column if exists attempt_no;
		alter table agent_jobs drop column if exists lease_token;
		alter table agent_jobs drop column if exists lease_expires_at;
		alter table agent_jobs add constraint agent_jobs_status_check check (status in ('queued', 'running', 'succeeded', 'failed'));
		alter table agent_runs drop constraint if exists agent_runs_status_check;
		alter table agent_runs add constraint agent_runs_status_check check (status in ('queued', 'running', 'completed', 'failed'));
		alter table agent_runs add constraint agent_runs_input_message_id_key unique(input_message_id);`); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(ctx, `update agent_runs set status='running', started_at=now() where id=$1`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(ctx, `update agent_jobs set status='running', started_at=now() where run_id=$1`, runID); err != nil {
		t.Fatal(err)
	}
	if err := app.RunMigrations(ctx, api.db); err != nil {
		t.Fatalf("Sprint 2A upgrade migration: %v", err)
	}

	var runStatus, jobStatus string
	var attemptNo int
	var token *string
	if err := api.db.Pool().QueryRow(ctx, `
		select r.status, j.status, j.attempt_no, j.lease_token::text
		from agent_runs r join agent_jobs j on j.run_id=r.id where r.id=$1`, runID).
		Scan(&runStatus, &jobStatus, &attemptNo, &token); err != nil {
		t.Fatal(err)
	}
	if runStatus != "queued" || jobStatus != "queued" || attemptNo != 0 || token != nil {
		t.Fatalf("upgraded legacy running state run=%q job=%q attempt=%d token=%v", runStatus, jobStatus, attemptNo, token)
	}
	for _, indexName := range []string{"agent_runs_one_active_per_input_idx", "agent_runs_one_completed_per_input_idx", "agent_jobs_expired_lease_idx"} {
		var exists bool
		if err := api.db.Pool().QueryRow(ctx, `select to_regclass($1) is not null`, indexName).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("upgraded schema is missing index %q", indexName)
		}
	}
}

func TestApplicationRequestsAreConstrainedByRequestRoleRLS(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	sessionCookie, csrfCookie := api.registerWithCSRF(t, "request-rls@example.com")
	create := api.postJSONWithCookieAndCSRF(t, "/api/v1/notebooks", map[string]any{"title": "Request RLS Notebook"}, sessionCookie, csrfCookie, csrfCookie.Value, "request-rls-create")
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", create.Code, create.Body.String())
	}

	_, err := api.db.Pool().Exec(ctx, `
		drop policy notebook_memberships_owner on notebook_memberships;
		create policy notebook_memberships_owner on notebook_memberships
			for all to nano_app
			using (false)
			with check (false);
	`)
	if err != nil {
		t.Fatal(err)
	}

	list := api.getWithCookie(t, "/api/v1/notebooks", sessionCookie)
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", list.Code, list.Body.String())
	}
	var listed struct {
		Notebooks []struct {
			ID string `json:"id"`
		} `json:"notebooks"`
	}
	decodeBody(t, list, &listed)
	if len(listed.Notebooks) != 0 {
		t.Fatalf("request bypassed nano_app RLS and returned notebooks: %+v", listed.Notebooks)
	}
}

type testAPI struct {
	handler       http.Handler
	server        *app.Server
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
	return &testAPI{handler: server.Handler(), server: server, db: db, csrfBySession: map[string]*http.Cookie{}}
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
	return api.postRawJSONWithCookieAndCSRF(t, path, string(body), cookie, csrfCookie, csrfHeader, idempotencyKey)
}

func (api *testAPI) postRawJSONWithCookieAndCSRF(t *testing.T, path string, body string, cookie *http.Cookie, csrfCookie *http.Cookie, csrfHeader string, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
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

func assertCookieAttrs(t *testing.T, cookie *http.Cookie, httpOnly bool, secure bool, maxAge int) {
	t.Helper()
	if cookie.Path != "/" || cookie.HttpOnly != httpOnly || cookie.Secure != secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie %s attrs path=%q httpOnly=%v secure=%v sameSite=%v", cookie.Name, cookie.Path, cookie.HttpOnly, cookie.Secure, cookie.SameSite)
	}
	if maxAge != 0 && cookie.MaxAge != maxAge {
		t.Fatalf("cookie %s max age = %d, want %d", cookie.Name, cookie.MaxAge, maxAge)
	}
	if maxAge == 0 && cookie.Expires.IsZero() {
		t.Fatalf("cookie %s missing expiry", cookie.Name)
	}
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

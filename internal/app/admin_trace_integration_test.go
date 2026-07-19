package app_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/app"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/huangxinxinyu/nano-notebook/internal/replay"
)

func TestAdminTraceCapabilitiesGateRoutesAndAuditReplay(t *testing.T) {
	api := newTestAPI(t)
	session, csrf := api.registerWithCSRF(t, "trace-operator@example.com")
	var userID string
	if err := api.db.Pool().QueryRow(context.Background(), `select id from identity_users where canonical_email = 'trace-operator@example.com'`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	keys, err := replay.NewDevelopmentKeyProvider("test-replay-key", bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, _ := replay.NewSealer(keys)
	plain, _ := replay.NewPlainPayload(replay.ClassModelRequest, 1, []byte(`{"schema_version":1,"class":"model_request","messages":[{"role":"user","content":"hello"}]}`))
	sealed, err := sealer.Seal(context.Background(), plain)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAdminTraceClient{replay: collector.OpaqueReplay{
		AttachmentID: "019bf000-0000-7000-8000-000000000555", TraceID: "trace-admin", SpanID: "span-admin",
		Class: replay.ClassModelRequest, Sealed: sealed,
	}}
	server := app.NewServer(app.Config{CookieSecure: false, AdminTraces: fake, ReplaySealer: sealer}, api.db)
	api.handler, api.server = server.Handler(), server

	forbidden := api.getWithCookie(t, "/api/admin/traces", session)
	if forbidden.Code != http.StatusForbidden || fake.listCalls != 0 {
		t.Fatalf("ungranted list status=%d calls=%d body=%s", forbidden.Code, fake.listCalls, forbidden.Body.String())
	}
	createdNotebook := api.postJSONWithCookieAndCSRF(t, "/api/v1/notebooks", map[string]any{"title": "Trace role isolation"}, session, csrf, csrf.Value, "trace-role-owner")
	if createdNotebook.Code != http.StatusCreated {
		t.Fatalf("create owner Notebook status=%d body=%s", createdNotebook.Code, createdNotebook.Body.String())
	}
	ownerForbidden := api.getWithCookie(t, "/api/admin/traces", session)
	if ownerForbidden.Code != http.StatusForbidden || fake.listCalls != 0 {
		t.Fatalf("Notebook Owner list status=%d calls=%d body=%s", ownerForbidden.Code, fake.listCalls, ownerForbidden.Body.String())
	}
	grantCapability(t, api, userID, "platform.trace.read")
	sessionResponse := api.getWithCookie(t, "/api/v1/session", session)
	if sessionResponse.Code != http.StatusOK || !strings.Contains(sessionResponse.Body.String(), `"platform.trace.read"`) {
		t.Fatalf("capability-aware session status=%d body=%s", sessionResponse.Code, sessionResponse.Body.String())
	}
	allowed := api.getWithCookie(t, "/api/admin/traces?page_size=25", session)
	if allowed.Code != http.StatusOK || fake.listCalls != 1 {
		t.Fatalf("granted list status=%d calls=%d body=%s", allowed.Code, fake.listCalls, allowed.Body.String())
	}

	replayPath := "/api/admin/traces/trace-admin/replay/019bf000-0000-7000-8000-000000000555?span_id=span-admin"
	replayForbidden := api.getWithCookie(t, replayPath, session)
	if replayForbidden.Code != http.StatusForbidden || fake.replayCalls != 0 {
		t.Fatalf("read-only Replay status=%d calls=%d body=%s", replayForbidden.Code, fake.replayCalls, replayForbidden.Body.String())
	}
	grantCapability(t, api, userID, "platform.trace.replay")
	var replayLogs bytes.Buffer
	priorLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&replayLogs, nil)))
	t.Cleanup(func() { slog.SetDefault(priorLogger) })
	replayed := api.getWithCookie(t, replayPath, session)
	if replayed.Code != http.StatusOK || replayed.Header().Get("Cache-Control") != "no-store" || fake.replayCalls != 1 ||
		!strings.Contains(replayed.Body.String(), `"content":"hello"`) || strings.Contains(replayed.Body.String(), "Ciphertext") || strings.Contains(replayed.Body.String(), "WrappedKey") {
		t.Fatalf("Replay status=%d calls=%d headers=%v body=%s", replayed.Code, fake.replayCalls, replayed.Header(), replayed.Body.String())
	}
	if strings.Contains(replayLogs.String(), "hello") || strings.Contains(replayLogs.String(), `"messages"`) {
		t.Fatalf("request log retained Replay response content: %s", replayLogs.String())
	}
	tampered := sealed
	tampered.Ciphertext = append([]byte(nil), sealed.Ciphertext...)
	tampered.Ciphertext[0] ^= 0xff
	fake.replay.Sealed = tampered
	corrupt := api.getWithCookie(t, replayPath, session)
	if corrupt.Code != http.StatusServiceUnavailable || !strings.Contains(corrupt.Body.String(), `"code":"replay_corrupt"`) || strings.Contains(corrupt.Body.String(), "hello") {
		t.Fatalf("corrupt Replay status=%d body=%s", corrupt.Code, corrupt.Body.String())
	}
	var denied, allowedAudits, failed int
	if err := api.db.Pool().QueryRow(context.Background(), `
		select count(*) filter(where outcome='denied'), count(*) filter(where outcome='allowed'), count(*) filter(where outcome='failed')
		from platform_replay_access_audit where operator_user_id=$1
	`, userID).Scan(&denied, &allowedAudits, &failed); err != nil {
		t.Fatal(err)
	}
	if denied != 1 || allowedAudits != 1 || failed != 1 {
		t.Fatalf("Replay audits denied=%d allowed=%d failed=%d", denied, allowedAudits, failed)
	}
}

func grantCapability(t *testing.T, api *testAPI, userID, capability string) {
	t.Helper()
	if _, err := api.db.Pool().Exec(context.Background(), `insert into platform_capability_grants(user_id, capability, granted_by) values($1,$2,'test-bootstrap')`, userID, capability); err != nil {
		t.Fatal(err)
	}
}

type fakeAdminTraceClient struct {
	listCalls, detailCalls, replayCalls int
	replay                              collector.OpaqueReplay
}

func (f *fakeAdminTraceClient) List(context.Context, collector.TraceListQuery) (collector.TraceListResult, error) {
	f.listCalls++
	return collector.TraceListResult{Items: []collector.TraceListItem{{Summary: collector.TraceSummary{TraceID: "trace-admin", RunID: "run-admin"}}}}, nil
}

func (f *fakeAdminTraceClient) Detail(context.Context, agentobs.TraceID) (collector.ProjectedTrace, error) {
	f.detailCalls++
	return collector.ProjectedTrace{}, nil
}

func (f *fakeAdminTraceClient) Replay(context.Context, agentobs.TraceID, agentobs.SpanID, string) (collector.OpaqueReplay, error) {
	f.replayCalls++
	return f.replay, nil
}

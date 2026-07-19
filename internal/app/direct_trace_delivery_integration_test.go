package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/agentbatch"
	"github.com/huangxinxinyu/nano-notebook/internal/app"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

func TestDirectTraceAdmissionCommitsOnlyAnchorAndProductSurvivesExporterOverflow(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "direct-trace-admission@example.com")
	sink := &capturingDirectTraceSink{err: agentbatch.ErrQueueFull}
	api.server = app.NewServer(app.Config{CookieSecure: false, TraceSink: sink}, api.db)
	api.handler = api.server.Handler()

	response := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id": "0190cdd2-5f2d-7ad8-b3f5-1b588788d111", "content": "direct Trace admission",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("admission status = %d, body = %s", response.Code, response.Body.String())
	}
	if len(sink.envelopes) != 2 {
		t.Fatalf("direct envelopes = %d, want root and admitted Event", len(sink.envelopes))
	}
	traceID := sink.envelopes[0].Trace.TraceID
	if traceID == "" || sink.envelopes[0].Record.IdentityKey == "" || sink.envelopes[1].Trace.TraceID != traceID {
		t.Fatalf("direct Trace envelope = %#v", sink.envelopes)
	}
	var anchors, fullRecords int
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from agent_trace_refs where trace_id = $1`, traceID).Scan(&anchors); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from agentobs_outbox_records where trace_id = $1`, traceID).Scan(&fullRecords); err != nil {
		t.Fatal(err)
	}
	if anchors != 1 || fullRecords != 0 {
		t.Fatalf("Application PostgreSQL Trace rows: anchors=%d full_records=%d", anchors, fullRecords)
	}
}

func TestDirectTraceQueueClaimDoesNotAppendApplicationOutbox(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "direct-trace-worker@example.com")
	sink := &capturingDirectTraceSink{}
	api.server = app.NewServer(app.Config{CookieSecure: false, TraceSink: sink}, api.db)
	api.handler = api.server.Handler()

	response := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id": "0190cdd2-5f2d-7ad8-b3f5-1b588788d222", "content": "direct Trace claim",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("admission status = %d, body = %s", response.Code, response.Body.String())
	}
	var admission struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &admission); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := jobs.NewQueueWithTraceSink(api.db.Pool(), sink).ClaimNext(context.Background())
	if err != nil || !ok || claimed.RunID != admission.RunID {
		t.Fatalf("ClaimNext = %#v ok=%t err=%v", claimed, ok, err)
	}
	pending, err := agent.NewProposalCheckpoint(1, models.ActionProposalBatch{Actions: []models.ActionProposal{{
		Name: "calculate", Input: []byte(`{"operation":"add","operands":["1","2"]}`),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := agent.NewPostgresRuntime(api.db.Pool(), "", nil, agent.WithTraceSink(sink))
	if _, err := runtime.AppendCheckpoint(context.Background(), attemptFromClaim(claimed), pending); err != nil {
		t.Fatalf("AppendCheckpoint: %v", err)
	}
	var fullRecords int
	if err := api.db.Pool().QueryRow(context.Background(), `
		select count(*) from agentobs_outbox_records r
		join agent_trace_refs t on t.trace_id = r.trace_id
		where t.run_id = $1`, admission.RunID).Scan(&fullRecords); err != nil {
		t.Fatal(err)
	}
	if fullRecords != 0 {
		t.Fatalf("Application PostgreSQL full Trace records after claim = %d", fullRecords)
	}
	foundAttempt := false
	foundCheckpoint := false
	for _, envelope := range sink.envelopes {
		if envelope.Record.IdentityKey == "run/"+admission.RunID+"/attempt/1/start" {
			foundAttempt = true
		}
		if envelope.Record.Name == agent.TraceEventCheckpointAccepted {
			foundCheckpoint = true
		}
	}
	if !foundAttempt || !foundCheckpoint {
		t.Fatalf("direct attempt/checkpoint missing from %#v", sink.envelopes)
	}
}

type capturingDirectTraceSink struct {
	envelopes []agentbatch.Envelope
	err       error
}

func (s *capturingDirectTraceSink) Offer(_ context.Context, envelope agentbatch.Envelope) error {
	s.envelopes = append(s.envelopes, envelope)
	return s.err
}

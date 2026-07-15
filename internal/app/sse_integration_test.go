package app_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/realtime"
)

func TestRunSSEReconnectSendsTheCompletedDurableSnapshot(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "sse-reconnect@example.com")
	admitted := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id":      "0190cdd2-5f2d-7ad8-b3f5-1b588788c007",
		"content": "Return a durable SSE answer.",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if admitted.Code != http.StatusAccepted {
		t.Fatalf("admission status = %d, body = %s", admitted.Code, admitted.Body.String())
	}
	var admittedBody struct {
		RunID string `json:"run_id"`
	}
	decodeBody(t, admitted, &admittedBody)

	ctx := context.Background()
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	model := &recordingModelClient{result: models.ChatResult{Text: "The durable answer.", FinishReason: "stop"}}
	runtime := agent.NewPostgresRuntime(api.db.Pool(), "System prompt.", func() string { return "msg_sse_answer" })
	if err := agent.NewLoop(runtime, runtime, agent.NewModelRunner(model), runtime).Execute(ctx, attemptFromClaim(claimed)); err != nil {
		t.Fatal(err)
	}

	events := api.getWithCookie(t, "/api/v1/agent-runs/"+admittedBody.RunID+"/events", sessionCookie)
	if events.Code != http.StatusOK {
		t.Fatalf("SSE status = %d, body = %s", events.Code, events.Body.String())
	}
	if contentType := events.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("SSE content type = %q", contentType)
	}
	body := events.Body.String()
	for _, expected := range []string{`event: run`, `"status":"completed"`, `"id":"msg_sse_answer"`, `"content":"The durable answer."`, `"answer_mode":"model_knowledge"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("SSE body missing %q: %s", expected, body)
		}
	}
}

func TestRunSSEProjectsQueuedRunningAndCompletedAcrossPostgresNotifications(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "sse-live@example.com")
	admitted := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id":      "0190cdd2-5f2d-7ad8-b3f5-1b588788c008",
		"content": "Project every durable state.",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if admitted.Code != http.StatusAccepted {
		t.Fatalf("admission status = %d, body = %s", admitted.Code, admitted.Body.String())
	}
	var admittedBody struct {
		RunID string `json:"run_id"`
	}
	decodeBody(t, admitted, &admittedBody)

	listenerCtx, stopListener := context.WithCancel(context.Background())
	defer stopListener()
	listener := realtime.NewRunListener(api.db.Pool(), api.server.NotifyRun)
	listenerDone := make(chan error, 1)
	go func() { listenerDone <- listener.Run(listenerCtx) }()
	select {
	case <-listener.Ready():
	case <-time.After(3 * time.Second):
		t.Fatal("Run listener did not become ready")
	}

	requestCtx, stopRequest := context.WithCancel(context.Background())
	defer stopRequest()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-runs/"+admittedBody.RunID+"/events", nil).WithContext(requestCtx)
	request.AddCookie(sessionCookie)
	writer := newStreamingRecorder()
	streamDone := make(chan struct{})
	go func() {
		api.handler.ServeHTTP(writer, request)
		close(streamDone)
	}()
	writer.waitForFlush(t)
	if body := writer.body(); !strings.Contains(body, `"status":"queued"`) {
		t.Fatalf("initial SSE body = %s", body)
	}

	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(context.Background())
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	writer.waitForFlush(t)
	if body := writer.body(); !strings.Contains(body, `"status":"running"`) {
		t.Fatalf("running SSE body = %s", body)
	}

	runtime := agent.NewPostgresRuntime(api.db.Pool(), "System prompt.", func() string { return "msg_sse_live" })
	model := &recordingModelClient{result: models.ChatResult{Text: "Projected completion.", FinishReason: "stop"}}
	if err := agent.NewLoop(runtime, runtime, agent.NewModelRunner(model), runtime).Execute(context.Background(), attemptFromClaim(claimed)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-streamDone:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE stream did not close after completion")
	}
	if body := writer.body(); !strings.Contains(body, `"status":"completed"`) || !strings.Contains(body, `"id":"msg_sse_live"`) {
		t.Fatalf("completed SSE body = %s", body)
	}
	stopListener()
	select {
	case err := <-listenerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run listener did not stop")
	}
}

type streamingRecorder struct {
	mu      sync.Mutex
	header  http.Header
	status  int
	buffer  bytes.Buffer
	flushes chan struct{}
}

func newStreamingRecorder() *streamingRecorder {
	return &streamingRecorder{header: make(http.Header), flushes: make(chan struct{}, 8)}
}

func (w *streamingRecorder) Header() http.Header {
	return w.header
}

func (w *streamingRecorder) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = status
	}
}

func (w *streamingRecorder) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.buffer.Write(data)
}

func (w *streamingRecorder) Flush() {
	select {
	case w.flushes <- struct{}{}:
	default:
	}
}

func (w *streamingRecorder) body() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

func (w *streamingRecorder) waitForFlush(t *testing.T) {
	t.Helper()
	select {
	case <-w.flushes:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE stream did not flush")
	}
}

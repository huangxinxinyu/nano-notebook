package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/jackc/pgx/v5"
)

func TestWorkerClaimsBuildsContextAndPublishesOneAnswer(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "worker-happy@example.com")
	const messageID = "0190cdd2-5f2d-7ad8-b3f5-1b588788c005"
	admitted := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id":      messageID,
		"content": "Why is a publication barrier useful?",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if admitted.Code != http.StatusAccepted {
		t.Fatalf("admission status = %d, body = %s", admitted.Code, admitted.Body.String())
	}
	var admittedBody struct {
		RunID string `json:"run_id"`
	}
	decodeBody(t, admitted, &admittedBody)

	ctx := context.Background()
	queue := jobs.NewQueue(api.db.Pool())
	claimed, ok, err := queue.ClaimNext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.RunID != admittedBody.RunID {
		t.Fatalf("claimed = %+v ok=%v, want run %q", claimed, ok, admittedBody.RunID)
	}

	var modelRequest struct {
		Model               string               `json:"model"`
		Messages            []models.ChatMessage `json:"messages"`
		Stream              bool                 `json:"stream"`
		MaxCompletionTokens int                  `json:"max_completion_tokens"`
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("Bifrost request = %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&modelRequest); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"It makes provisional output durable exactly once."},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20}
		}`))
	}))
	defer upstream.Close()
	model := models.NewBifrostClient(upstream.URL, upstream.Client(), 2048)
	runtime := agent.NewPostgresRuntime(api.db.Pool(), "System prompt for the bare agent.", func() string { return "msg_worker_answer" })
	loop := agent.NewLoop(runtime, runtime, agent.NewModelRunner(model), runtime)
	if err := loop.Execute(ctx, attemptFromClaim(claimed)); err != nil {
		t.Fatal(err)
	}
	if modelRequest.Model != "aliyun/qwen-flash" || modelRequest.Stream || modelRequest.MaxCompletionTokens != 2048 {
		t.Fatalf("model request = %+v", modelRequest)
	}
	if len(modelRequest.Messages) != 2 || modelRequest.Messages[0].Role != "system" || modelRequest.Messages[1].Role != "user" || modelRequest.Messages[1].Content != "Why is a publication barrier useful?" {
		t.Fatalf("model context = %+v", modelRequest.Messages)
	}

	var runStatus, jobStatus, outputMessageID, role, content string
	if err := api.db.Pool().QueryRow(ctx, `
		select status, output_message_id
		from agent_runs where id = $1`, admittedBody.RunID).
		Scan(&runStatus, &outputMessageID); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select status from agent_jobs where run_id = $1`, admittedBody.RunID).Scan(&jobStatus); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select role, content from chat_messages where id = $1`, outputMessageID).Scan(&role, &content); err != nil {
		t.Fatal(err)
	}
	if runStatus != "completed" || jobStatus != "succeeded" || outputMessageID != "msg_worker_answer" {
		t.Fatalf("terminal state run=%s job=%s output=%s", runStatus, jobStatus, outputMessageID)
	}
	if role != "assistant" || content != "It makes provisional output durable exactly once." {
		t.Fatalf("published message role=%q content=%q", role, content)
	}
}

func TestControllerExecutesBifrostActionBatchAndPublishesFinal(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "controller-actions@example.com")
	const messageID = "0190cdd2-5f2d-7ad8-b3f5-1b588788c072"
	admitted := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id": messageID, "content": "Calculate two values, then summarize.", "time_zone": "Asia/Shanghai",
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
		t.Fatalf("claim = %+v ok=%t err=%v", claimed, ok, err)
	}

	modelCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls++
		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages []struct {
				Role       string `json:"role"`
				Content    string `json:"content"`
				ToolCallID string `json:"tool_call_id"`
				ToolCalls  []struct {
					ID       string `json:"id"`
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch modelCalls {
		case 1:
			if len(request.Tools) != 2 || request.Tools[0].Function.Name != "calculate" || request.Tools[1].Function.Name != "current_time" || len(request.Messages) != 2 {
				t.Fatalf("first model request = %+v", request)
			}
			_, _ = w.Write([]byte(`{
				"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[
					{"id":"provider-a","type":"function","function":{"name":"calculate","arguments":"{\"operation\":\"add\",\"operands\":[\"12.5\",\"3.2\"]}"}},
					{"id":"provider-b","type":"function","function":{"name":"calculate","arguments":"{\"operation\":\"multiply\",\"operands\":[\"4\",\"5\"]}"}}
				]},"finish_reason":"tool_calls"}]
			}`))
		case 2:
			if len(request.Messages) != 5 || len(request.Messages[2].ToolCalls) != 2 ||
				request.Messages[2].ToolCalls[0].ID != "decision:1/action:0" ||
				request.Messages[2].ToolCalls[1].ID != "decision:1/action:1" ||
				request.Messages[3].Role != "tool" || request.Messages[3].ToolCallID != "decision:1/action:0" ||
				request.Messages[4].Role != "tool" || request.Messages[4].ToolCallID != "decision:1/action:1" {
				t.Fatalf("reconstructed model request = %+v", request.Messages)
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"12.5 + 3.2 = 15.7, and 4 × 5 = 20."},"finish_reason":"stop"}]}`))
		default:
			t.Fatalf("unexpected model call %d", modelCalls)
		}
	}))
	defer upstream.Close()

	runtime := agent.NewPostgresRuntime(api.db.Pool(), agent.BareSystemPrompt, func() string { return "msg_controller_actions" })
	registry, err := agent.NewActionRegistry(agent.NewCalculateAction(), agent.NewCurrentTimeAction(nil))
	if err != nil {
		t.Fatal(err)
	}
	controller := agent.NewController(runtime, models.NewBifrostClient(upstream.URL, upstream.Client(), 2048), registry)
	if err := controller.Execute(ctx, attemptFromClaim(claimed)); err != nil {
		t.Fatal(err)
	}

	var runStatus, jobStatus, outputID, content string
	var checkpointKinds []string
	if err := api.db.Pool().QueryRow(ctx, `
		select r.status, j.status, r.output_message_id, m.content
		from agent_runs r
		join agent_jobs j on j.run_id = r.id
		join chat_messages m on m.id = r.output_message_id
		where r.id = $1`, admittedBody.RunID).Scan(&runStatus, &jobStatus, &outputID, &content); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `
		select array_agg(kind order by sequence_no)
		from agent_run_checkpoints where run_id = $1`, admittedBody.RunID).Scan(&checkpointKinds); err != nil {
		t.Fatal(err)
	}
	if runStatus != "completed" || jobStatus != "succeeded" || outputID != "msg_controller_actions" || content != "12.5 + 3.2 = 15.7, and 4 × 5 = 20." {
		t.Fatalf("terminal state = %s/%s/%s/%q", runStatus, jobStatus, outputID, content)
	}
	wantKinds := []string{"action_proposal", "action_result", "action_result", "final_draft"}
	if len(checkpointKinds) != len(wantKinds) {
		t.Fatalf("checkpoint kinds = %v", checkpointKinds)
	}
	for index := range wantKinds {
		if checkpointKinds[index] != wantKinds[index] {
			t.Fatalf("checkpoint kinds = %v, want %v", checkpointKinds, wantKinds)
		}
	}
}

func TestWorkerPersistsTerminalBifrostFailureWithoutAssistantMessage(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "worker-failure@example.com")
	admitted := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id":      "0190cdd2-5f2d-7ad8-b3f5-1b588788c006",
		"content": "This provider call will fail.",
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
	if err != nil || !ok || claimed.RunID != admittedBody.RunID {
		t.Fatalf("claim = %+v ok=%v err=%v", claimed, ok, err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "provider unavailable", http.StatusServiceUnavailable)
	}))
	defer upstream.Close()
	model := models.NewBifrostClient(upstream.URL, upstream.Client(), 2048)
	runtime := agent.NewPostgresRuntime(api.db.Pool(), agent.BareSystemPrompt, nil)
	loop := agent.NewLoop(runtime, runtime, agent.NewModelRunner(model), runtime)
	if err := loop.Execute(ctx, attemptFromClaim(claimed)); err == nil {
		t.Fatal("failed Bifrost call returned nil error")
	}

	var runStatus, jobStatus, errorCode string
	var outputMessageID *string
	if err := api.db.Pool().QueryRow(ctx, `select status, output_message_id, error_code from agent_runs where id = $1`, claimed.RunID).Scan(&runStatus, &outputMessageID, &errorCode); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select status from agent_jobs where run_id = $1`, claimed.RunID).Scan(&jobStatus); err != nil {
		t.Fatal(err)
	}
	var assistantCount int
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from chat_messages where chat_id = $1 and role = 'assistant'`, chatID).Scan(&assistantCount); err != nil {
		t.Fatal(err)
	}
	if runStatus != "failed" || jobStatus != "failed" || errorCode != "model_unavailable" || outputMessageID != nil || assistantCount != 0 {
		t.Fatalf("failure state run=%q job=%q code=%q output=%v assistants=%d", runStatus, jobStatus, errorCode, outputMessageID, assistantCount)
	}

	next := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id":      "0190cdd2-5f2d-7ad8-b3f5-1b588788c007",
		"content": "A new turn can now be admitted.",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if next.Code != http.StatusAccepted {
		t.Fatalf("admission after terminal failure = %d, body = %s", next.Code, next.Body.String())
	}
}

func TestContextBuilderSelectsTheLatestTwentyDurableMessages(t *testing.T) {
	api, _, _, chatID := newChatFixture(t, "context-window@example.com")
	ctx := context.Background()
	for i := 1; i <= 25; i++ {
		role := "user"
		if i%2 == 0 {
			role = "assistant"
		}
		if _, err := api.db.Pool().Exec(ctx, `
			insert into chat_messages(id, chat_id, role, content, created_at)
			values($1, $2, $3, $4, timestamp with time zone '2026-07-14 00:00:00+00' + ($5 * interval '1 second'))`,
			messageIDForIndex(i), chatID, role, messageContentForIndex(i), i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := api.db.Pool().Exec(ctx, `
		insert into chat_messages(id, chat_id, role, content, created_at)
		values('msg_context_later', $1, 'user', 'must-not-enter-earlier-run', timestamp with time zone '2026-07-14 00:01:00+00')`, chatID); err != nil {
		t.Fatal(err)
	}

	runtime := agent.NewPostgresRuntime(api.db.Pool(), "Bounded system prompt.", nil)
	request, err := runtime.Build(ctx, agent.Execution{ChatID: chatID, InputMessageID: messageIDForIndex(25), Model: "aliyun/qwen-flash"})
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) != 21 || request.Messages[0].Role != "system" || request.Messages[0].Content != "Bounded system prompt." {
		t.Fatalf("context size/system = %d/%+v", len(request.Messages), request.Messages[0])
	}
	if request.Messages[1].Content != "message-06" || request.Messages[20].Content != "message-25" {
		t.Fatalf("context bounds = %q ... %q", request.Messages[1].Content, request.Messages[20].Content)
	}
}

func TestPublicationRejectsAnExpiredAttemptAfterTheJobIsReclaimed(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "publish-fence@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c022")
	ctx := context.Background()
	queue := jobs.NewQueue(api.db.Pool())
	first, ok, err := queue.ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("first claim = %+v ok=%v err=%v", first, ok, err)
	}
	if _, err := api.db.Pool().Exec(ctx, `update agent_jobs set lease_expires_at = now() - interval '1 second' where id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	second, ok, err := queue.ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("reclaim = %+v ok=%v err=%v", second, ok, err)
	}
	runtime := agent.NewPostgresRuntime(api.db.Pool(), "System prompt.", func() string { return "msg_fenced_answer" })
	result := models.ChatResult{Text: "Only the current attempt may publish.", FinishReason: "stop"}
	if err := runtime.Publish(ctx, attemptFromClaim(first), result); !errors.Is(err, agent.ErrLeaseLost) {
		t.Fatalf("stale publish error = %v, want ErrLeaseLost", err)
	}
	if err := runtime.Fail(ctx, attemptFromClaim(first), "model_unavailable"); !errors.Is(err, agent.ErrLeaseLost) {
		t.Fatalf("stale failure error = %v, want ErrLeaseLost", err)
	}
	if err := runtime.Publish(ctx, attemptFromClaim(second), result); err != nil {
		t.Fatal(err)
	}
	var assistantCount int
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from chat_messages where chat_id = $1 and role = 'assistant'`, chatID).Scan(&assistantCount); err != nil {
		t.Fatal(err)
	}
	if assistantCount != 1 {
		t.Fatalf("assistant count = %d, want exactly one for run %q", assistantCount, runID)
	}
}

func TestPublicationAcknowledgementLossReconcilesCommittedSuccess(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "publish-reconcile@example.com")
	admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c025")
	ctx := context.Background()
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim = %+v ok=%v err=%v", claimed, ok, err)
	}
	ackLost := errors.New("simulated commit acknowledgement loss")
	firstCommit := true
	runtime := agent.NewPostgresRuntime(
		api.db.Pool(),
		"System prompt.",
		func() string { return "msg_reconciled_answer" },
		agent.WithCommitFunc(func(ctx context.Context, tx pgx.Tx) error {
			err := tx.Commit(ctx)
			if firstCommit && err == nil {
				firstCommit = false
				return ackLost
			}
			return err
		}),
	)
	if err := runtime.Publish(ctx, attemptFromClaim(claimed), models.ChatResult{Text: "Committed exactly once.", FinishReason: "stop"}); err != nil {
		t.Fatalf("reconciled publication = %v", err)
	}
	var runStatus string
	var assistants int
	if err := api.db.Pool().QueryRow(ctx, `select status from agent_runs where id=$1`, claimed.RunID).Scan(&runStatus); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from chat_messages where chat_id=$1 and role='assistant'`, chatID).Scan(&assistants); err != nil {
		t.Fatal(err)
	}
	if runStatus != "completed" || assistants != 1 {
		t.Fatalf("reconciled state run=%q assistants=%d", runStatus, assistants)
	}
}

func attemptFromClaim(job jobs.ClaimedJob) agent.Attempt {
	return agent.Attempt{JobID: job.ID, RunID: job.RunID, AttemptNo: job.AttemptNo, LeaseToken: job.LeaseToken}
}

func messageIDForIndex(index int) string {
	return "msg_context_" + messageContentForIndex(index)
}

func messageContentForIndex(index int) string {
	if index < 10 {
		return "message-0" + string(rune('0'+index))
	}
	return "message-" + string(rune('0'+index/10)) + string(rune('0'+index%10))
}

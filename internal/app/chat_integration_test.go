package app_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/app"
)

func TestPrivateChatCreateIsIdempotentAndRestorable(t *testing.T) {
	api := newTestAPI(t)
	sessionCookie, csrfCookie := api.registerWithCSRF(t, "chat-owner@example.com")

	createNotebook := api.postJSONWithCookieAndCSRF(t, "/api/v1/notebooks", map[string]any{"title": "Agent Loop"}, sessionCookie, csrfCookie, csrfCookie.Value, "chat-notebook")
	if createNotebook.Code != http.StatusCreated {
		t.Fatalf("create notebook status = %d, body = %s", createNotebook.Code, createNotebook.Body.String())
	}
	var notebookBody struct {
		Notebook struct {
			ID string `json:"id"`
		} `json:"notebook"`
	}
	decodeBody(t, createNotebook, &notebookBody)
	path := "/api/v1/notebooks/" + notebookBody.Notebook.ID + "/chats"

	created := api.postJSONWithCookieAndCSRF(t, path, map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "first-private-chat")
	if created.Code != http.StatusCreated {
		t.Fatalf("create chat status = %d, body = %s", created.Code, created.Body.String())
	}
	var createdBody struct {
		Chat struct {
			ID         string `json:"id"`
			NotebookID string `json:"notebook_id"`
			Title      string `json:"title"`
		} `json:"chat"`
	}
	decodeBody(t, created, &createdBody)
	if createdBody.Chat.ID == "" || createdBody.Chat.NotebookID != notebookBody.Notebook.ID || createdBody.Chat.Title == "" {
		t.Fatalf("unexpected created chat: %+v", createdBody.Chat)
	}

	replayed := api.postJSONWithCookieAndCSRF(t, path, map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "first-private-chat")
	if replayed.Code != http.StatusOK {
		t.Fatalf("replay chat status = %d, body = %s", replayed.Code, replayed.Body.String())
	}
	var replayedBody struct {
		Chat struct {
			ID string `json:"id"`
		} `json:"chat"`
	}
	decodeBody(t, replayed, &replayedBody)
	if replayedBody.Chat.ID != createdBody.Chat.ID {
		t.Fatalf("replayed chat id = %q, want %q", replayedBody.Chat.ID, createdBody.Chat.ID)
	}

	listed := api.getWithCookie(t, path, sessionCookie)
	if listed.Code != http.StatusOK {
		t.Fatalf("list chats status = %d, body = %s", listed.Code, listed.Body.String())
	}
	var listedBody struct {
		Chats []struct {
			ID string `json:"id"`
		} `json:"chats"`
	}
	decodeBody(t, listed, &listedBody)
	if len(listedBody.Chats) != 1 || listedBody.Chats[0].ID != createdBody.Chat.ID {
		t.Fatalf("listed chats = %+v, want one %q", listedBody.Chats, createdBody.Chat.ID)
	}
}

func TestMessageAdmissionAtomicallyCreatesQueuedRunAndJob(t *testing.T) {
	api := newTestAPI(t)
	sessionCookie, csrfCookie := api.registerWithCSRF(t, "admission@example.com")

	createNotebook := api.postJSONWithCookieAndCSRF(t, "/api/v1/notebooks", map[string]any{"title": "Durable Admission"}, sessionCookie, csrfCookie, csrfCookie.Value, "admission-notebook")
	if createNotebook.Code != http.StatusCreated {
		t.Fatalf("create notebook status = %d, body = %s", createNotebook.Code, createNotebook.Body.String())
	}
	var notebookBody struct {
		Notebook struct {
			ID string `json:"id"`
		} `json:"notebook"`
	}
	decodeBody(t, createNotebook, &notebookBody)

	createChat := api.postJSONWithCookieAndCSRF(t, "/api/v1/notebooks/"+notebookBody.Notebook.ID+"/chats", map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "admission-chat")
	if createChat.Code != http.StatusCreated {
		t.Fatalf("create chat status = %d, body = %s", createChat.Code, createChat.Body.String())
	}
	var chatBody struct {
		Chat struct {
			ID string `json:"id"`
		} `json:"chat"`
	}
	decodeBody(t, createChat, &chatBody)

	const messageID = "0190cdd2-5f2d-7ad8-b3f5-1b588788c001"
	admitted := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatBody.Chat.ID+"/messages", map[string]any{
		"id":        messageID,
		"content":   "Explain why durable admission matters.",
		"time_zone": "Asia/Shanghai",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if admitted.Code != http.StatusAccepted {
		t.Fatalf("admit message status = %d, body = %s", admitted.Code, admitted.Body.String())
	}
	var admittedBody struct {
		MessageID string `json:"message_id"`
		RunID     string `json:"run_id"`
		Status    string `json:"status"`
	}
	decodeBody(t, admitted, &admittedBody)
	if admittedBody.MessageID != messageID || admittedBody.RunID == "" || admittedBody.Status != "queued" {
		t.Fatalf("unexpected admission payload: %+v", admittedBody)
	}

	ctx := context.Background()
	var messageCount, runCount, jobCount int
	var runStatus, jobStatus, inputMessageID, jobRunID, timeZone string
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from chat_messages where id = $1`, messageID).Scan(&messageCount); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select count(*), min(status), min(input_message_id), min(time_zone) from agent_runs where id = $1`, admittedBody.RunID).Scan(&runCount, &runStatus, &inputMessageID, &timeZone); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select count(*), min(status), min(run_id) from agent_jobs where run_id = $1`, admittedBody.RunID).Scan(&jobCount, &jobStatus, &jobRunID); err != nil {
		t.Fatal(err)
	}
	if messageCount != 1 || runCount != 1 || jobCount != 1 || runStatus != "queued" || jobStatus != "queued" || inputMessageID != messageID || jobRunID != admittedBody.RunID || timeZone != "Asia/Shanghai" {
		t.Fatalf("durable admission message=%d run=%d/%s/%s/%s job=%d/%s/%s", messageCount, runCount, runStatus, inputMessageID, timeZone, jobCount, jobStatus, jobRunID)
	}
}

func TestMessageAdmissionPinsConfiguredRunBudgets(t *testing.T) {
	api := newTestAPI(t)
	api.server = app.NewServer(app.Config{
		CookieSecure: false,
		AgentRun: agent.RunConfig{
			ActionDecisionLimit:    2,
			FinalDecisionLimit:     1,
			ActionLimit:            5,
			ActionBatchLimit:       2,
			ActionResultByteLimit:  8 * 1024,
			ActionResultsByteLimit: 24 * 1024,
			Deadline:               3 * time.Minute,
		},
	}, api.db)
	api.handler = api.server.Handler()
	sessionCookie, csrfCookie := api.registerWithCSRF(t, "configured-admission@example.com")

	createdNotebook := api.postJSONWithCookieAndCSRF(t, "/api/v1/notebooks", map[string]any{"title": "Configured Agent"}, sessionCookie, csrfCookie, csrfCookie.Value, "configured-notebook")
	var notebookBody struct {
		Notebook struct {
			ID string `json:"id"`
		} `json:"notebook"`
	}
	decodeBody(t, createdNotebook, &notebookBody)
	createdChat := api.postJSONWithCookieAndCSRF(t, "/api/v1/notebooks/"+notebookBody.Notebook.ID+"/chats", map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "configured-chat")
	var chatBody struct {
		Chat struct {
			ID string `json:"id"`
		} `json:"chat"`
	}
	decodeBody(t, createdChat, &chatBody)

	admittedAfter := time.Now().UTC()
	response := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatBody.Chat.ID+"/messages", map[string]any{
		"id": "0190cdd2-5f2d-7ad8-b3f5-1b588788c063", "content": "Pin configured limits.", "time_zone": "Europe/Paris",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("admission status = %d, body = %s", response.Code, response.Body.String())
	}
	var body struct {
		RunID string `json:"run_id"`
	}
	decodeBody(t, response, &body)
	var deadlineAt time.Time
	var decisions, finals, actions, batch, resultBytes, resultsBytes int
	if err := api.db.Pool().QueryRow(context.Background(), `
		select deadline_at, action_decision_limit, final_decision_limit, action_limit,
			action_batch_limit, action_result_byte_limit, action_results_byte_limit
		from agent_runs where id = $1`, body.RunID).Scan(
		&deadlineAt, &decisions, &finals, &actions, &batch, &resultBytes, &resultsBytes,
	); err != nil {
		t.Fatal(err)
	}
	if decisions != 2 || finals != 1 || actions != 5 || batch != 2 || resultBytes != 8*1024 || resultsBytes != 24*1024 {
		t.Fatalf("pinned config=%d+%d/%d/%d/%d/%d", decisions, finals, actions, batch, resultBytes, resultsBytes)
	}
	if deadlineAt.Before(admittedAfter.Add(2*time.Minute+50*time.Second)) || deadlineAt.After(admittedAfter.Add(3*time.Minute+10*time.Second)) {
		t.Fatalf("deadline_at=%s, want approximately three minutes after admission", deadlineAt)
	}
}

func TestMessageAdmissionExpiresOverdueRunBeforeActiveSlotCheck(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "admission-deadline@example.com")
	oldRunID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c074")
	ctx := context.Background()
	if _, err := api.db.Pool().Exec(ctx, `update agent_runs set deadline_at = now() - interval '1 second' where id = $1`, oldRunID); err != nil {
		t.Fatal(err)
	}

	response := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id": "0190cdd2-5f2d-7ad8-b3f5-1b588788c075", "content": "Start after the old deadline.",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("admission status = %d, body = %s", response.Code, response.Body.String())
	}
	var body struct {
		RunID string `json:"run_id"`
	}
	decodeBody(t, response, &body)
	var oldStatus, oldError, newStatus string
	if err := api.db.Pool().QueryRow(ctx, `select status, error_code from agent_runs where id = $1`, oldRunID).Scan(&oldStatus, &oldError); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select status from agent_runs where id = $1`, body.RunID).Scan(&newStatus); err != nil {
		t.Fatal(err)
	}
	if oldStatus != "failed" || oldError != "run_deadline_exceeded" || newStatus != "queued" {
		t.Fatalf("old/new state = %s/%s -> %s", oldStatus, oldError, newStatus)
	}
}

func TestMessageAdmissionReusesTheOriginalRunForTheSameCommand(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "admission-replay@example.com")
	const messageID = "0190cdd2-5f2d-7ad8-b3f5-1b588788c002"
	payload := map[string]any{"id": messageID, "content": "Explain idempotent admission.", "time_zone": "Asia/Tokyo"}

	first := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", payload, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if first.Code != http.StatusAccepted {
		t.Fatalf("first admission status = %d, body = %s", first.Code, first.Body.String())
	}
	var firstBody struct {
		RunID string `json:"run_id"`
	}
	decodeBody(t, first, &firstBody)

	replayed := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id": messageID, "content": "Explain idempotent admission.", "time_zone": "America/New_York",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if replayed.Code != http.StatusAccepted {
		t.Fatalf("replayed admission status = %d, body = %s", replayed.Code, replayed.Body.String())
	}
	var replayedBody struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
	}
	decodeBody(t, replayed, &replayedBody)
	if replayedBody.RunID != firstBody.RunID || replayedBody.Status != "queued" {
		t.Fatalf("replayed admission = %+v, want original run %q", replayedBody, firstBody.RunID)
	}
	var pinnedTimeZone string
	if err := api.db.Pool().QueryRow(context.Background(), `select time_zone from agent_runs where id = $1`, firstBody.RunID).Scan(&pinnedTimeZone); err != nil {
		t.Fatal(err)
	}
	if pinnedTimeZone != "Asia/Tokyo" {
		t.Fatalf("replayed admission changed pinned time zone to %q", pinnedTimeZone)
	}

	mismatch := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id":      messageID,
		"content": "Different content",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if mismatch.Code != http.StatusConflict || decodeError(t, mismatch).Code != "message_id_conflict" {
		t.Fatalf("message id mismatch status = %d, body = %s", mismatch.Code, mismatch.Body.String())
	}

	var messages, runs, jobs int
	ctx := context.Background()
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from chat_messages`).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_jobs`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if messages != 1 || runs != 1 || jobs != 1 {
		t.Fatalf("replay created duplicate rows messages=%d runs=%d jobs=%d", messages, runs, jobs)
	}
}

func TestMessageAdmissionFallsBackToUTCForInvalidTimeZone(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "admission-time-zone-fallback@example.com")
	response := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id":        "0190cdd2-5f2d-7ad8-b3f5-1b588788c062",
		"content":   "Use a safe time zone fallback.",
		"time_zone": "Mars/Olympus_Mons",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("admission status = %d, body = %s", response.Code, response.Body.String())
	}
	var body struct {
		RunID string `json:"run_id"`
	}
	decodeBody(t, response, &body)
	var timeZone string
	if err := api.db.Pool().QueryRow(context.Background(), `select time_zone from agent_runs where id = $1`, body.RunID).Scan(&timeZone); err != nil {
		t.Fatal(err)
	}
	if timeZone != "UTC" {
		t.Fatalf("invalid browser time zone pinned as %q, want UTC", timeZone)
	}
}

func TestMessageAdmissionRejectsASecondActiveRunWithoutCreatingRows(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "active-run@example.com")
	first := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id":      "0190cdd2-5f2d-7ad8-b3f5-1b588788c003",
		"content": "First active turn",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if first.Code != http.StatusAccepted {
		t.Fatalf("first admission status = %d, body = %s", first.Code, first.Body.String())
	}

	second := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id":      "0190cdd2-5f2d-7ad8-b3f5-1b588788c004",
		"content": "Second turn must not queue",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if second.Code != http.StatusConflict || decodeError(t, second).Code != "active_run_conflict" {
		t.Fatalf("second admission status = %d, body = %s", second.Code, second.Body.String())
	}

	ctx := context.Background()
	var messages, runs, jobs int
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from chat_messages`).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_jobs`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if messages != 1 || runs != 1 || jobs != 1 {
		t.Fatalf("active conflict left rows messages=%d runs=%d jobs=%d", messages, runs, jobs)
	}
}

func TestConcurrentDistinctMessagesAdmitExactlyOneRunWithoutOrphans(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "concurrent-admission@example.com")
	start := make(chan struct{})
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for index, messageID := range []string{
		"0190cdd2-5f2d-7ad8-b3f5-1b588788c010",
		"0190cdd2-5f2d-7ad8-b3f5-1b588788c011",
	} {
		wg.Add(1)
		go func(index int, messageID string) {
			defer wg.Done()
			<-start
			response := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
				"id":      messageID,
				"content": "Concurrent turn " + string(rune('1'+index)),
			}, sessionCookie, csrfCookie, csrfCookie.Value, "")
			results <- response.Code
		}(index, messageID)
	}
	close(start)
	wg.Wait()
	close(results)

	statuses := map[int]int{}
	for status := range results {
		statuses[status]++
	}
	if statuses[http.StatusAccepted] != 1 || statuses[http.StatusConflict] != 1 {
		t.Fatalf("concurrent admission statuses = %+v, want one 202 and one 409", statuses)
	}

	ctx := context.Background()
	var messages, runs, jobs int
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from chat_messages where chat_id = $1`, chatID).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_runs where chat_id = $1`, chatID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_jobs`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if messages != 1 || runs != 1 || jobs != 1 {
		t.Fatalf("concurrent admission rows messages=%d runs=%d jobs=%d", messages, runs, jobs)
	}
}

func TestPrivateChatRunAndMessageEndpointsDoNotLeakAcrossUsers(t *testing.T) {
	api, ownerSession, ownerCSRF, chatID := newChatFixture(t, "private-chat-owner@example.com")
	intruderSession, intruderCSRF := api.registerWithCSRF(t, "private-chat-intruder@example.com")

	ownerAdmission := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id":      "0190cdd2-5f2d-7ad8-b3f5-1b588788c012",
		"content": "This turn is private.",
	}, ownerSession, ownerCSRF, ownerCSRF.Value, "")
	if ownerAdmission.Code != http.StatusAccepted {
		t.Fatalf("owner admission status = %d, body = %s", ownerAdmission.Code, ownerAdmission.Body.String())
	}
	var admitted struct {
		RunID string `json:"run_id"`
	}
	decodeBody(t, ownerAdmission, &admitted)

	for label, response := range map[string]struct {
		code int
		body string
	}{
		"snapshot": responseSummary(api.getWithCookie(t, "/api/v1/chats/"+chatID, intruderSession)),
		"run SSE":  responseSummary(api.getWithCookie(t, "/api/v1/agent-runs/"+admitted.RunID+"/events", intruderSession)),
		"admission": responseSummary(api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
			"id":      "0190cdd2-5f2d-7ad8-b3f5-1b588788c013",
			"content": "I must not enter this Chat.",
		}, intruderSession, intruderCSRF, intruderCSRF.Value, "")),
	} {
		if response.code != http.StatusNotFound {
			t.Fatalf("intruder %s status = %d, body = %s", label, response.code, response.body)
		}
	}

	var messages int
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from chat_messages where chat_id = $1`, chatID).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if messages != 1 {
		t.Fatalf("intruder mutation changed private Chat message count to %d", messages)
	}
}

func responseSummary(response *httptest.ResponseRecorder) struct {
	code int
	body string
} {
	return struct {
		code int
		body string
	}{code: response.Code, body: response.Body.String()}
}

func newChatFixture(t *testing.T, email string) (*testAPI, *http.Cookie, *http.Cookie, string) {
	t.Helper()
	api := newTestAPI(t)
	sessionCookie, csrfCookie := api.registerWithCSRF(t, email)
	createNotebook := api.postJSONWithCookieAndCSRF(t, "/api/v1/notebooks", map[string]any{"title": "Chat Fixture"}, sessionCookie, csrfCookie, csrfCookie.Value, "fixture-notebook")
	if createNotebook.Code != http.StatusCreated {
		t.Fatalf("create fixture notebook status = %d, body = %s", createNotebook.Code, createNotebook.Body.String())
	}
	var notebookBody struct {
		Notebook struct {
			ID string `json:"id"`
		} `json:"notebook"`
	}
	decodeBody(t, createNotebook, &notebookBody)
	createChat := api.postJSONWithCookieAndCSRF(t, "/api/v1/notebooks/"+notebookBody.Notebook.ID+"/chats", map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "fixture-chat")
	if createChat.Code != http.StatusCreated {
		t.Fatalf("create fixture chat status = %d, body = %s", createChat.Code, createChat.Body.String())
	}
	var chatBody struct {
		Chat struct {
			ID string `json:"id"`
		} `json:"chat"`
	}
	decodeBody(t, createChat, &chatBody)
	return api, sessionCookie, csrfCookie, chatBody.Chat.ID
}

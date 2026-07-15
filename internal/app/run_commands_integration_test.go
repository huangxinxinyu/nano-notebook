package app_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

func TestCancelQueuedRunIsDurableIdempotentAndReleasesTheActiveSlot(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "cancel-queued@example.com")
	const messageID = "0190cdd2-5f2d-7ad8-b3f5-1b588788c030"
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, messageID)
	path := "/api/v1/agent-runs/" + runID + "/cancel"

	for call := 1; call <= 2; call++ {
		response := api.postJSONWithCookieAndCSRF(t, path, map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "")
		if response.Code != http.StatusOK {
			t.Fatalf("cancel call %d status=%d body=%s", call, response.Code, response.Body.String())
		}
		var body struct {
			Run agent.RunSnapshot `json:"run"`
		}
		decodeBody(t, response, &body)
		if body.Run.ID != runID || body.Run.InputMessageID != messageID || body.Run.Status != "cancelled" {
			t.Fatalf("cancelled projection = %+v", body.Run)
		}
	}

	var runStatus, jobStatus string
	if err := api.db.Pool().QueryRow(context.Background(), `
		select r.status, j.status from agent_runs r join agent_jobs j on j.run_id = r.id where r.id = $1`, runID).
		Scan(&runStatus, &jobStatus); err != nil {
		t.Fatal(err)
	}
	if runStatus != "cancelled" || jobStatus != "cancelled" {
		t.Fatalf("durable cancel run=%q job=%q", runStatus, jobStatus)
	}
	next := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id": "0190cdd2-5f2d-7ad8-b3f5-1b588788c031", "content": "The active slot is free.",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if next.Code != http.StatusAccepted {
		t.Fatalf("admission after cancel = %d body=%s", next.Code, next.Body.String())
	}
}

func TestCancelRunningRunFencesLatePublication(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "cancel-running@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c032")
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(context.Background())
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%v err=%v", claimed, ok, err)
	}
	cancelled := api.postJSONWithCookieAndCSRF(t, "/api/v1/agent-runs/"+runID+"/cancel", map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if cancelled.Code != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", cancelled.Code, cancelled.Body.String())
	}
	runtime := agent.NewPostgresRuntime(api.db.Pool(), "System prompt.", func() string { return "msg_too_late" })
	if err := runtime.Publish(context.Background(), attemptFromClaim(claimed), models.ChatResult{Text: "Too late", FinishReason: "stop"}); !errors.Is(err, agent.ErrLeaseLost) {
		t.Fatalf("late publish error=%v, want ErrLeaseLost", err)
	}
	var assistants int
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from chat_messages where chat_id=$1 and role='assistant'`, chatID).Scan(&assistants); err != nil {
		t.Fatal(err)
	}
	if assistants != 0 {
		t.Fatalf("late publication inserted %d assistant messages", assistants)
	}
}

func TestCompletedRunCannotBeCancelled(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "cancel-completed@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c033")
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(context.Background())
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%v err=%v", claimed, ok, err)
	}
	runtime := agent.NewPostgresRuntime(api.db.Pool(), "System prompt.", func() string { return "msg_publish_first" })
	if err := runtime.Publish(context.Background(), attemptFromClaim(claimed), models.ChatResult{Text: "Published first", FinishReason: "stop"}); err != nil {
		t.Fatal(err)
	}
	response := api.postJSONWithCookieAndCSRF(t, "/api/v1/agent-runs/"+runID+"/cancel", map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if response.Code != http.StatusConflict || decodeError(t, response).Code != "run_not_cancellable" {
		t.Fatalf("cancel completed status=%d body=%s", response.Code, response.Body.String())
	}
	retry := api.postJSONWithCookieAndCSRF(t, "/api/v1/agent-runs/"+runID+"/retry", map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "retry-completed")
	if retry.Code != http.StatusConflict || decodeError(t, retry).Code != "run_not_retryable" {
		t.Fatalf("retry completed status=%d body=%s", retry.Code, retry.Body.String())
	}
}

func TestRetryCreatesANewRunForTheSameMessageAndReplaysByIdempotencyKey(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "retry-run@example.com")
	const messageID = "0190cdd2-5f2d-7ad8-b3f5-1b588788c034"
	oldRunID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, messageID)
	cancelled := api.postJSONWithCookieAndCSRF(t, "/api/v1/agent-runs/"+oldRunID+"/cancel", map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if cancelled.Code != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", cancelled.Code, cancelled.Body.String())
	}
	path := "/api/v1/agent-runs/" + oldRunID + "/retry"
	first := api.postJSONWithCookieAndCSRF(t, path, map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "retry-command-one")
	if first.Code != http.StatusAccepted {
		t.Fatalf("retry status=%d body=%s", first.Code, first.Body.String())
	}
	var firstBody struct {
		Run agent.RunSnapshot `json:"run"`
	}
	decodeBody(t, first, &firstBody)
	if firstBody.Run.ID == "" || firstBody.Run.ID == oldRunID || firstBody.Run.InputMessageID != messageID || firstBody.Run.Status != "queued" {
		t.Fatalf("new retry Run=%+v old=%q", firstBody.Run, oldRunID)
	}
	replay := api.postJSONWithCookieAndCSRF(t, path, map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "retry-command-one")
	if replay.Code != http.StatusAccepted {
		t.Fatalf("retry replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	var replayBody struct {
		Run agent.RunSnapshot `json:"run"`
	}
	decodeBody(t, replay, &replayBody)
	if replayBody.Run.ID != firstBody.Run.ID {
		t.Fatalf("retry replay Run=%q want=%q", replayBody.Run.ID, firstBody.Run.ID)
	}
	admissionReplay := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id": messageID, "content": "Exercise lease semantics.",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if admissionReplay.Code != http.StatusAccepted {
		t.Fatalf("message replay status=%d body=%s", admissionReplay.Code, admissionReplay.Body.String())
	}
	var admissionBody struct {
		RunID string `json:"run_id"`
	}
	decodeBody(t, admissionReplay, &admissionBody)
	if admissionBody.RunID != oldRunID {
		t.Fatalf("message replay Run=%q want original admission Run=%q", admissionBody.RunID, oldRunID)
	}
	var messages, runs, jobCount int
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from chat_messages where chat_id=$1`, chatID).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from agent_runs where input_message_id=$1`, messageID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from agent_jobs j join agent_runs r on r.id=j.run_id where r.input_message_id=$1`, messageID).Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if messages != 1 || runs != 2 || jobCount != 2 {
		t.Fatalf("retry rows messages=%d runs=%d jobs=%d", messages, runs, jobCount)
	}
}

func TestCancelledRunSSEIsTerminalAndRetryRejectsHistoricalInput(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "cancelled-sse@example.com")
	oldRunID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c035")
	cancelled := api.postJSONWithCookieAndCSRF(t, "/api/v1/agent-runs/"+oldRunID+"/cancel", map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if cancelled.Code != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", cancelled.Code, cancelled.Body.String())
	}
	events := api.getWithCookie(t, "/api/v1/agent-runs/"+oldRunID+"/events", sessionCookie)
	if events.Code != http.StatusOK || !strings.Contains(events.Body.String(), `"status":"cancelled"`) {
		t.Fatalf("cancelled SSE status=%d body=%s", events.Code, events.Body.String())
	}
	next := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id": "0190cdd2-5f2d-7ad8-b3f5-1b588788c036", "content": "Advance the conversation.",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if next.Code != http.StatusAccepted {
		t.Fatalf("next admission=%d body=%s", next.Code, next.Body.String())
	}
	retry := api.postJSONWithCookieAndCSRF(t, "/api/v1/agent-runs/"+oldRunID+"/retry", map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "historical-retry")
	if retry.Code != http.StatusConflict || decodeError(t, retry).Code != "retry_not_latest" {
		t.Fatalf("historical retry status=%d body=%s", retry.Code, retry.Body.String())
	}
}

func TestRunCommandsRequireCSRFAndDoNotLeakAcrossUsers(t *testing.T) {
	api, ownerSession, ownerCSRF, chatID := newChatFixture(t, "run-command-owner@example.com")
	intruderSession, intruderCSRF := api.registerWithCSRF(t, "run-command-intruder@example.com")
	runID := admitRunForLeaseTest(t, api, ownerSession, ownerCSRF, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c037")
	path := "/api/v1/agent-runs/" + runID + "/cancel"
	missingCSRF := api.postJSONWithCookieAndCSRF(t, path, map[string]any{}, ownerSession, nil, "", "")
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("cancel without CSRF status=%d body=%s", missingCSRF.Code, missingCSRF.Body.String())
	}
	intruder := api.postJSONWithCookieAndCSRF(t, path, map[string]any{}, intruderSession, intruderCSRF, intruderCSRF.Value, "")
	if intruder.Code != http.StatusNotFound {
		t.Fatalf("intruder cancel status=%d body=%s", intruder.Code, intruder.Body.String())
	}
	owner := api.postJSONWithCookieAndCSRF(t, path, map[string]any{}, ownerSession, ownerCSRF, ownerCSRF.Value, "")
	if owner.Code != http.StatusOK {
		t.Fatalf("owner cancel status=%d body=%s", owner.Code, owner.Body.String())
	}
	retryWithoutKey := api.postJSONWithCookieAndCSRF(t, "/api/v1/agent-runs/"+runID+"/retry", map[string]any{}, ownerSession, ownerCSRF, ownerCSRF.Value, "")
	if retryWithoutKey.Code != http.StatusBadRequest || decodeError(t, retryWithoutKey).Code != "idempotency_required" {
		t.Fatalf("retry without key status=%d body=%s", retryWithoutKey.Code, retryWithoutKey.Body.String())
	}
}

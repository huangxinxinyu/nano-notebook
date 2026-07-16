package app_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
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
	retryPayload := map[string]any{"time_zone": "Europe/London"}
	first := api.postJSONWithCookieAndCSRF(t, path, retryPayload, sessionCookie, csrfCookie, csrfCookie.Value, "retry-command-one")
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
	replay := api.postJSONWithCookieAndCSRF(t, path, retryPayload, sessionCookie, csrfCookie, csrfCookie.Value, "retry-command-one")
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
	var retryTimeZone string
	if err := api.db.Pool().QueryRow(context.Background(), `select time_zone from agent_runs where id=$1`, firstBody.Run.ID).Scan(&retryTimeZone); err != nil {
		t.Fatal(err)
	}
	if retryTimeZone != "Europe/London" {
		t.Fatalf("retry time zone=%q want Europe/London", retryTimeZone)
	}
	mismatch := api.postJSONWithCookieAndCSRF(t, path, map[string]any{"time_zone": "Asia/Tokyo"}, sessionCookie, csrfCookie, csrfCookie.Value, "retry-command-one")
	if mismatch.Code != http.StatusConflict || decodeError(t, mismatch).Code != "idempotency_mismatch" {
		t.Fatalf("retry time-zone mismatch status=%d body=%s", mismatch.Code, mismatch.Body.String())
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

func TestRetryExpiresOverdueSourceBeforeRetryabilityAndActiveSlotChecks(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "retry-deadline@example.com")
	sourceRunID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c076")
	ctx := context.Background()
	if _, err := api.db.Pool().Exec(ctx, `update agent_runs set deadline_at = now() - interval '1 second' where id = $1`, sourceRunID); err != nil {
		t.Fatal(err)
	}

	response := api.postJSONWithCookieAndCSRF(t, "/api/v1/agent-runs/"+sourceRunID+"/retry", map[string]any{
		"time_zone": "Europe/London",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "retry-overdue-source")
	if response.Code != http.StatusAccepted {
		t.Fatalf("retry status = %d, body = %s", response.Code, response.Body.String())
	}
	var body struct {
		Run agent.RunSnapshot `json:"run"`
	}
	decodeBody(t, response, &body)
	var sourceStatus, sourceError, retryStatus string
	var retryCheckpoints int
	if err := api.db.Pool().QueryRow(ctx, `select status, error_code from agent_runs where id = $1`, sourceRunID).Scan(&sourceStatus, &sourceError); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select status from agent_runs where id = $1`, body.Run.ID).Scan(&retryStatus); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_run_checkpoints where run_id = $1`, body.Run.ID).Scan(&retryCheckpoints); err != nil {
		t.Fatal(err)
	}
	if sourceStatus != "failed" || sourceError != "run_deadline_exceeded" || retryStatus != "queued" || retryCheckpoints != 0 {
		t.Fatalf("source/retry state = %s/%s -> %s checkpoints=%d", sourceStatus, sourceError, retryStatus, retryCheckpoints)
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

func TestConcurrentCancelAndPublishCommitOneConsistentTerminalOutcome(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "cancel-publish-race@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c038")
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(context.Background())
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%v err=%v", claimed, ok, err)
	}
	runtime := agent.NewPostgresRuntime(api.db.Pool(), "System prompt.", func() string { return "msg_cancel_publish_race" })
	start := make(chan struct{})
	var cancelResponseStatus int
	var cancelErrorCode string
	var publishErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		response := api.postJSONWithCookieAndCSRF(t, "/api/v1/agent-runs/"+runID+"/cancel", map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "")
		cancelResponseStatus = response.Code
		if response.Code != http.StatusOK {
			cancelErrorCode = decodeError(t, response).Code
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		publishErr = runtime.Publish(context.Background(), attemptFromClaim(claimed), models.ChatResult{Text: "Race result", FinishReason: "stop"})
	}()
	close(start)
	wg.Wait()

	var runStatus, jobStatus string
	var assistants int
	if err := api.db.Pool().QueryRow(context.Background(), `
		select r.status, j.status from agent_runs r join agent_jobs j on j.run_id=r.id where r.id=$1`, runID).
		Scan(&runStatus, &jobStatus); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from chat_messages where chat_id=$1 and role='assistant'`, chatID).Scan(&assistants); err != nil {
		t.Fatal(err)
	}
	if cancelResponseStatus == http.StatusOK {
		if !errors.Is(publishErr, agent.ErrLeaseLost) || runStatus != "cancelled" || jobStatus != "cancelled" || assistants != 0 {
			t.Fatalf("cancel-first outcome cancel=%d publish=%v run=%q job=%q assistants=%d", cancelResponseStatus, publishErr, runStatus, jobStatus, assistants)
		}
		return
	}
	if cancelResponseStatus != http.StatusConflict || cancelErrorCode != "run_not_cancellable" || publishErr != nil || runStatus != "completed" || jobStatus != "succeeded" || assistants != 1 {
		t.Fatalf("publish-first outcome cancel=%d/%q publish=%v run=%q job=%q assistants=%d", cancelResponseStatus, cancelErrorCode, publishErr, runStatus, jobStatus, assistants)
	}
}

func TestRetryRejectsAnotherChatsActiveRunWithoutPartialRows(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "retry-active-conflict@example.com")
	const messageID = "0190cdd2-5f2d-7ad8-b3f5-1b588788c039"
	sourceRunID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, messageID)
	cancelled := api.postJSONWithCookieAndCSRF(t, "/api/v1/agent-runs/"+sourceRunID+"/cancel", map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if cancelled.Code != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", cancelled.Code, cancelled.Body.String())
	}
	var notebookID string
	if err := api.db.Pool().QueryRow(context.Background(), `select notebook_id from chat_chats where id=$1`, chatID).Scan(&notebookID); err != nil {
		t.Fatal(err)
	}
	created := api.postJSONWithCookieAndCSRF(t, "/api/v1/notebooks/"+notebookID+"/chats", map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "retry-conflict-chat")
	if created.Code != http.StatusCreated {
		t.Fatalf("second chat status=%d body=%s", created.Code, created.Body.String())
	}
	var createdBody struct {
		Chat struct {
			ID string `json:"id"`
		} `json:"chat"`
	}
	decodeBody(t, created, &createdBody)
	active := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+createdBody.Chat.ID+"/messages", map[string]any{
		"id": "0190cdd2-5f2d-7ad8-b3f5-1b588788c040", "content": "Occupy the global slot.",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if active.Code != http.StatusAccepted {
		t.Fatalf("active admission=%d body=%s", active.Code, active.Body.String())
	}
	retry := api.postJSONWithCookieAndCSRF(t, "/api/v1/agent-runs/"+sourceRunID+"/retry", map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "retry-active-conflict")
	if retry.Code != http.StatusConflict || decodeError(t, retry).Code != "active_run_conflict" {
		t.Fatalf("retry active conflict status=%d body=%s", retry.Code, retry.Body.String())
	}
	var sourceRuns, retryKeys int
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from agent_runs where input_message_id=$1`, messageID).Scan(&sourceRuns); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from platform_idempotency_keys where action='retry_agent_run' and key='retry-active-conflict'`).Scan(&retryKeys); err != nil {
		t.Fatal(err)
	}
	if sourceRuns != 1 || retryKeys != 0 {
		t.Fatalf("rejected retry left source runs=%d idempotency rows=%d", sourceRuns, retryKeys)
	}
}

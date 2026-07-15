package app_test

import (
	"net/http"
	"testing"
)

func TestChatSnapshotRestoresDurableMessagesAndTheActiveRun(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "chat-snapshot@example.com")
	const messageID = "0190cdd2-5f2d-7ad8-b3f5-1b588788c006"
	admitted := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id":      messageID,
		"content": "Restore this durable message.",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if admitted.Code != http.StatusAccepted {
		t.Fatalf("admission status = %d, body = %s", admitted.Code, admitted.Body.String())
	}
	var admittedBody struct {
		RunID string `json:"run_id"`
	}
	decodeBody(t, admitted, &admittedBody)

	snapshot := api.getWithCookie(t, "/api/v1/chats/"+chatID, sessionCookie)
	if snapshot.Code != http.StatusOK {
		t.Fatalf("chat snapshot status = %d, body = %s", snapshot.Code, snapshot.Body.String())
	}
	var body struct {
		Chat struct {
			ID string `json:"id"`
		} `json:"chat"`
		Messages []struct {
			ID      string `json:"id"`
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		ActiveRun *struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"active_run"`
	}
	decodeBody(t, snapshot, &body)
	if body.Chat.ID != chatID || len(body.Messages) != 1 || body.Messages[0].ID != messageID || body.Messages[0].Role != "user" || body.Messages[0].Content != "Restore this durable message." {
		t.Fatalf("unexpected durable snapshot: %+v", body)
	}
	if body.ActiveRun == nil || body.ActiveRun.ID != admittedBody.RunID || body.ActiveRun.Status != "queued" {
		t.Fatalf("active Run = %+v, want queued %q", body.ActiveRun, admittedBody.RunID)
	}
}

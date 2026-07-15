package app_test

import (
	"context"
	"net/http"
	"testing"
)

func TestChatSnapshotRestoresDurableMessagesAndTheNewestRunForEachInput(t *testing.T) {
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

	ctx := context.Background()
	if _, err := api.db.Pool().Exec(ctx, `
		update agent_runs
		set status = 'cancelled', finished_at = now(), updated_at = now()
		where id = $1`, admittedBody.RunID); err != nil {
		t.Fatal(err)
	}
	const retryRunID = "run_snapshot_retry"
	if _, err := api.db.Pool().Exec(ctx, `
		insert into agent_runs(id, user_id, chat_id, input_message_id, status, model, prompt_version, created_at)
		select $1, user_id, chat_id, input_message_id, 'queued', model, prompt_version, created_at + interval '1 second'
		from agent_runs where id = $2`, retryRunID, admittedBody.RunID); err != nil {
		t.Fatal(err)
	}

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
		Runs []struct {
			ID             string `json:"id"`
			InputMessageID string `json:"input_message_id"`
			Status         string `json:"status"`
		} `json:"runs"`
	}
	decodeBody(t, snapshot, &body)
	if body.Chat.ID != chatID || len(body.Messages) != 1 || body.Messages[0].ID != messageID || body.Messages[0].Role != "user" || body.Messages[0].Content != "Restore this durable message." {
		t.Fatalf("unexpected durable snapshot: %+v", body)
	}
	if len(body.Runs) != 1 || body.Runs[0].ID != retryRunID || body.Runs[0].InputMessageID != messageID || body.Runs[0].Status != "queued" {
		t.Fatalf("Run projections = %+v, want newest queued Run %q for input %q", body.Runs, retryRunID, messageID)
	}
}

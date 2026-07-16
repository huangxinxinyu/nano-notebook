package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
)

func TestPublicationAndDeadlineExpiryHonorTheFirstTerminalCommit(t *testing.T) {
	t.Run("publication first", func(t *testing.T) {
		api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "publish-before-expiry@example.com")
		runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c082")
		ctx := context.Background()
		claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
		if err != nil || !ok {
			t.Fatalf("claim=%+v ok=%t err=%v", claimed, ok, err)
		}
		runtime := agent.NewPostgresRuntime(api.db.Pool(), "", func() string { return "msg_before_expiry" })
		draft := appendFinalDraft(t, runtime, attemptFromClaim(claimed), "Publication won the terminal boundary.")
		if err := runtime.PublishFinal(ctx, attemptFromClaim(claimed), draft); err != nil {
			t.Fatal(err)
		}
		if _, err := api.db.Pool().Exec(ctx, `update agent_runs set deadline_at = now() - interval '1 second' where id = $1`, runID); err != nil {
			t.Fatal(err)
		}
		if expired := expireRunInTransaction(t, api, runID); expired != 0 {
			t.Fatalf("completed Run expired count=%d", expired)
		}
		assertTerminalRunState(t, api, runID, chatID, "completed", "succeeded", "", 1)
	})

	t.Run("expiry first", func(t *testing.T) {
		api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "expiry-before-publish@example.com")
		runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c083")
		ctx := context.Background()
		claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
		if err != nil || !ok {
			t.Fatalf("claim=%+v ok=%t err=%v", claimed, ok, err)
		}
		runtime := agent.NewPostgresRuntime(api.db.Pool(), "", func() string { return "msg_after_expiry" })
		draft := appendFinalDraft(t, runtime, attemptFromClaim(claimed), "Expiry must fence this accepted draft.")
		if _, err := api.db.Pool().Exec(ctx, `update agent_runs set deadline_at = now() - interval '1 second' where id = $1`, runID); err != nil {
			t.Fatal(err)
		}
		if expired := expireRunInTransaction(t, api, runID); expired != 1 {
			t.Fatalf("expired count=%d, want 1", expired)
		}
		if err := runtime.PublishFinal(ctx, attemptFromClaim(claimed), draft); !errors.Is(err, agent.ErrLeaseLost) {
			t.Fatalf("late publication error=%v, want lease lost", err)
		}
		assertCheckpointCount(t, api, runID, 1)
		assertTerminalRunState(t, api, runID, chatID, "failed", "failed", "run_deadline_exceeded", 0)
	})
}

func expireRunInTransaction(t *testing.T, api *testAPI, runID string) int {
	t.Helper()
	ctx := context.Background()
	tx, err := api.db.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	expired, err := agent.NewStore(tx).ExpireIfOverdue(ctx, "", runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return expired
}

func assertTerminalRunState(t *testing.T, api *testAPI, runID, chatID, wantRun, wantJob, wantError string, wantAssistants int) {
	t.Helper()
	ctx := context.Background()
	var runStatus, jobStatus string
	var errorCode *string
	if err := api.db.Pool().QueryRow(ctx, `
		select r.status, j.status, r.error_code
		from agent_runs r join agent_jobs j on j.run_id = r.id
		where r.id = $1`, runID).Scan(&runStatus, &jobStatus, &errorCode); err != nil {
		t.Fatal(err)
	}
	var assistants int
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from chat_messages where chat_id = $1 and role = 'assistant'`, chatID).Scan(&assistants); err != nil {
		t.Fatal(err)
	}
	gotError := ""
	if errorCode != nil {
		gotError = *errorCode
	}
	if runStatus != wantRun || jobStatus != wantJob || gotError != wantError || assistants != wantAssistants {
		t.Fatalf("terminal state=%s/%s/%q assistants=%d, want %s/%s/%q/%d", runStatus, jobStatus, gotError, assistants, wantRun, wantJob, wantError, wantAssistants)
	}
}

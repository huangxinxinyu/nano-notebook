package app_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/jackc/pgx/v5"
)

func TestCheckpointAppendIsIdempotentAndRejectsIdentityConflict(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "checkpoint-idempotency@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c060")
	ctx := context.Background()
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim = %+v ok=%t err=%v", claimed, ok, err)
	}
	runtime := agent.NewPostgresRuntime(api.db.Pool(), "", nil)
	firstPending, err := agent.NewProposalCheckpoint(1, models.ActionProposalBatch{Actions: []models.ActionProposal{
		{Name: "calculate", Input: []byte(`{"operation":"add","operands":["1","2"]}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}

	first, err := runtime.AppendCheckpoint(ctx, attemptFromClaim(claimed), firstPending)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := runtime.AppendCheckpoint(ctx, attemptFromClaim(claimed), firstPending)
	if err != nil {
		t.Fatal(err)
	}
	if first.SequenceNo != 1 || replayed.SequenceNo != first.SequenceNo || replayed.IdentityKey != first.IdentityKey {
		t.Fatalf("first=%+v replayed=%+v", first, replayed)
	}

	conflict, err := agent.NewProposalCheckpoint(1, models.ActionProposalBatch{Actions: []models.ActionProposal{
		{Name: "current_time", Input: []byte(`{"time_zone":"Asia/Shanghai"}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AppendCheckpoint(ctx, attemptFromClaim(claimed), conflict); !errors.Is(err, agent.ErrCheckpointInvalid) {
		t.Fatalf("conflicting append error = %v, want checkpoint_invalid", err)
	}

	var count int
	var storedHash string
	if err := api.db.Pool().QueryRow(ctx, `
		select count(*), max(payload_sha256)
		from agent_run_checkpoints
		where run_id = $1`, runID).Scan(&count, &storedHash); err != nil {
		t.Fatal(err)
	}
	if count != 1 || storedHash != firstPending.PayloadSHA256 {
		t.Fatalf("stored checkpoints=%d hash=%q, want one original %q", count, storedHash, firstPending.PayloadSHA256)
	}
	if runID != claimed.RunID {
		t.Fatalf("claimed Run = %q, want %q", claimed.RunID, runID)
	}
}

func TestCheckpointAppendReconcilesCommittedWriteAfterAcknowledgementLoss(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "checkpoint-uncertain-commit@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c061")
	ctx := context.Background()
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim = %+v ok=%t err=%v", claimed, ok, err)
	}
	commitCalls := 0
	runtime := agent.NewPostgresRuntime(api.db.Pool(), "", nil, agent.WithCommitFunc(func(ctx context.Context, tx pgx.Tx) error {
		commitCalls++
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		if commitCalls == 1 {
			return errors.New("simulated lost commit acknowledgement")
		}
		return nil
	}))
	pending, err := agent.NewFinalDraftCheckpoint(1, models.FinalDraft{Text: "The durable answer."})
	if err != nil {
		t.Fatal(err)
	}

	checkpoint, err := runtime.AppendCheckpoint(ctx, attemptFromClaim(claimed), pending)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.SequenceNo != 1 || commitCalls != 1 {
		t.Fatalf("checkpoint=%+v commit calls=%d", checkpoint, commitCalls)
	}
	var count int
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_run_checkpoints where run_id = $1`, runID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("checkpoint count = %d, want 1", count)
	}
}

func TestCheckpointPrefixLoadsFirstIncompleteActionAndDerivedConsumption(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "checkpoint-prefix@example.com")
	_ = admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c062")
	ctx := context.Background()
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim = %+v ok=%t err=%v", claimed, ok, err)
	}
	attempt := attemptFromClaim(claimed)
	runtime := agent.NewPostgresRuntime(api.db.Pool(), "", nil)
	proposal, err := agent.NewProposalCheckpoint(1, models.ActionProposalBatch{Actions: []models.ActionProposal{
		{Name: "current_time", Input: []byte(`{"time_zone":"UTC"}`)},
		{Name: "calculate", Input: []byte(`{"operation":"subtract","operands":["9","4"]}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AppendCheckpoint(ctx, attempt, proposal); err != nil {
		t.Fatal(err)
	}
	result, err := agent.NewActionResultCheckpoint(1, 0, "decision:1/action:0", agent.ActionResult{
		Status: agent.ActionSucceeded,
		Output: []byte(`{"local_time":"2026-07-16T17:40:00+08:00","observed_at":"2026-07-16T09:40:00Z","time_zone":"Asia/Shanghai","utc_offset_seconds":28800}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AppendCheckpoint(ctx, attempt, result); err != nil {
		t.Fatal(err)
	}

	prefix, err := runtime.LoadCheckpointPrefix(ctx, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if prefix.AcceptedDecisions != 1 || prefix.AcceptedActions != 2 || len(prefix.Proposals) != 1 {
		t.Fatalf("prefix consumption = %+v", prefix)
	}
	actions := prefix.Proposals[0].Actions
	if len(actions) != 2 || actions[0].Result == nil || actions[1].Result != nil {
		t.Fatalf("recovered Actions = %+v", actions)
	}
	if actions[0].ActionID != "decision:1/action:0" || actions[1].ActionID != "decision:1/action:1" {
		t.Fatalf("recovered Action IDs = %+v", actions)
	}
}

func TestCheckpointAppendRetriesAbsentWriteWhileAttemptRemainsCurrent(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "checkpoint-retry-absent@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c063")
	ctx := context.Background()
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim = %+v ok=%t err=%v", claimed, ok, err)
	}
	commitCalls := 0
	runtime := agent.NewPostgresRuntime(api.db.Pool(), "", nil, agent.WithCommitFunc(func(ctx context.Context, tx pgx.Tx) error {
		commitCalls++
		if commitCalls == 1 {
			return errors.New("simulated commit failure before commit")
		}
		return tx.Commit(ctx)
	}))
	pending, err := agent.NewFinalDraftCheckpoint(1, models.FinalDraft{Text: "Recovered by retry."})
	if err != nil {
		t.Fatal(err)
	}

	checkpoint, err := runtime.AppendCheckpoint(ctx, attemptFromClaim(claimed), pending)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.SequenceNo != 1 || commitCalls != 2 {
		t.Fatalf("checkpoint=%+v commit calls=%d", checkpoint, commitCalls)
	}
	var count int
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_run_checkpoints where run_id = $1`, runID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("checkpoint count = %d, want 1", count)
	}
}

func TestCheckpointReconciliationPreservesDeadlineExpiryReason(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "checkpoint-reconcile-deadline@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c068")
	ctx := context.Background()
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim = %+v ok=%t err=%v", claimed, ok, err)
	}
	commitCalls := 0
	runtime := agent.NewPostgresRuntime(api.db.Pool(), "", nil, agent.WithCommitFunc(func(ctx context.Context, tx pgx.Tx) error {
		commitCalls++
		if err := tx.Rollback(ctx); err != nil {
			return err
		}
		if _, err := api.db.Pool().Exec(ctx, `update agent_runs set deadline_at = now() - interval '1 second' where id = $1`, runID); err != nil {
			return err
		}
		return errors.New("simulated write failure at deadline")
	}))
	pending, err := agent.NewFinalDraftCheckpoint(1, models.FinalDraft{Text: "Too late."})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := runtime.AppendCheckpoint(ctx, attemptFromClaim(claimed), pending); !errors.Is(err, agent.ErrRunDeadlineExceeded) {
		t.Fatalf("reconciled deadline error = %v, want run_deadline_exceeded", err)
	}
	if commitCalls != 1 {
		t.Fatalf("commit calls = %d, want 1", commitCalls)
	}
	assertCheckpointCount(t, api, runID, 0)
}

func TestCheckpointAppendFencesExpiredLease(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "checkpoint-expired-lease@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c064")
	ctx := context.Background()
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim = %+v ok=%t err=%v", claimed, ok, err)
	}
	if _, err := api.db.Pool().Exec(ctx, `update agent_jobs set lease_expires_at = now() - interval '1 second' where id = $1`, claimed.ID); err != nil {
		t.Fatal(err)
	}
	pending, err := agent.NewFinalDraftCheckpoint(1, models.FinalDraft{Text: "Must not commit."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.NewPostgresRuntime(api.db.Pool(), "", nil).AppendCheckpoint(ctx, attemptFromClaim(claimed), pending); !errors.Is(err, agent.ErrLeaseLost) {
		t.Fatalf("expired-Lease append error = %v, want lease lost", err)
	}
	assertCheckpointCount(t, api, runID, 0)
}

func TestCheckpointAppendFencesExpiredRunDeadline(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "checkpoint-expired-deadline@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c065")
	ctx := context.Background()
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim = %+v ok=%t err=%v", claimed, ok, err)
	}
	if _, err := api.db.Pool().Exec(ctx, `update agent_runs set deadline_at = now() - interval '1 second' where id = $1`, runID); err != nil {
		t.Fatal(err)
	}
	pending, err := agent.NewFinalDraftCheckpoint(1, models.FinalDraft{Text: "Must not commit."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.NewPostgresRuntime(api.db.Pool(), "", nil).AppendCheckpoint(ctx, attemptFromClaim(claimed), pending); !errors.Is(err, agent.ErrRunDeadlineExceeded) {
		t.Fatalf("expired-deadline append error = %v, want deadline exceeded", err)
	}
	assertCheckpointCount(t, api, runID, 0)
}

func TestCheckpointPrefixRejectsCorruptStoredPayload(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "checkpoint-corrupt-prefix@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c066")
	ctx := context.Background()
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim = %+v ok=%t err=%v", claimed, ok, err)
	}
	attempt := attemptFromClaim(claimed)
	runtime := agent.NewPostgresRuntime(api.db.Pool(), "", nil)
	pending, err := agent.NewProposalCheckpoint(1, models.ActionProposalBatch{Actions: []models.ActionProposal{
		{Name: "calculate", Input: []byte(`{"operation":"add","operands":["1","2"]}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AppendCheckpoint(ctx, attempt, pending); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(ctx, `
		update agent_run_checkpoints
		set payload = jsonb_set(payload, '{actions,0,name}', '"current_time"'::jsonb)
		where run_id = $1 and sequence_no = 1`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.LoadCheckpointPrefix(ctx, attempt); !errors.Is(err, agent.ErrCheckpointInvalid) {
		t.Fatalf("corrupt prefix error = %v, want checkpoint_invalid", err)
	}
}

func TestCheckpointConcurrentReplayCommitsOneRow(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "checkpoint-concurrent-replay@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c067")
	ctx := context.Background()
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim = %+v ok=%t err=%v", claimed, ok, err)
	}
	attempt := attemptFromClaim(claimed)
	runtime := agent.NewPostgresRuntime(api.db.Pool(), "", nil)
	pending, err := agent.NewFinalDraftCheckpoint(1, models.FinalDraft{Text: "One durable draft."})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			checkpoint, err := runtime.AppendCheckpoint(ctx, attempt, pending)
			if err == nil && checkpoint.SequenceNo != 1 {
				err = errors.New("concurrent replay returned non-first sequence")
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertCheckpointCount(t, api, runID, 1)
}

func assertCheckpointCount(t *testing.T, api *testAPI, runID string, want int) {
	t.Helper()
	var count int
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from agent_run_checkpoints where run_id = $1`, runID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("checkpoint count = %d, want %d", count, want)
	}
}

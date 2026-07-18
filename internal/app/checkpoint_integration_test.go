package app_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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

func TestRuntimeLoadProjectsPinnedSprint3Configuration(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "runtime-pinned-config@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c069")
	ctx := context.Background()
	if _, err := api.db.Pool().Exec(ctx, `
		update agent_runs
		set time_zone = 'Asia/Tokyo', deadline_at = now() + interval '5 minutes',
			action_decision_limit = 2, final_decision_limit = 1,
			action_limit = 5, action_batch_limit = 2,
			action_result_byte_limit = 8192, action_results_byte_limit = 24576
		where id = $1`, runID); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim = %+v ok=%t err=%v", claimed, ok, err)
	}
	loadedAt := time.Now().UTC()
	execution, err := agent.NewPostgresRuntime(api.db.Pool(), "", nil).Load(ctx, attemptFromClaim(claimed))
	if err != nil {
		t.Fatal(err)
	}
	if execution.PromptVersion != "agent-bare-v1" || execution.TimeZone != "Asia/Tokyo" {
		t.Fatalf("prompt/time zone = %q/%q", execution.PromptVersion, execution.TimeZone)
	}
	if execution.ActionDecisionLimit != 2 || execution.FinalDecisionLimit != 1 ||
		execution.ActionLimit != 5 || execution.ActionBatchLimit != 2 ||
		execution.ActionResultByteLimit != 8192 || execution.ActionResultsByteLimit != 24576 {
		t.Fatalf("loaded limits = %+v", execution)
	}
	if execution.DeadlineAt.Before(loadedAt.Add(4*time.Minute+50*time.Second)) || execution.DeadlineAt.After(loadedAt.Add(5*time.Minute+10*time.Second)) {
		t.Fatalf("deadline = %s, want approximately five minutes after load", execution.DeadlineAt)
	}
}

func TestDecisionContextReconstructsCompletedCheckpointBatches(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "decision-context@example.com")
	_ = admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c070")
	ctx := context.Background()
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim = %+v ok=%t err=%v", claimed, ok, err)
	}
	attempt := attemptFromClaim(claimed)
	runtime := agent.NewPostgresRuntime(api.db.Pool(), "System prompt for checkpoint context.", nil)
	execution, err := runtime.Load(ctx, attempt)
	if err != nil {
		t.Fatal(err)
	}
	emptyPrefix, err := runtime.LoadCheckpointPrefix(ctx, attempt)
	if err != nil {
		t.Fatal(err)
	}
	emptyRequest, err := runtime.BuildDecisionRequest(ctx, execution, emptyPrefix, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(emptyRequest.Messages) != 2 {
		t.Fatalf("empty-prefix messages=%+v", emptyRequest.Messages)
	}
	proposal, err := agent.NewProposalCheckpoint(1, models.ActionProposalBatch{Actions: []models.ActionProposal{
		{Name: "current_time", Input: []byte(`{"time_zone":"Asia/Shanghai"}`)},
		{Name: "calculate", Input: []byte(`{"operation":"divide","operands":["1","0"]}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AppendCheckpoint(ctx, attempt, proposal); err != nil {
		t.Fatal(err)
	}
	firstResult, err := agent.NewActionResultCheckpoint(1, 0, "decision:1/action:0", agent.ActionResult{
		Status: agent.ActionSucceeded,
		Output: []byte(`{"local_time":"2026-07-16T17:40:00+08:00","observed_at":"2026-07-16T09:40:00Z","time_zone":"Asia/Shanghai","utc_offset_seconds":28800}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AppendCheckpoint(ctx, attempt, firstResult); err != nil {
		t.Fatal(err)
	}
	incomplete, err := runtime.LoadCheckpointPrefix(ctx, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.BuildDecisionRequest(ctx, execution, incomplete, nil); err == nil {
		t.Fatal("incomplete Action batch incorrectly produced another model request")
	}
	secondResult, err := agent.NewActionResultCheckpoint(1, 1, "decision:1/action:1", agent.ActionResult{
		Status: agent.ActionDomainError, ErrorCode: "division_by_zero",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AppendCheckpoint(ctx, attempt, secondResult); err != nil {
		t.Fatal(err)
	}
	prefix, err := runtime.LoadCheckpointPrefix(ctx, attempt)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := agent.NewActionRegistry(agent.NewCurrentTimeAction(nil), agent.NewCalculateAction())
	if err != nil {
		t.Fatal(err)
	}
	definitions := registry.Definitions(agent.ActionPolicy{RemainingActions: 6})

	request, err := runtime.BuildDecisionRequest(ctx, execution, prefix, definitions)
	if err != nil {
		t.Fatal(err)
	}
	if request.Model != "aliyun/qwen-flash" || len(request.Messages) != 5 {
		t.Fatalf("request model/messages = %q/%+v", request.Model, request.Messages)
	}
	if request.Messages[0].Role != models.RoleSystem || request.Messages[0].Content != "System prompt for checkpoint context." ||
		request.Messages[1].Role != models.RoleUser || request.Messages[1].Content != "Exercise lease semantics." {
		t.Fatalf("durable Chat context = %+v", request.Messages[:2])
	}
	proposalMessage := request.Messages[2]
	if proposalMessage.Role != models.RoleAssistant || proposalMessage.Content != "" || len(proposalMessage.ActionCalls) != 2 {
		t.Fatalf("proposal message = %+v", proposalMessage)
	}
	if proposalMessage.ActionCalls[0].ID != "decision:1/action:0" || proposalMessage.ActionCalls[0].Name != "current_time" ||
		proposalMessage.ActionCalls[1].ID != "decision:1/action:1" || proposalMessage.ActionCalls[1].Name != "calculate" {
		t.Fatalf("reconstructed calls = %+v", proposalMessage.ActionCalls)
	}
	if got := request.Messages[3]; got.Role != models.RoleAction || got.ActionCallID != "decision:1/action:0" || got.Content != string(firstResult.Payload) {
		t.Fatalf("success result message = %+v, payload=%s", got, firstResult.Payload)
	}
	if got := request.Messages[4]; got.Role != models.RoleAction || got.ActionCallID != "decision:1/action:1" || got.Content != string(secondResult.Payload) {
		t.Fatalf("domain result message = %+v, payload=%s", got, secondResult.Payload)
	}
	if len(request.ActionDefinitions) != 2 || request.ActionDefinitions[0].Name != "calculate" || request.ActionDefinitions[1].Name != "current_time" {
		t.Fatalf("Action definitions = %+v", request.ActionDefinitions)
	}
	final, err := agent.NewFinalDraftCheckpoint(2, models.FinalDraft{Text: "Accepted after reconstructed domain context."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AppendCheckpoint(ctx, attempt, final); err != nil {
		t.Fatal(err)
	}
	finalPrefix, err := runtime.LoadCheckpointPrefix(ctx, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.BuildDecisionRequest(ctx, execution, finalPrefix, definitions); err == nil {
		t.Fatal("Final prefix incorrectly produced another model request")
	}
}

func TestEveryCheckpointKindReconcilesCommitAcknowledgementLossAndRejectsConflict(t *testing.T) {
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
		return errors.New("simulated lost commit acknowledgement")
	}))
	proposal, err := agent.NewProposalCheckpoint(1, models.ActionProposalBatch{Actions: []models.ActionProposal{
		{Name: "calculate", Input: []byte(`{"operation":"add","operands":["1","2"]}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := agent.NewActionResultCheckpoint(1, 0, "decision:1/action:0", agent.ActionResult{
		Status: agent.ActionSucceeded, Output: []byte(`{"result":"3"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	final, err := agent.NewFinalDraftCheckpoint(2, models.FinalDraft{Text: "The durable answer."})
	if err != nil {
		t.Fatal(err)
	}
	for index, pending := range []agent.PendingCheckpoint{proposal, result, final} {
		checkpoint, err := runtime.AppendCheckpoint(ctx, attemptFromClaim(claimed), pending)
		if err != nil {
			t.Fatalf("append %s: %v", pending.Kind, err)
		}
		if checkpoint.SequenceNo != index+1 {
			t.Fatalf("%s checkpoint=%+v", pending.Kind, checkpoint)
		}
	}
	if commitCalls != 3 {
		t.Fatalf("commit calls=%d, want one uncertain commit per kind", commitCalls)
	}
	var acceptanceEvents int
	if err := api.db.Pool().QueryRow(ctx, `
		select count(*)
		from agentobs_outbox_records r
		join agent_trace_refs t on t.trace_id = r.trace_id
		where t.run_id = $1 and r.record_kind = 'event' and r.name = $2`, runID, agent.TraceEventCheckpointAccepted).Scan(&acceptanceEvents); err != nil {
		t.Fatal(err)
	}
	if acceptanceEvents != 3 {
		t.Fatalf("checkpoint acceptance Events = %d, want one per reconciled checkpoint", acceptanceEvents)
	}

	conflictingProposal, err := agent.NewProposalCheckpoint(1, models.ActionProposalBatch{Actions: []models.ActionProposal{
		{Name: "calculate", Input: []byte(`{"operation":"add","operands":["2","2"]}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	conflictingResult, err := agent.NewActionResultCheckpoint(1, 0, "decision:1/action:0", agent.ActionResult{
		Status: agent.ActionSucceeded, Output: []byte(`{"result":"4"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	conflictingFinal, err := agent.NewFinalDraftCheckpoint(2, models.FinalDraft{Text: "A contradictory answer."})
	if err != nil {
		t.Fatal(err)
	}
	for _, pending := range []agent.PendingCheckpoint{conflictingProposal, conflictingResult, conflictingFinal} {
		if _, err := runtime.AppendCheckpoint(ctx, attemptFromClaim(claimed), pending); !errors.Is(err, agent.ErrCheckpointInvalid) {
			t.Fatalf("conflicting %s error=%v, want checkpoint_invalid", pending.Kind, err)
		}
	}
	var count int
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_run_checkpoints where run_id = $1`, runID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("checkpoint count = %d, want 3", count)
	}
}

func TestReclaimedAttemptFencesEveryCheckpointKind(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "checkpoint-stale-kinds@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c079")
	ctx := context.Background()
	queue := jobs.NewQueue(api.db.Pool())
	first, ok, err := queue.ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("first claim=%+v ok=%t err=%v", first, ok, err)
	}
	if _, err := api.db.Pool().Exec(ctx, `update agent_jobs set lease_expires_at = now() - interval '1 second' where id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	second, ok, err := queue.ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("second claim=%+v ok=%t err=%v", second, ok, err)
	}
	proposal, err := agent.NewProposalCheckpoint(1, models.ActionProposalBatch{Actions: []models.ActionProposal{
		{Name: "calculate", Input: []byte(`{"operation":"add","operands":["1","2"]}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := agent.NewActionResultCheckpoint(1, 0, "decision:1/action:0", agent.ActionResult{
		Status: agent.ActionSucceeded, Output: []byte(`{"result":"3"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	final, err := agent.NewFinalDraftCheckpoint(1, models.FinalDraft{Text: "Stale draft."})
	if err != nil {
		t.Fatal(err)
	}
	runtime := agent.NewPostgresRuntime(api.db.Pool(), "", nil)
	for _, pending := range []agent.PendingCheckpoint{proposal, result, final} {
		if _, err := runtime.AppendCheckpoint(ctx, attemptFromClaim(first), pending); !errors.Is(err, agent.ErrLeaseLost) {
			t.Fatalf("stale %s append error=%v, want lease lost", pending.Kind, err)
		}
	}
	assertCheckpointCount(t, api, runID, 0)
	if _, err := runtime.AppendCheckpoint(ctx, attemptFromClaim(second), proposal); err != nil {
		t.Fatalf("current attempt append=%v", err)
	}
}

func TestDeletingParentRunCascadesItsInternalCheckpoints(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "checkpoint-cascade@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c080")
	ctx := context.Background()
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%t err=%v", claimed, ok, err)
	}
	pending, err := agent.NewFinalDraftCheckpoint(1, models.FinalDraft{Text: "Retained only with its Run."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.NewPostgresRuntime(api.db.Pool(), "", nil).AppendCheckpoint(ctx, attemptFromClaim(claimed), pending); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(ctx, `delete from agent_runs where id = $1`, runID); err != nil {
		t.Fatal(err)
	}
	assertCheckpointCount(t, api, runID, 0)
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
	var eventCount int
	if err := api.db.Pool().QueryRow(ctx, `
		select count(*)
		from agentobs_outbox_records r
		join agent_trace_refs t on t.trace_id = r.trace_id
		where t.run_id = $1 and r.record_kind = 'event' and r.name = $2`, runID, agent.TraceEventCheckpointAccepted).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("retried checkpoint acceptance Events = %d, want 1", eventCount)
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

func TestPostgresCheckpointLoaderRejectsPersistedGapsAndContradictoryRows(t *testing.T) {
	tests := []struct {
		name     string
		pending  func(t *testing.T) agent.PendingCheckpoint
		sequence int
	}{
		{
			name: "sequence gap",
			pending: func(t *testing.T) agent.PendingCheckpoint {
				t.Helper()
				pending, err := agent.NewFinalDraftCheckpoint(1, models.FinalDraft{Text: "Illegally stored after a gap."})
				if err != nil {
					t.Fatal(err)
				}
				return pending
			},
			sequence: 2,
		},
		{
			name: "result without proposal",
			pending: func(t *testing.T) agent.PendingCheckpoint {
				t.Helper()
				pending, err := agent.NewActionResultCheckpoint(1, 0, "decision:1/action:0", agent.ActionResult{
					Status: agent.ActionSucceeded, Output: []byte(`{"value":"3"}`),
				})
				if err != nil {
					t.Fatal(err)
				}
				return pending
			},
			sequence: 1,
		},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "checkpoint-illegal-row-"+string(rune('a'+index))+"@example.com")
			_ = admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c09"+string(rune('0'+index)))
			ctx := context.Background()
			claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
			if err != nil || !ok {
				t.Fatalf("claim=%+v ok=%t err=%v", claimed, ok, err)
			}
			pending := tt.pending(t)
			var actionIndex any
			if pending.ActionIndex != nil {
				actionIndex = *pending.ActionIndex
			}
			var actionID any
			if pending.ActionID != "" {
				actionID = pending.ActionID
			}
			if _, err := api.db.Pool().Exec(ctx, `
				insert into agent_run_checkpoints(
					run_id, sequence_no, identity_key, kind, decision_no,
					action_index, action_id, payload_version, payload, payload_sha256
				) values($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10)`,
				claimed.RunID, tt.sequence, pending.IdentityKey, string(pending.Kind), pending.DecisionNo,
				actionIndex, actionID, pending.PayloadVersion, []byte(pending.Payload), pending.PayloadSHA256,
			); err != nil {
				t.Fatal(err)
			}
			if _, err := agent.NewPostgresRuntime(api.db.Pool(), "", nil).LoadCheckpointPrefix(ctx, attemptFromClaim(claimed)); !errors.Is(err, agent.ErrCheckpointInvalid) {
				t.Fatalf("illegal PostgreSQL prefix error=%v, want checkpoint_invalid", err)
			}
		})
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

func TestConcurrentWorkersUnderDifferentLeasesAcceptOneMissingActionResult(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "checkpoint-concurrent-leases@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c084")
	ctx := context.Background()
	queue := jobs.NewQueue(api.db.Pool())
	first, ok, err := queue.ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("first claim=%+v ok=%t err=%v", first, ok, err)
	}
	runtime := agent.NewPostgresRuntime(api.db.Pool(), "", nil)
	proposal, err := agent.NewProposalCheckpoint(1, models.ActionProposalBatch{Actions: []models.ActionProposal{
		{Name: "calculate", Input: []byte(`{"operation":"add","operands":["1","2"]}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AppendCheckpoint(ctx, attemptFromClaim(first), proposal); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(ctx, `update agent_jobs set lease_expires_at = now() - interval '1 second' where id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	second, ok, err := queue.ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("second claim=%+v ok=%t err=%v", second, ok, err)
	}
	result, err := agent.NewActionResultCheckpoint(1, 0, "decision:1/action:0", agent.ActionResult{
		Status: agent.ActionSucceeded, Output: []byte(`{"value":"3"}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, claimed := range []jobs.ClaimedJob{first, second} {
		claimed := claimed
		go func() {
			<-start
			_, err := runtime.AppendCheckpoint(ctx, attemptFromClaim(claimed), result)
			errs <- err
		}()
	}
	close(start)
	firstErr, secondErr := <-errs, <-errs
	close(errs)
	accepted, fenced := 0, 0
	for _, err := range []error{firstErr, secondErr} {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, agent.ErrLeaseLost):
			fenced++
		default:
			t.Fatalf("concurrent append error=%v", err)
		}
	}
	if accepted != 1 || fenced != 1 {
		t.Fatalf("concurrent outcomes accepted=%d fenced=%d", accepted, fenced)
	}
	var resultCount int
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_run_checkpoints where run_id = $1 and kind = 'action_result'`, runID).Scan(&resultCount); err != nil {
		t.Fatal(err)
	}
	if resultCount != 1 {
		t.Fatalf("Action Result rows=%d, want 1", resultCount)
	}
}

func TestPublicationRequiresMatchingAcceptedFinalCheckpoint(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "checkpoint-publication-barrier@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c071")
	ctx := context.Background()
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim = %+v ok=%t err=%v", claimed, ok, err)
	}
	attempt := attemptFromClaim(claimed)
	runtime := agent.NewPostgresRuntime(api.db.Pool(), "", func() string { return "msg_checkpoint_final" })
	draft := models.FinalDraft{Text: "Accepted before publication."}

	if err := runtime.PublishFinal(ctx, attempt, draft); !errors.Is(err, agent.ErrCheckpointInvalid) {
		t.Fatalf("publication without Final Checkpoint error = %v, want checkpoint_invalid", err)
	}
	var assistants int
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from chat_messages where chat_id = $1 and role = 'assistant'`, chatID).Scan(&assistants); err != nil {
		t.Fatal(err)
	}
	if assistants != 0 {
		t.Fatalf("Assistant Messages before Final Checkpoint = %d", assistants)
	}
	final, err := agent.NewFinalDraftCheckpoint(1, draft)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AppendCheckpoint(ctx, attempt, final); err != nil {
		t.Fatal(err)
	}
	if err := runtime.PublishFinal(ctx, attempt, draft); err != nil {
		t.Fatal(err)
	}
	var runStatus, jobStatus, outputID, content string
	if err := api.db.Pool().QueryRow(ctx, `
		select r.status, j.status, r.output_message_id, m.content
		from agent_runs r
		join agent_jobs j on j.run_id = r.id
		join chat_messages m on m.id = r.output_message_id
		where r.id = $1`, runID).Scan(&runStatus, &jobStatus, &outputID, &content); err != nil {
		t.Fatal(err)
	}
	if runStatus != "completed" || jobStatus != "succeeded" || outputID != "msg_checkpoint_final" || content != draft.Text {
		t.Fatalf("publication state = %s/%s/%s/%q", runStatus, jobStatus, outputID, content)
	}
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

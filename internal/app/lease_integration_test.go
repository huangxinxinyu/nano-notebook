package app_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
)

func TestRetryableDispositionActivelyRequeuesWithAvailabilityFence(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "active-retry@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c090")
	ctx := context.Background()
	queue := jobs.NewQueue(api.db.Pool())
	claimed, ok, err := queue.ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%v err=%v", claimed, ok, err)
	}
	requested := agent.AttemptResolution{Disposition: agent.AttemptRetryable, ErrorCode: "model_unavailable", Backoff: time.Minute}
	actual, err := queue.ResolveAttempt(ctx, claimed, requested)
	if err != nil || actual != requested {
		t.Fatalf("resolution=%+v err=%v", actual, err)
	}
	var runStatus, jobStatus, lastError string
	var lease *string
	var availableAt time.Time
	if err := api.db.Pool().QueryRow(ctx, `
		select r.status,j.status,j.last_error_code,j.lease_token::text,j.available_at
		from agent_runs r join agent_jobs j on j.run_id=r.id where r.id=$1
	`, runID).Scan(&runStatus, &jobStatus, &lastError, &lease, &availableAt); err != nil {
		t.Fatal(err)
	}
	if runStatus != "queued" || jobStatus != "queued" || lastError != "model_unavailable" || lease != nil || !availableAt.After(time.Now().Add(50*time.Second)) {
		t.Fatalf("retry state=%s/%s code=%q lease=%v available=%s", runStatus, jobStatus, lastError, lease, availableAt)
	}
	if next, ok, err := queue.ClaimNext(ctx); err != nil || ok {
		t.Fatalf("early claim=%+v ok=%v err=%v", next, ok, err)
	}
	if _, err := api.db.Pool().Exec(ctx, `update agent_jobs set available_at=now()-interval '1 second' where run_id=$1`, runID); err != nil {
		t.Fatal(err)
	}
	next, ok, err := queue.ClaimNext(ctx)
	if err != nil || !ok || next.RunID != runID || next.AttemptNo != 2 {
		t.Fatalf("retry claim=%+v ok=%v err=%v", next, ok, err)
	}
}

func TestTerminalDispositionDoesNotWaitForLeaseExpiry(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "active-terminal@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c091")
	ctx := context.Background()
	queue := jobs.NewQueue(api.db.Pool())
	claimed, ok, err := queue.ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%v err=%v", claimed, ok, err)
	}
	actual, err := queue.ResolveAttempt(ctx, claimed, agent.AttemptResolution{Disposition: agent.AttemptTerminal, ErrorCode: "model_invalid_response"})
	if err != nil || actual.Disposition != agent.AttemptTerminal {
		t.Fatalf("resolution=%+v err=%v", actual, err)
	}
	var runStatus, jobStatus, runError, jobError string
	if err := api.db.Pool().QueryRow(ctx, `
		select r.status,j.status,r.error_code,j.last_error_code
		from agent_runs r join agent_jobs j on j.run_id=r.id where r.id=$1
	`, runID).Scan(&runStatus, &jobStatus, &runError, &jobError); err != nil {
		t.Fatal(err)
	}
	if runStatus != "failed" || jobStatus != "failed" || runError != "model_invalid_response" || jobError != runError {
		t.Fatalf("terminal state=%s/%s errors=%q/%q", runStatus, jobStatus, runError, jobError)
	}
}

func TestJobLeaseClaimHeartbeatAndReclaimFenceOlderAttempt(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "lease-reclaim@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c020")
	ctx := context.Background()
	queue := jobs.NewQueue(api.db.Pool())

	first, ok, err := queue.ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("first claim = %+v ok=%v err=%v", first, ok, err)
	}
	if first.RunID != runID || first.AttemptNo != 1 {
		t.Fatalf("first claim = %+v, want run %q attempt 1", first, runID)
	}
	if _, err := uuid.Parse(first.LeaseToken); err != nil {
		t.Fatalf("lease token %q is not a UUID: %v", first.LeaseToken, err)
	}
	assertLeaseRemaining(t, api, first.ID, 25*time.Second, 31*time.Second)

	if _, err := api.db.Pool().Exec(ctx, `update agent_jobs set lease_expires_at = now() + interval '2 seconds' where id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	heartbeat, err := queue.Heartbeat(ctx, first.ID, first.LeaseToken, 30*time.Second)
	if err != nil || !heartbeat {
		t.Fatalf("heartbeat current lease ok=%v err=%v", heartbeat, err)
	}
	assertLeaseRemaining(t, api, first.ID, 25*time.Second, 31*time.Second)
	if heartbeat, err := queue.Heartbeat(ctx, first.ID, uuid.NewString(), 30*time.Second); err != nil || heartbeat {
		t.Fatalf("heartbeat stale token ok=%v err=%v, want false", heartbeat, err)
	}

	if _, err := api.db.Pool().Exec(ctx, `update agent_jobs set lease_expires_at = now() - interval '1 second' where id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	second, ok, err := queue.ClaimNext(ctx)
	if err != nil || !ok || second.ID != first.ID || second.RunID != first.RunID || second.AttemptNo != 2 || second.LeaseToken == first.LeaseToken {
		t.Fatalf("reclaim = %+v ok=%v err=%v after first=%+v", second, ok, err, first)
	}
	if heartbeat, err := queue.Heartbeat(ctx, first.ID, first.LeaseToken, 30*time.Second); err != nil || heartbeat {
		t.Fatalf("old attempt heartbeat ok=%v err=%v, want fenced", heartbeat, err)
	}
}

func TestQueueExpiresOverdueRunBeforeClaim(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "queue-deadline@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c073")
	ctx := context.Background()
	if _, err := api.db.Pool().Exec(ctx, `
		update agent_trees set absolute_deadline = now() - interval '1 second'
		where id = (select tree_id from agent_runs where id = $1)
	`, runID); err != nil {
		t.Fatal(err)
	}

	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("overdue Run was claimed: %+v", claimed)
	}
	var runStatus, jobStatus, errorCode string
	var leaseToken *string
	if err := api.db.Pool().QueryRow(ctx, `
		select r.status, r.error_code, j.status, j.lease_token::text
		from agent_runs r join agent_jobs j on j.run_id = r.id
		where r.id = $1`, runID).Scan(&runStatus, &errorCode, &jobStatus, &leaseToken); err != nil {
		t.Fatal(err)
	}
	if runStatus != "failed" || jobStatus != "failed" || errorCode != "run_deadline_exceeded" || leaseToken != nil {
		t.Fatalf("expired state = %s/%s/%s lease=%v", runStatus, jobStatus, errorCode, leaseToken)
	}
}

func TestThirdExpiredAttemptFailsRunAndJobAsRecoveryExhausted(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "lease-exhausted@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c021")
	ctx := context.Background()
	queue := jobs.NewQueue(api.db.Pool())

	for attempt := 1; attempt <= 3; attempt++ {
		claimed, ok, err := queue.ClaimNext(ctx)
		if err != nil || !ok || claimed.AttemptNo != attempt {
			t.Fatalf("attempt %d claim = %+v ok=%v err=%v", attempt, claimed, ok, err)
		}
		if _, err := api.db.Pool().Exec(ctx, `update agent_jobs set lease_expires_at = now() - interval '1 second' where id = $1`, claimed.ID); err != nil {
			t.Fatal(err)
		}
	}
	if claimed, ok, err := queue.ClaimNext(ctx); err != nil || ok {
		t.Fatalf("claim after third expiry = %+v ok=%v err=%v, want no work", claimed, ok, err)
	}

	var runStatus, jobStatus, errorCode string
	var attemptNo int
	var token *string
	var expiry *time.Time
	if err := api.db.Pool().QueryRow(ctx, `
		select r.status, j.status, r.error_code, j.attempt_no, j.lease_token::text, j.lease_expires_at
		from agent_runs r join agent_jobs j on j.run_id = r.id
		where r.id = $1`, runID).Scan(&runStatus, &jobStatus, &errorCode, &attemptNo, &token, &expiry); err != nil {
		t.Fatal(err)
	}
	if runStatus != "failed" || jobStatus != "failed" || errorCode != "recovery_exhausted" || attemptNo != 3 || token != nil || expiry != nil {
		t.Fatalf("exhausted state run=%q job=%q code=%q attempt=%d token=%v expiry=%v", runStatus, jobStatus, errorCode, attemptNo, token, expiry)
	}
	var assistants int
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from chat_messages where chat_id = $1 and role = 'assistant'`, chatID).Scan(&assistants); err != nil {
		t.Fatal(err)
	}
	if assistants != 0 {
		t.Fatalf("recovery exhaustion published %d Assistant Messages", assistants)
	}
}

func TestJobExecutionStateConstraintRejectsLeaseAuthorityOnQueuedWork(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "lease-state-check@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c023")
	if _, err := api.db.Pool().Exec(context.Background(), `
		update agent_jobs set lease_token = $2::uuid where run_id = $1`, runID, uuid.NewString()); err == nil {
		t.Fatal("queued Job accepted a lease token without a running attempt")
	}
}

func TestGracefulLeaseReleaseMakesTheJobImmediatelyReclaimable(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "lease-release@example.com")
	admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c024")
	ctx := context.Background()
	queue := jobs.NewQueue(api.db.Pool())
	first, ok, err := queue.ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("first claim = %+v ok=%v err=%v", first, ok, err)
	}
	if released, err := queue.ReleaseLease(ctx, first.ID, uuid.NewString()); err != nil || released {
		t.Fatalf("stale release ok=%v err=%v, want false", released, err)
	}
	if released, err := queue.ReleaseLease(ctx, first.ID, first.LeaseToken); err != nil || !released {
		t.Fatalf("current release ok=%v err=%v, want true", released, err)
	}
	second, ok, err := queue.ClaimNext(ctx)
	if err != nil || !ok || second.ID != first.ID || second.AttemptNo != 2 || second.LeaseToken == first.LeaseToken {
		t.Fatalf("immediate reclaim = %+v ok=%v err=%v after %+v", second, ok, err, first)
	}
}

func TestConcurrentWorkersReclaimAnExpiredLeaseExactlyOnce(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "lease-concurrent-reclaim@example.com")
	admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c027")
	ctx := context.Background()
	queue := jobs.NewQueue(api.db.Pool())
	first, ok, err := queue.ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("first claim = %+v ok=%v err=%v", first, ok, err)
	}
	if _, err := api.db.Pool().Exec(ctx, `update agent_jobs set lease_expires_at=now()-interval '1 second' where id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan struct {
		job jobs.ClaimedJob
		ok  bool
		err error
	}, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			job, ok, err := queue.ClaimNext(ctx)
			results <- struct {
				job jobs.ClaimedJob
				ok  bool
				err error
			}{job: job, ok: ok, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	claimed := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.ok {
			claimed++
			if result.job.ID != first.ID || result.job.AttemptNo != 2 || result.job.LeaseToken == first.LeaseToken {
				t.Fatalf("concurrent reclaim = %+v after %+v", result.job, first)
			}
		}
	}
	if claimed != 1 {
		t.Fatalf("successful concurrent reclaims = %d, want exactly one", claimed)
	}
}

func admitRunForLeaseTest(t *testing.T, api *testAPI, sessionCookie, csrfCookie *http.Cookie, chatID, messageID string) string {
	t.Helper()
	response := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id": messageID, "content": "Exercise lease semantics.",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("admission status = %d, body = %s", response.Code, response.Body.String())
	}
	var body struct {
		RunID string `json:"run_id"`
	}
	decodeBody(t, response, &body)
	return body.RunID
}

func assertLeaseRemaining(t *testing.T, api *testAPI, jobID string, minimum, maximum time.Duration) {
	t.Helper()
	var remaining time.Duration
	if err := api.db.Pool().QueryRow(context.Background(), `select lease_expires_at - now() from agent_jobs where id = $1`, jobID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining < minimum || remaining > maximum {
		t.Fatalf("lease remaining = %s, want %s..%s", remaining, minimum, maximum)
	}
}

func TestWaitResumptionsDoNotSpendTheFailedAttemptBudget(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "wait-resumptions@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c0a1")
	ctx := context.Background()
	queue := jobs.NewQueue(api.db.Pool())
	// A wait_agent yield requeues the Job without a failure, as five
	// resumptions exceed any definition's attempt limit.
	for wait := 1; wait <= 5; wait++ {
		claimed, ok, err := queue.ClaimNext(ctx)
		if err != nil || !ok || claimed.AttemptNo != wait || claimed.FailedAttempts != 0 {
			t.Fatalf("wait %d claim=%+v ok=%v err=%v", wait, claimed, ok, err)
		}
		if _, err := api.db.Pool().Exec(ctx, `update agent_runs set status='queued' where id=$1`, runID); err != nil {
			t.Fatal(err)
		}
		if _, err := api.db.Pool().Exec(ctx, `update agent_jobs set status='queued',lease_token=null,lease_expires_at=null,available_at=now() where run_id=$1`, runID); err != nil {
			t.Fatal(err)
		}
	}
	retry := agent.AttemptResolution{Disposition: agent.AttemptRetryable, ErrorCode: "model_timeout", Backoff: time.Millisecond}
	var maxAttempts int
	for failure := 1; ; failure++ {
		if _, err := api.db.Pool().Exec(ctx, `update agent_jobs set available_at=now()-interval '1 second' where run_id=$1`, runID); err != nil {
			t.Fatal(err)
		}
		claimed, ok, err := queue.ClaimNext(ctx)
		if err != nil || !ok || claimed.FailedAttempts != failure-1 {
			t.Fatalf("failure %d claim=%+v ok=%v err=%v", failure, claimed, ok, err)
		}
		maxAttempts = claimed.MaxAttempts
		actual, err := queue.ResolveAttempt(ctx, claimed, retry)
		if err != nil {
			t.Fatal(err)
		}
		if failure < maxAttempts {
			if actual.Disposition != agent.AttemptRetryable {
				t.Fatalf("failure %d of %d after waits resolved %+v", failure, maxAttempts, actual)
			}
			continue
		}
		if actual.Disposition != agent.AttemptTerminal || actual.ErrorCode != "retry_exhausted" {
			t.Fatalf("failure %d of %d resolved %+v, want retry_exhausted", failure, maxAttempts, actual)
		}
		break
	}
	var attemptNo, failedAttempts int
	if err := api.db.Pool().QueryRow(ctx, `select attempt_no,failed_attempts from agent_jobs where run_id=$1`, runID).Scan(&attemptNo, &failedAttempts); err != nil {
		t.Fatal(err)
	}
	if attemptNo != 5+maxAttempts || failedAttempts != maxAttempts-1 {
		t.Fatalf("attempt_no=%d failed_attempts=%d max=%d", attemptNo, failedAttempts, maxAttempts)
	}
}

package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/semconv"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/jackc/pgx/v5"
)

func TestAdmissionAtomicallyStartsDurableTraceAndReplayDoesNotDuplicate(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-admission@example.com")
	const messageID = "0190cdd2-5f2d-7ad8-b3f5-1b588788c420"
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, messageID)
	ctx := context.Background()

	trace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
	if err != nil {
		t.Fatalf("Load admitted Trace: %v", err)
	}
	if trace.RunID != runID || trace.TraceID == "" || trace.RootSpanID == "" || len(trace.Records) != 2 {
		t.Fatalf("admitted Trace envelope/records = %#v", trace)
	}
	root, admitted := trace.Records[0], trace.Records[1]
	if root.Kind != agentobs.RecordSpanStarted || root.SpanID != trace.RootSpanID || root.ParentSpanID != "" || root.Name != agent.TraceSpanAgentExecution {
		t.Fatalf("admitted root = %#v", root)
	}
	if admitted.Kind != agentobs.RecordEvent || admitted.SpanID != trace.RootSpanID || admitted.Name != agent.TraceEventRunAdmitted {
		t.Fatalf("admitted Event = %#v", admitted)
	}

	replayedRunID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, messageID)
	if replayedRunID != runID {
		t.Fatalf("replay Run = %q, want %q", replayedRunID, runID)
	}
	replayed, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.TraceID != trace.TraceID || len(replayed.Records) != 2 {
		t.Fatalf("replay changed Trace = %#v", replayed)
	}
}

func TestClaimAndReclaimRecordAttemptTreeAndContinuesLink(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-attempts@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c422")
	ctx := context.Background()
	queue := jobs.NewQueue(api.db.Pool())

	first, ok, err := queue.ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("first claim = %#v ok=%t err=%v", first, ok, err)
	}
	firstTrace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstTrace.Records) != 3 {
		t.Fatalf("first claim records = %#v", firstTrace.Records)
	}
	firstAttempt := firstTrace.Records[2]
	if firstAttempt.Kind != agentobs.RecordSpanStarted || firstAttempt.Name != agent.TraceSpanJobAttempt || firstAttempt.ParentSpanID != firstTrace.RootSpanID {
		t.Fatalf("first Attempt = %#v", firstAttempt)
	}

	if _, err := api.db.Pool().Exec(ctx, `update agent_jobs set lease_expires_at = now() - interval '1 second' where id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	second, ok, err := queue.ClaimNext(ctx)
	if err != nil || !ok || second.AttemptNo != 2 {
		t.Fatalf("second claim = %#v ok=%t err=%v", second, ok, err)
	}
	trace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Records) != 6 {
		t.Fatalf("reclaim records = %#v", trace.Records)
	}
	leaseExpired, secondAttempt, continues := trace.Records[3], trace.Records[4], trace.Records[5]
	if leaseExpired.Kind != agentobs.RecordEvent || leaseExpired.SpanID != firstAttempt.SpanID || leaseExpired.Name != agent.TraceEventLeaseExpired {
		t.Fatalf("Lease expiry Event = %#v", leaseExpired)
	}
	if secondAttempt.Kind != agentobs.RecordSpanStarted || secondAttempt.ParentSpanID != trace.RootSpanID || secondAttempt.SpanID == firstAttempt.SpanID {
		t.Fatalf("second Attempt = %#v", secondAttempt)
	}
	if continues.Kind != agentobs.RecordLink || continues.Name != semconv.LinkContinues || continues.SpanID != secondAttempt.SpanID || continues.TargetTraceID != trace.TraceID || continues.TargetSpanID != firstAttempt.SpanID {
		t.Fatalf("continues Link = %#v", continues)
	}
	for _, record := range trace.Records {
		if record.Kind == agentobs.RecordSpanEnded && (record.SpanID == firstAttempt.SpanID || record.SpanID == secondAttempt.SpanID) {
			t.Fatalf("claim/reclaim fabricated Attempt completion: %#v", record)
		}
	}
}

func TestCancellationRecordsAuthoritativeEventsAndEndsRoot(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-cancel@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c423")
	response := api.postJSONWithCookieAndCSRF(t, "/api/v1/agent-runs/"+runID+"/cancel", map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if response.Code != http.StatusOK {
		t.Fatalf("cancel status = %d body=%s", response.Code, response.Body.String())
	}
	trace, err := agent.LoadDurableTraceByRun(context.Background(), api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Records) != 5 {
		t.Fatalf("cancel Trace records = %#v", trace.Records)
	}
	cancellation, terminal, rootEnd := trace.Records[2], trace.Records[3], trace.Records[4]
	if cancellation.Kind != agentobs.RecordEvent || cancellation.Name != agent.TraceEventCancellation || cancellation.SpanID != trace.RootSpanID {
		t.Fatalf("cancellation Event = %#v", cancellation)
	}
	if terminal.Kind != agentobs.RecordEvent || terminal.Name != agent.TraceEventRunTerminal {
		t.Fatalf("terminal Event = %#v", terminal)
	}
	if rootEnd.Kind != agentobs.RecordSpanEnded || rootEnd.Name != agent.TraceSpanAgentExecution || rootEnd.Status != agentobs.StatusCancelled || rootEnd.SpanID != trace.RootSpanID {
		t.Fatalf("cancelled root end = %#v", rootEnd)
	}
}

func TestDeadlineExpiryRecordsAuthoritativeEventsAndEndsRoot(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-deadline@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c424")
	ctx := context.Background()
	if _, err := api.db.Pool().Exec(ctx, `update agent_runs set deadline_at = now() - interval '1 second' where id = $1`, runID); err != nil {
		t.Fatal(err)
	}
	if claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx); err != nil || ok {
		t.Fatalf("deadline claim = %#v ok=%t err=%v", claimed, ok, err)
	}
	trace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Records) != 5 {
		t.Fatalf("deadline Trace records = %#v", trace.Records)
	}
	deadline, terminal, rootEnd := trace.Records[2], trace.Records[3], trace.Records[4]
	if deadline.Kind != agentobs.RecordEvent || deadline.Name != agent.TraceEventDeadlineExpired {
		t.Fatalf("deadline Event = %#v", deadline)
	}
	if terminal.Kind != agentobs.RecordEvent || terminal.Name != agent.TraceEventRunTerminal {
		t.Fatalf("deadline terminal Event = %#v", terminal)
	}
	if rootEnd.Kind != agentobs.RecordSpanEnded || rootEnd.Status != agentobs.StatusError {
		t.Fatalf("deadline root end = %#v", rootEnd)
	}
}

func TestRecoveryExhaustionRecordsLastLeaseLossAndEndsRoot(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-recovery-exhausted@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c425")
	ctx := context.Background()
	queue := jobs.NewQueue(api.db.Pool())
	for attempt := 1; attempt <= 3; attempt++ {
		claimed, ok, err := queue.ClaimNext(ctx)
		if err != nil || !ok || claimed.AttemptNo != attempt {
			t.Fatalf("attempt %d claim = %#v ok=%t err=%v", attempt, claimed, ok, err)
		}
		if _, err := api.db.Pool().Exec(ctx, `update agent_jobs set lease_expires_at = now() - interval '1 second' where id = $1`, claimed.ID); err != nil {
			t.Fatal(err)
		}
	}
	if claimed, ok, err := queue.ClaimNext(ctx); err != nil || ok {
		t.Fatalf("exhaustion claim = %#v ok=%t err=%v", claimed, ok, err)
	}
	trace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Records) < 4 {
		t.Fatalf("recovery Trace = %#v", trace.Records)
	}
	tail := trace.Records[len(trace.Records)-4:]
	if tail[0].Kind != agentobs.RecordEvent || tail[0].Name != agent.TraceEventLeaseExpired {
		t.Fatalf("third Lease expiry = %#v", tail[0])
	}
	if tail[1].Kind != agentobs.RecordEvent || tail[1].Name != agent.TraceEventRecoveryExhausted {
		t.Fatalf("recovery exhaustion Event = %#v", tail[1])
	}
	if tail[2].Kind != agentobs.RecordEvent || tail[2].Name != agent.TraceEventRunTerminal {
		t.Fatalf("recovery terminal Event = %#v", tail[2])
	}
	if tail[3].Kind != agentobs.RecordSpanEnded || tail[3].SpanID != trace.RootSpanID || tail[3].Status != agentobs.StatusError {
		t.Fatalf("recovery root end = %#v", tail[3])
	}
	for _, record := range trace.Records {
		if record.Kind == agentobs.RecordSpanEnded && record.SpanID != trace.RootSpanID {
			t.Fatalf("recovery fabricated Attempt terminal: %#v", record)
		}
	}
}

func TestRetryCreatesSeparateTraceLinkedToPriorRootAndReplays(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-retry@example.com")
	const messageID = "0190cdd2-5f2d-7ad8-b3f5-1b588788c426"
	sourceRunID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, messageID)
	cancelled := api.postJSONWithCookieAndCSRF(t, "/api/v1/agent-runs/"+sourceRunID+"/cancel", map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if cancelled.Code != http.StatusOK {
		t.Fatalf("cancel status = %d body=%s", cancelled.Code, cancelled.Body.String())
	}
	path := "/api/v1/agent-runs/" + sourceRunID + "/retry"
	first := api.postJSONWithCookieAndCSRF(t, path, map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "trace-retry-key")
	if first.Code != http.StatusAccepted {
		t.Fatalf("retry status = %d body=%s", first.Code, first.Body.String())
	}
	var firstBody struct {
		Run agent.RunSnapshot `json:"run"`
	}
	decodeBody(t, first, &firstBody)
	ctx := context.Background()
	source, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), sourceRunID)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), firstBody.Run.ID)
	if err != nil {
		t.Fatalf("load Retry Trace: %v", err)
	}
	if retry.TraceID == source.TraceID || len(retry.Records) != 4 {
		t.Fatalf("Retry Trace = %#v source=%#v", retry, source)
	}
	link, admitted := retry.Records[2], retry.Records[3]
	if link.Kind != agentobs.RecordLink || link.Name != semconv.LinkRetriedFrom || link.SpanID != retry.RootSpanID || link.TargetTraceID != source.TraceID || link.TargetSpanID != source.RootSpanID {
		t.Fatalf("retried_from Link = %#v", link)
	}
	if admitted.Kind != agentobs.RecordEvent || admitted.Name != agent.TraceEventRetryAdmitted {
		t.Fatalf("Retry admitted Event = %#v", admitted)
	}

	replay := api.postJSONWithCookieAndCSRF(t, path, map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "trace-retry-key")
	if replay.Code != http.StatusAccepted {
		t.Fatalf("retry replay status = %d body=%s", replay.Code, replay.Body.String())
	}
	var replayBody struct {
		Run agent.RunSnapshot `json:"run"`
	}
	decodeBody(t, replay, &replayBody)
	if replayBody.Run.ID != firstBody.Run.ID {
		t.Fatalf("Retry replay Run = %q, want %q", replayBody.Run.ID, firstBody.Run.ID)
	}
	replayed, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), firstBody.Run.ID)
	if err != nil || len(replayed.Records) != 4 {
		t.Fatalf("Retry replay Trace records = %#v err=%v", replayed.Records, err)
	}
}

func TestAdmissionRollsBackRunJobAndMessageWhenRequiredTraceWriteFails(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-admission-failure@example.com")
	ctx := context.Background()
	if _, err := api.db.Pool().Exec(ctx, `drop table agent_trace_records`); err != nil {
		t.Fatal(err)
	}
	const messageID = "0190cdd2-5f2d-7ad8-b3f5-1b588788c421"
	response := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id": messageID, "content": "Required Trace failure must roll back.",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("failed Trace admission status = %d body=%s", response.Code, response.Body.String())
	}

	var messages, runs, jobs, traces int
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from chat_messages where id = $1`, messageID).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_runs where input_message_id = $1`, messageID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_jobs j join agent_runs r on r.id = j.run_id where r.input_message_id = $1`, messageID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_traces`).Scan(&traces); err != nil {
		t.Fatal(err)
	}
	if messages != 0 || runs != 0 || jobs != 0 || traces != 0 {
		t.Fatalf("failed admission retained message/run/job/Trace = %d/%d/%d/%d", messages, runs, jobs, traces)
	}
}

func TestControllerRecordsModelCheckpointPublicationAndTerminalTree(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-controller@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c427")
	ctx := context.Background()
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim = %#v ok=%t err=%v", claimed, ok, err)
	}
	registry, err := agent.NewActionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	model := &recordingModelClient{result: models.ModelDecision{Final: &models.FinalDraft{Text: "Durable traced answer."}}}
	telemetry := &failingTraceExporter{}
	runtime := agent.NewPostgresRuntime(api.db.Pool(), agent.BareSystemPrompt, func() string { return "msg_trace_controller" }, agent.WithBestEffortTraceExporter(telemetry))
	if err := agent.NewController(runtime, model, registry).Execute(ctx, attemptFromClaim(claimed)); err != nil {
		t.Fatalf("Controller Execute: %v", err)
	}
	trace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	wantKindsAndNames := []struct {
		kind agentobs.RecordKind
		name string
	}{
		{agentobs.RecordSpanStarted, agent.TraceSpanAgentExecution},
		{agentobs.RecordEvent, agent.TraceEventRunAdmitted},
		{agentobs.RecordSpanStarted, agent.TraceSpanJobAttempt},
		{agentobs.RecordSpanStarted, semconv.ModelCall},
		{agentobs.RecordSpanEnded, semconv.ModelCall},
		{agentobs.RecordEvent, agent.TraceEventCheckpointAccepted},
		{agentobs.RecordSpanStarted, agent.TraceSpanPublication},
		{agentobs.RecordEvent, agent.TraceEventPublicationPassed},
		{agentobs.RecordSpanEnded, agent.TraceSpanPublication},
		{agentobs.RecordSpanEnded, agent.TraceSpanJobAttempt},
		{agentobs.RecordEvent, agent.TraceEventRunTerminal},
		{agentobs.RecordSpanEnded, agent.TraceSpanAgentExecution},
	}
	if len(trace.Records) != len(wantKindsAndNames) {
		t.Fatalf("complete Trace records = %#v", trace.Records)
	}
	for index, want := range wantKindsAndNames {
		got := trace.Records[index]
		if got.Kind != want.kind || got.Name != want.name {
			t.Fatalf("record %d = %s/%s, want %s/%s", index, got.Kind, got.Name, want.kind, want.name)
		}
	}
	if telemetry.calls < 2 {
		t.Fatalf("best-effort exporter calls = %d, want Model start and terminal", telemetry.calls)
	}
}

func TestReclaimLinksRepeatedPhysicalActionAndKeepsFirstIncomplete(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-repeated-action@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c428")
	ctx := context.Background()
	queue := jobs.NewQueue(api.db.Pool())
	first, ok, err := queue.ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("first claim = %#v ok=%t err=%v", first, ok, err)
	}
	runtime := agent.NewPostgresRuntime(api.db.Pool(), agent.BareSystemPrompt, func() string { return "msg_trace_repeated_action" })
	proposal, err := agent.NewProposalCheckpoint(1, models.ActionProposalBatch{Actions: []models.ActionProposal{{
		Name: "recovery_record", Input: json.RawMessage(`{"value":"repeat-me"}`),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AppendCheckpoint(ctx, attemptFromClaim(first), proposal); err != nil {
		t.Fatal(err)
	}
	firstContext, firstTracer, err := runtime.StartAttemptTrace(ctx, attemptFromClaim(first))
	if err != nil {
		t.Fatal(err)
	}
	_, firstActionSpan, err := firstTracer.StartSpan(firstContext, agentobs.SpanStart{
		IdentityKey: agent.TraceActionStartIdentity(runID, 1, "decision:1/action:0"),
		Name:        semconv.AgentAction,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(ctx, `update agent_jobs set lease_expires_at = now() - interval '1 second' where id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	second, ok, err := queue.ClaimNext(ctx)
	if err != nil || !ok || second.AttemptNo != 2 {
		t.Fatalf("second claim = %#v ok=%t err=%v", second, ok, err)
	}
	action := &recoveryRecordingAction{}
	registry, err := agent.NewActionRegistry(action)
	if err != nil {
		t.Fatal(err)
	}
	model := &recordingModelClient{result: models.ModelDecision{Final: &models.FinalDraft{Text: "Repeated work completed."}}}
	if err := agent.NewController(runtime, model, registry).Execute(ctx, attemptFromClaim(second)); err != nil {
		t.Fatal(err)
	}
	trace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	var actionStarts []agentobs.Record
	var retries *agentobs.Record
	ended := make(map[agentobs.SpanID]bool)
	for index := range trace.Records {
		record := trace.Records[index]
		if record.Kind == agentobs.RecordSpanStarted && record.Name == semconv.AgentAction {
			actionStarts = append(actionStarts, record)
		}
		if record.Kind == agentobs.RecordSpanEnded {
			ended[record.SpanID] = true
		}
		if record.Kind == agentobs.RecordLink && record.Name == semconv.LinkRetries {
			retries = &trace.Records[index]
		}
	}
	if len(actionStarts) != 2 || actionStarts[0].SpanID != firstActionSpan.SpanID || ended[firstActionSpan.SpanID] {
		t.Fatalf("physical Action executions = %#v ended=%v", actionStarts, ended)
	}
	if retries == nil || retries.SpanID != actionStarts[1].SpanID || retries.TargetTraceID != trace.TraceID || retries.TargetSpanID != firstActionSpan.SpanID {
		t.Fatalf("retries Link = %#v", retries)
	}
	var resultCheckpoints int
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_run_checkpoints where run_id = $1 and kind = 'action_result'`, runID).Scan(&resultCheckpoints); err != nil {
		t.Fatal(err)
	}
	if resultCheckpoints != 1 || len(action.calls) != 1 {
		t.Fatalf("accepted Result/physical second call = %d/%d", resultCheckpoints, len(action.calls))
	}
}

func TestRequiredModelStartFailureDoesNotCallGateway(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-model-start-failure@example.com")
	_ = admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c429")
	ctx := context.Background()
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim = %#v ok=%t err=%v", claimed, ok, err)
	}
	if _, err := api.db.Pool().Exec(ctx, `drop table agent_trace_records`); err != nil {
		t.Fatal(err)
	}
	model := &recordingModelClient{result: models.ModelDecision{Final: &models.FinalDraft{Text: "must not be called"}}}
	registry, err := agent.NewActionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	err = agent.NewController(agent.NewPostgresRuntime(api.db.Pool(), agent.BareSystemPrompt, nil), model, registry).Execute(ctx, attemptFromClaim(claimed))
	if err == nil || model.calls != 0 {
		t.Fatalf("Controller error/model calls = %v/%d, want required start failure before gateway", err, model.calls)
	}
}

func TestStaleAttemptCannotAppendTraceRecordsAfterReclaim(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-stale-attempt@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c430")
	ctx := context.Background()
	queue := jobs.NewQueue(api.db.Pool())
	first, ok, err := queue.ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("first claim = %#v ok=%t err=%v", first, ok, err)
	}
	runtime := agent.NewPostgresRuntime(api.db.Pool(), agent.BareSystemPrompt, nil)
	firstContext, firstTracer, err := runtime.StartAttemptTrace(ctx, attemptFromClaim(first))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(ctx, `update agent_jobs set lease_expires_at = now() - interval '1 second' where id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	second, ok, err := queue.ClaimNext(ctx)
	if err != nil || !ok || second.AttemptNo != 2 {
		t.Fatalf("second claim = %#v ok=%t err=%v", second, ok, err)
	}
	if err := firstTracer.Event(firstContext, agentobs.Event{
		IdentityKey: "run/" + runID + "/stale-worker-event", Name: "nano.test.stale",
	}); !errors.Is(err, agent.ErrLeaseLost) {
		t.Fatalf("stale append error = %v, want ErrLeaseLost", err)
	}
	trace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range trace.Records {
		if record.IdentityKey == "run/"+runID+"/stale-worker-event" {
			t.Fatalf("stale Event was appended: %#v", record)
		}
	}
}

func TestModelCallRemainsIncompleteWhenResponseIsNeverObserved(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-model-incomplete@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c431")
	ctx, cancel := context.WithCancel(context.Background())
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim = %#v ok=%t err=%v", claimed, ok, err)
	}
	model := &blockingTraceModel{started: make(chan struct{})}
	registry, err := agent.NewActionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- agent.NewController(agent.NewPostgresRuntime(api.db.Pool(), agent.BareSystemPrompt, nil), model, registry).Execute(ctx, attemptFromClaim(claimed))
	}()
	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("Model was not called")
	}
	trace, err := agent.LoadDurableTraceByRun(context.Background(), api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	var modelStarts, modelEnds int
	for _, record := range trace.Records {
		if record.Name == semconv.ModelCall && record.Kind == agentobs.RecordSpanStarted {
			modelStarts++
		}
		if record.Name == semconv.ModelCall && record.Kind == agentobs.RecordSpanEnded {
			modelEnds++
		}
	}
	if modelStarts != 1 || modelEnds != 0 {
		t.Fatalf("in-flight Model records start/end = %d/%d, Trace=%#v", modelStarts, modelEnds, trace.Records)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Controller error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled Controller did not return")
	}
	trace, err = agent.LoadDurableTraceByRun(context.Background(), api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range trace.Records {
		if record.Name == semconv.ModelCall && record.Kind == agentobs.RecordSpanEnded {
			t.Fatalf("cancelled process fabricated a Model terminal: %#v", record)
		}
	}
}

func TestCompletedModelCallRemainsUnacceptedWhenCheckpointCommitFails(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-model-unaccepted@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c432")
	ctx := context.Background()
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim = %#v ok=%t err=%v", claimed, ok, err)
	}
	commitCalls := 0
	runtime := agent.NewPostgresRuntime(api.db.Pool(), agent.BareSystemPrompt, nil, agent.WithCommitFunc(func(ctx context.Context, tx pgx.Tx) error {
		commitCalls++
		if commitCalls >= 3 {
			return errors.New("checkpoint storage unavailable")
		}
		return tx.Commit(ctx)
	}))
	model := &recordingModelClient{result: models.ModelDecision{Final: &models.FinalDraft{Text: "observed but unaccepted"}}}
	registry, err := agent.NewActionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.NewController(runtime, model, registry).Execute(ctx, attemptFromClaim(claimed)); err == nil {
		t.Fatal("Checkpoint failure returned nil")
	}
	trace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	var modelEnds, accepted int
	for _, record := range trace.Records {
		if record.Name == semconv.ModelCall && record.Kind == agentobs.RecordSpanEnded {
			modelEnds++
		}
		if record.Name == agent.TraceEventCheckpointAccepted {
			accepted++
		}
	}
	var checkpoints int
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_run_checkpoints where run_id = $1`, runID).Scan(&checkpoints); err != nil {
		t.Fatal(err)
	}
	if model.calls != 1 || modelEnds != 1 || checkpoints != 0 || accepted != 0 {
		t.Fatalf("Model calls/ends/checkpoints/Events = %d/%d/%d/%d", model.calls, modelEnds, checkpoints, accepted)
	}
}

func TestActionLossBoundariesPreservePhysicalWorkWithoutAcceptance(t *testing.T) {
	tests := []struct {
		name      string
		messageID string
		failFrom  int
		wantEnded bool
	}{
		{name: "after execution before terminal", messageID: "0190cdd2-5f2d-7ad8-b3f5-1b588788c433", failFrom: 2, wantEnded: false},
		{name: "after terminal before Result Checkpoint", messageID: "0190cdd2-5f2d-7ad8-b3f5-1b588788c434", failFrom: 3, wantEnded: true},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-action-loss-"+string(rune('a'+index))+"@example.com")
			runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, tt.messageID)
			ctx := context.Background()
			claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
			if err != nil || !ok {
				t.Fatalf("claim = %#v ok=%t err=%v", claimed, ok, err)
			}
			attempt := attemptFromClaim(claimed)
			normalRuntime := agent.NewPostgresRuntime(api.db.Pool(), agent.BareSystemPrompt, nil)
			proposal, err := agent.NewProposalCheckpoint(1, models.ActionProposalBatch{Actions: []models.ActionProposal{{
				Name: "recovery_record", Input: json.RawMessage(`{"value":"physical-work"}`),
			}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := normalRuntime.AppendCheckpoint(ctx, attempt, proposal); err != nil {
				t.Fatal(err)
			}
			commitCalls := 0
			faultyRuntime := agent.NewPostgresRuntime(api.db.Pool(), agent.BareSystemPrompt, nil, agent.WithCommitFunc(func(ctx context.Context, tx pgx.Tx) error {
				commitCalls++
				if commitCalls >= tt.failFrom {
					return errors.New("simulated durable boundary loss")
				}
				return tx.Commit(ctx)
			}))
			action := &recoveryRecordingAction{}
			registry, err := agent.NewActionRegistry(action)
			if err != nil {
				t.Fatal(err)
			}
			model := &recordingModelClient{result: models.ModelDecision{Final: &models.FinalDraft{Text: "must not be reached"}}}
			if err := agent.NewController(faultyRuntime, model, registry).Execute(ctx, attempt); err == nil {
				t.Fatal("faulty Action boundary returned nil")
			}
			trace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
			if err != nil {
				t.Fatal(err)
			}
			var actionStarts, actionEnds, resultEvents int
			for _, record := range trace.Records {
				if record.Name == semconv.AgentAction && record.Kind == agentobs.RecordSpanStarted {
					actionStarts++
				}
				if record.Name == semconv.AgentAction && record.Kind == agentobs.RecordSpanEnded {
					actionEnds++
				}
				if record.Name == agent.TraceEventCheckpointAccepted && stringAttributeForTest(record, agent.TraceKeyCheckpointKind) == string(agent.CheckpointActionResult) {
					resultEvents++
				}
			}
			var results int
			if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_run_checkpoints where run_id = $1 and kind = 'action_result'`, runID).Scan(&results); err != nil {
				t.Fatal(err)
			}
			wantEnds := 0
			if tt.wantEnded {
				wantEnds = 1
			}
			if len(action.calls) != 1 || actionStarts != 1 || actionEnds != wantEnds || results != 0 || resultEvents != 0 || model.calls != 0 {
				t.Fatalf("calls/starts/ends/results/Events/model = %d/%d/%d/%d/%d/%d", len(action.calls), actionStarts, actionEnds, results, resultEvents, model.calls)
			}
		})
	}
}

type failingTraceExporter struct{ calls int }

func (e *failingTraceExporter) Export(context.Context, agentobs.Record) error {
	e.calls++
	return errors.New("telemetry unavailable")
}
func (*failingTraceExporter) ForceFlush(context.Context) error { return nil }
func (*failingTraceExporter) Shutdown(context.Context) error   { return nil }

type blockingTraceModel struct{ started chan struct{} }

func (m *blockingTraceModel) Decide(ctx context.Context, request models.ModelRequest) (models.ModelOutcome, error) {
	close(m.started)
	<-ctx.Done()
	return models.ModelOutcome{Metadata: models.ModelCallMetadata{RequestedModel: request.Model, ResultKind: models.ModelResultUnavailable}}, ctx.Err()
}

func stringAttributeForTest(record agentobs.Record, key string) string {
	for _, attribute := range record.Attributes {
		if attribute.Key == key && attribute.Value.Kind == agentobs.ValueString {
			return attribute.Value.String
		}
	}
	return ""
}

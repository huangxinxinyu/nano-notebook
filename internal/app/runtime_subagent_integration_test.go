package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/promptcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/researchsource"
	"github.com/huangxinxinyu/nano-notebook/internal/sourcejobs"
	"github.com/huangxinxinyu/nano-notebook/internal/webreader"
	"github.com/jackc/pgx/v5"
)

type runtimeSubagentFixture struct {
	api        *testAPI
	parent     jobs.ClaimedJob
	runtime    *agent.ResearchRuntime
	execution  agent.Execution
	tools      map[string]agent.Action
	definition agentcatalog.Definition
	sink       *capturingDirectTraceSink
}

func newRuntimeSubagentFixture(t *testing.T) runtimeSubagentFixture {
	t.Helper()
	api := newTestAPI(t)
	parent, _, _, _ := admitResearchExecutionForRelease(t, api, "runtime-subagents@example.com", "nano.default@27")
	runtime, err := agent.NewResearchRuntime(api.db.Pool(), promptcatalog.MustLoadEmbedded())
	if err != nil {
		t.Fatal(err)
	}
	execution, err := runtime.Load(context.Background(), attemptFromClaim(parent))
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := agentcatalog.MustLoadEmbedded().ResolveDefinition(agentcatalog.MustParseReference("research.executor@18"))
	if !ok {
		t.Fatal("missing definition")
	}
	sink := &capturingDirectTraceSink{}
	runtime.WithTraceSink(sink)
	f := runtimeSubagentFixture{sink: sink, api: api, parent: parent, runtime: runtime, execution: execution, definition: definition, tools: map[string]agent.Action{}}
	for _, registration := range agent.NewRuntimeSubagentToolRegistrations(api.db.Pool(), sink) {
		f.tools[registration.Action.Definition().Name] = registration.Action
	}
	return f
}

func (f runtimeSubagentFixture) request(name, actionID string, input json.RawMessage, attempt agent.Attempt) agent.ActionRequest {
	return agent.ActionRequest{Attempt: attempt, UserID: f.execution.UserID, ChatID: f.execution.ChatID, ActionID: actionID, Input: input, Definition: f.definition.Reference(), DefinitionSHA256: f.definition.SHA256}
}

func (f runtimeSubagentFixture) spawn(t *testing.T) string {
	t.Helper()
	input := json.RawMessage(`{"message":"Independently verify the numerical claims; return evidence and unresolved gaps.","task_name":"verify numbers"}`)
	appendResearchProposal(t, f.runtime, attemptFromClaim(f.parent), 1, []models.ActionProposal{{Name: "spawn_agent", Input: input}})
	result, err := f.tools["spawn_agent"].Execute(context.Background(), f.request("spawn_agent", "decision:1/action:0", input, attemptFromClaim(f.parent)))
	if err != nil || result.Status != agent.ActionSucceeded {
		t.Fatalf("spawn=%+v err=%v", result, err)
	}
	appendResearchResult(t, f.runtime, attemptFromClaim(f.parent), 1, 0, result)
	var output struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal(result.Output, &output); err != nil || output.AgentID == "" {
		t.Fatalf("spawn output=%s err=%v", result.Output, err)
	}
	return output.AgentID
}

func (f runtimeSubagentFixture) claimChild(t *testing.T, childID string) jobs.ClaimedJob {
	t.Helper()
	child, ok, err := jobs.NewQueueWithTraceSink(f.api.db.Pool(), f.sink).ClaimNext(context.Background())
	if err != nil || !ok || child.RunID != childID {
		t.Fatalf("child claim=%+v ok=%v err=%v", child, ok, err)
	}
	return child
}

func TestRuntimeSubagentInheritsParentAndPublishesOnlyToParent(t *testing.T) {
	f := newRuntimeSubagentFixture(t)
	ctx := context.Background()
	childID := f.spawn(t)
	if err := f.runtime.CheckAuthority(ctx, attemptFromClaim(f.parent)); err != nil {
		t.Fatalf("spawn suspended parent: %v", err)
	}
	child := f.claimChild(t, childID)
	execution, err := f.runtime.Load(ctx, attemptFromClaim(child))
	if err != nil {
		t.Fatal(err)
	}
	if execution.UserID != f.execution.UserID || execution.ChatID != f.execution.ChatID || execution.Model != f.execution.Model || execution.AgentConfigID != f.execution.AgentConfigID || execution.ModelContext.Policy.SHA256 != f.execution.ModelContext.Policy.SHA256 || execution.ParentRunID != f.parent.RunID {
		t.Fatalf("child scope=%+v parent=%+v", execution, f.execution)
	}
	request, err := f.runtime.BuildDecisionRequest(ctx, execution, agent.CheckpointPrefix{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	all := ""
	for _, message := range request.Messages {
		all += message.Content
	}
	if !strings.Contains(all, "Independently verify the numerical claims") {
		t.Fatalf("child context omits assigned task: %s", all)
	}
	draft := models.FinalDraft{Text: "Verified two claims against original evidence; the third remains unresolved."}
	if _, err := f.runtime.PrepareDecisionResponse(ctx, execution, agent.CheckpointPrefix{}, models.ModelDecision{Final: &draft}); err != nil {
		t.Fatalf("child incorrectly requires full report assembly: %v", err)
	}
	checkpoint, err := agent.NewFinalDraftCheckpoint(1, draft)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.runtime.AppendCheckpoint(ctx, attemptFromClaim(child), checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := f.runtime.PublishFinal(ctx, attemptFromClaim(child), draft); err != nil {
		t.Fatal(err)
	}
	var messages int
	if err := f.api.db.Pool().QueryRow(ctx, `select count(*) from chat_messages where chat_id=$1 and role='assistant'`, execution.ChatID).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if messages != 0 {
		t.Fatalf("subagent published %d user-facing messages", messages)
	}
	result, err := f.tools["list_agents"].Execute(ctx, f.request("list_agents", "decision:2/action:0", json.RawMessage(`{}`), attemptFromClaim(f.parent)))
	if err != nil || !strings.Contains(string(result.Output), draft.Text) {
		t.Fatalf("child result=%s err=%v", result.Output, err)
	}
}

func TestRuntimeSubagentWaitReleasesWorkerAndChildCompletionWakesParent(t *testing.T) {
	f := newRuntimeSubagentFixture(t)
	ctx := context.Background()
	childID := f.spawn(t)
	input, _ := json.Marshal(map[string]any{"agent_ids": []string{childID}, "timeout_ms": 60000})
	appendResearchProposal(t, f.runtime, attemptFromClaim(f.parent), 2, []models.ActionProposal{{Name: "wait_agent", Input: input}})
	_, err := f.tools["wait_agent"].Execute(ctx, f.request("wait_agent", "decision:2/action:0", input, attemptFromClaim(f.parent)))
	if !errors.Is(err, agent.ErrLeaseLost) {
		t.Fatalf("wait did not release lease: %v", err)
	}
	child := f.claimChild(t, childID)
	if _, err := f.runtime.Load(ctx, attemptFromClaim(child)); err != nil {
		t.Fatal(err)
	}
	draft := models.FinalDraft{Text: "Child finding with source references"}
	checkpoint, _ := agent.NewFinalDraftCheckpoint(1, draft)
	if _, err := f.runtime.AppendCheckpoint(ctx, attemptFromClaim(child), checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := f.runtime.PublishFinal(ctx, attemptFromClaim(child), draft); err != nil {
		t.Fatal(err)
	}
	parent, ok, err := jobs.NewQueue(f.api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok || parent.RunID != f.parent.RunID {
		t.Fatalf("root not woken: %+v ok=%v err=%v", parent, ok, err)
	}
	result, err := f.tools["wait_agent"].Execute(ctx, f.request("wait_agent", "decision:2/action:0", input, attemptFromClaim(parent)))
	if err != nil || result.Status != agent.ActionSucceeded || !strings.Contains(string(result.Output), draft.Text) {
		t.Fatalf("wait result=%s err=%v", result.Output, err)
	}
	appendResearchResult(t, f.runtime, attemptFromClaim(parent), 2, 0, result)
}

func TestRuntimeSubagentCannotSpawnEvenThroughDirectToolCall(t *testing.T) {
	f := newRuntimeSubagentFixture(t)
	ctx := context.Background()
	childID := f.spawn(t)
	child := f.claimChild(t, childID)
	if _, err := f.runtime.Load(ctx, attemptFromClaim(child)); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"message":"spawn a grandchild"}`)
	result, err := f.tools["spawn_agent"].Execute(ctx, f.request("spawn_agent", "decision:1/action:0", input, attemptFromClaim(child)))
	if err != nil || result.Status != agent.ActionDomainError || result.ErrorCode != "subagent_cannot_delegate" {
		t.Fatalf("recursive spawn=%+v err=%v", result, err)
	}
	var count int
	if err := f.api.db.Pool().QueryRow(ctx, `select count(*) from agent_subagents`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("created descendants: %d", count)
	}
}

func TestRuntimeSubagentFatalAttemptReturnsFailureToParent(t *testing.T) {
	f := newRuntimeSubagentFixture(t)
	ctx := context.Background()
	childID := f.spawn(t)
	child := f.claimChild(t, childID)
	resolution, err := jobs.NewQueue(f.api.db.Pool()).ResolveAttempt(ctx, child, agent.AttemptResolution{Disposition: agent.AttemptTerminal, ErrorCode: "model_invalid_response"})
	if err != nil || resolution.Disposition != agent.AttemptTerminal {
		t.Fatalf("terminal child=%+v err=%v", resolution, err)
	}
	result, err := f.tools["list_agents"].Execute(ctx, f.request("list_agents", "decision:2/action:0", json.RawMessage(`{}`), attemptFromClaim(f.parent)))
	if err != nil || !strings.Contains(string(result.Output), "model_invalid_response") {
		t.Fatalf("failure lost: %s %v", result.Output, err)
	}
}

func TestRuntimeSubagentRootTerminationCancelsOutstandingChildren(t *testing.T) {
	f := newRuntimeSubagentFixture(t)
	ctx := context.Background()
	childID := f.spawn(t)
	if err := f.runtime.Fail(ctx, attemptFromClaim(f.parent), "model_invalid_response"); err != nil {
		t.Fatal(err)
	}
	var runStatus, jobStatus string
	var token *string
	if err := f.api.db.Pool().QueryRow(ctx, `select r.status,j.status,j.lease_token::text from agent_runs r join agent_jobs j on j.run_id=r.id where r.id=$1`, childID).Scan(&runStatus, &jobStatus, &token); err != nil {
		t.Fatal(err)
	}
	if runStatus != "cancelled" || jobStatus != "cancelled" || token != nil {
		t.Fatalf("orphan work: %s %s %v", runStatus, jobStatus, token)
	}
}

func TestRuntimeSubagentWaitTimeoutResumesSameAcceptedAction(t *testing.T) {
	f := newRuntimeSubagentFixture(t)
	ctx := context.Background()
	childID := f.spawn(t)
	input, _ := json.Marshal(map[string]any{"agent_ids": []string{childID}, "timeout_ms": 1})
	appendResearchProposal(t, f.runtime, attemptFromClaim(f.parent), 2, []models.ActionProposal{{Name: "wait_agent", Input: input}})
	if _, err := f.tools["wait_agent"].Execute(ctx, f.request("wait_agent", "decision:2/action:0", input, attemptFromClaim(f.parent))); !errors.Is(err, agent.ErrLeaseLost) {
		t.Fatalf("wait=%v", err)
	}
	time.Sleep(5 * time.Millisecond)
	// Keep the child queued beyond this test's wait deadline.
	if _, err := f.api.db.Pool().Exec(ctx, `update agent_jobs set available_at=now()+interval '1 minute' where run_id=$1`, childID); err != nil {
		t.Fatal(err)
	}
	parent, ok, err := jobs.NewQueue(f.api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok || parent.RunID != f.parent.RunID {
		t.Fatalf("timeout claim=%+v %v %v", parent, ok, err)
	}
	result, err := f.tools["wait_agent"].Execute(ctx, f.request("wait_agent", "decision:2/action:0", input, attemptFromClaim(parent)))
	if err != nil || !strings.Contains(string(result.Output), `"timed_out":true`) {
		t.Fatalf("timeout reset on recovery: %s %v", result.Output, err)
	}
}

func TestRuntimeSubagentControllerUsesIndependentTodoAndReturnsTaskFinal(t *testing.T) {
	f := newRuntimeSubagentFixture(t)
	ctx := context.Background()
	childID := f.spawn(t)
	child := f.claimChild(t, childID)
	base := agent.NewPostgresRuntime(f.api.db.Pool(), "", nil)
	todo := agent.NewRewriteTodoListAction(base)
	for _, task := range []struct {
		claimed  jobs.ClaimedJob
		decision int
		item     string
	}{
		{f.parent, 2, "parent.todo.only: assemble the complete report"},
		{child, 1, "child.todo.only: verify numerical evidence"},
	} {
		input, _ := json.Marshal(map[string]any{"items": []string{task.item}})
		appendResearchProposal(t, f.runtime, attemptFromClaim(task.claimed), task.decision, []models.ActionProposal{{Name: "rewrite_todo_list", Input: input}})
		result, err := todo.Execute(ctx, f.request("rewrite_todo_list", fmt.Sprintf("decision:%d/action:0", task.decision), input, attemptFromClaim(task.claimed)))
		if err != nil || result.Status != agent.ActionSucceeded {
			t.Fatalf("TODO=%+v %v", result, err)
		}
		var snapshot agent.TodoSnapshot
		if err := json.Unmarshal(result.Output, &snapshot); err != nil {
			t.Fatal(err)
		}
		if snapshot.Revision != 1 || len(snapshot.Items) != 1 || snapshot.Items[0].Content != task.item {
			t.Fatalf("task TODO inherited another agent's plan: %+v", snapshot)
		}
		appendResearchResult(t, f.runtime, attemptFromClaim(task.claimed), task.decision, 0, result)
	}
	model := &recordingModelClient{result: models.ModelDecision{Final: &models.FinalDraft{Text: "Assigned investigation complete; uncertainty retained."}}}
	registry, err := agent.NewActionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	controller := agent.NewController(f.runtime, model, registry)
	if err := controller.Execute(ctx, attemptFromClaim(child)); err != nil {
		t.Fatal(err)
	}
	contextText := ""
	for _, message := range model.request.Messages {
		contextText += message.Content
	}
	if !strings.Contains(contextText, "child.todo.only") || strings.Contains(contextText, "parent.todo.only") {
		t.Fatalf("model received another agent's TODO: %s", contextText)
	}
	if model.calls != 1 {
		t.Fatalf("child did not reach its own decision: %d calls", model.calls)
	}
	var status string
	if err := f.api.db.Pool().QueryRow(ctx, `select status from agent_runs where id=$1`, childID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Fatalf("child status=%s", status)
	}
}

func TestRuntimeSubagentSpawnReplayDoesNotCreateAnotherChild(t *testing.T) {
	f := newRuntimeSubagentFixture(t)
	ctx := context.Background()
	childID := f.spawn(t)
	input := json.RawMessage(`{"message":"Independently verify the numerical claims; return evidence and unresolved gaps.","task_name":"verify numbers"}`)
	result, err := f.tools["spawn_agent"].Execute(ctx, f.request("spawn_agent", "decision:1/action:0", input, attemptFromClaim(f.parent)))
	if err != nil || !strings.Contains(string(result.Output), childID) {
		t.Fatalf("spawn replay=%s %v", result.Output, err)
	}
	var count int
	if err := f.api.db.Pool().QueryRow(ctx, `select count(*) from agent_subagents`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("spawn replay created %d children", count)
	}
}

func TestRuntimeSubagentWaitRejectsIDsOutsideParentScope(t *testing.T) {
	f := newRuntimeSubagentFixture(t)
	ctx := context.Background()
	_ = f.spawn(t)
	input := json.RawMessage(`{"agent_ids":["run-unrelated"],"timeout_ms":0}`)
	result, err := f.tools["wait_agent"].Execute(ctx, f.request("wait_agent", "decision:2/action:0", input, attemptFromClaim(f.parent)))
	if err != nil || result.Status != agent.ActionDomainError || result.ErrorCode != "subagent_not_owned" {
		t.Fatalf("foreign result disclosed: %+v %v", result, err)
	}
}

func TestRuntimeSubagentCapacityAllowsFourConcurrentIndependentTasks(t *testing.T) {
	f := newRuntimeSubagentFixture(t)
	ctx := context.Background()
	actions := make([]models.ActionProposal, 5)
	for i := range actions {
		input, _ := json.Marshal(map[string]string{"message": "Independent investigation", "task_name": "task"})
		actions[i] = models.ActionProposal{Name: "spawn_agent", Input: input}
	}
	appendResearchProposal(t, f.runtime, attemptFromClaim(f.parent), 1, actions)
	for i, action := range actions {
		actionID := fmt.Sprintf("decision:1/action:%d", i)
		result, err := f.tools["spawn_agent"].Execute(ctx, f.request("spawn_agent", actionID, action.Input, attemptFromClaim(f.parent)))
		if err != nil {
			t.Fatal(err)
		}
		if i < 4 && result.Status != agent.ActionSucceeded {
			t.Fatalf("child %d=%+v", i, result)
		}
		if i == 4 && result.ErrorCode != "subagent_capacity_exhausted" {
			t.Fatalf("missing capacity bound: %+v", result)
		}
		appendResearchResult(t, f.runtime, attemptFromClaim(f.parent), 1, i, result)
	}
	var count int
	if err := f.api.db.Pool().QueryRow(ctx, `select count(*) from agent_subagents`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("children=%d", count)
	}
	if err := f.runtime.CheckAuthority(ctx, attemptFromClaim(f.parent)); err != nil {
		t.Fatalf("parallel spawn suspended root: %v", err)
	}
}

func TestRuntimeSubagentImportedEvidenceBecomesAvailableToParent(t *testing.T) {
	f := newRuntimeSubagentFixture(t)
	ctx := context.Background()
	childID := f.spawn(t)
	child := f.claimChild(t, childID)
	var notebookID string
	if err := f.api.db.Pool().QueryRow(ctx, `select notebook_id from chat_chats where id=$1`, f.execution.ChatID).Scan(&notebookID); err != nil {
		t.Fatal(err)
	}
	installReadyEvidenceSetFixture(t, f.api, notebookID, "", "", "", "")
	service := researchsource.NewService(f.api.db.Pool(), &importPDFAcquirer{contents: map[string]webreader.Content{
		"https://example.com/subagent.pdf": {MediaType: webreader.MediaTypePDF, FinalURL: "https://example.com/subagent.pdf", PDF: researchNativePDF("Verified evidence from independent research.")},
	}}, objectstore.NewMemoryStore())
	imported, err := service.ImportResearchPDF(ctx, agent.ResearchSourceImportRequest{URL: "https://example.com/subagent.pdf", ActionID: "decision:1/action:0", Attempt: attemptFromClaim(child)})
	if err != nil {
		t.Fatal(err)
	}
	queue := sourcejobs.NewQueue(f.api.db.Pool(), 30*time.Second)
	lease, ok, err := queue.Claim(ctx)
	if err != nil || !ok {
		t.Fatalf("source claim=%+v %v %v", lease, ok, err)
	}
	completeResearchPDFEvidence(t, f.api, queue, lease, "rev_subagent", "unit_subagent", "Verified evidence from independent research.", 1)
	for _, id := range []string{f.parent.RunID, childID} {
		var count int
		if err := f.api.db.Pool().QueryRow(ctx, `select count(*) from agent_run_evidence_set where run_id=$1 and source_id=$2`, id, imported.SourceID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("verified child Source unavailable to family Run %s", id)
		}
	}
	// A child's completed retrieval is valid citation authority for the root,
	// but an unrelated, unread URL in the child's prose must remain ineligible.
	appendResearchProposal(t, f.runtime, attemptFromClaim(child), 1, []models.ActionProposal{{Name: "search_evidence", Input: json.RawMessage(`{"query":"verify evidence"}`)}})
	manifest, _ := json.Marshal(map[string]any{
		"result_version": 2, "complete_empty": false, "degraded": false, "degradations": []string{},
		"evidence": []map[string]string{{"chunk_id": "unit_subagent", "source_id": imported.SourceID, "evidence_revision_id": "rev_subagent"}},
	})
	appendResearchResult(t, f.runtime, attemptFromClaim(child), 1, 0, agent.ActionResult{Status: agent.ActionSucceeded, Output: manifest})
	childFinal := models.FinalDraft{Text: "Verified [paper](https://example.com/subagent.pdf). Also [unread](https://example.com/unread)."}
	childCheckpoint, err := agent.NewFinalDraftCheckpoint(2, childFinal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.runtime.AppendCheckpoint(ctx, attemptFromClaim(child), childCheckpoint); err != nil {
		t.Fatal(err)
	}
	if err := f.runtime.PublishFinal(ctx, attemptFromClaim(child), childFinal); err != nil {
		t.Fatal(err)
	}
	rootCheckpoint, err := agent.NewFinalDraftCheckpoint(2, childFinal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.runtime.AppendCheckpoint(ctx, attemptFromClaim(f.parent), rootCheckpoint); err != nil {
		t.Fatal(err)
	}
	if err := f.runtime.PublishFinal(ctx, attemptFromClaim(f.parent), childFinal); err != nil {
		t.Fatal(err)
	}
	var report string
	if err := f.api.db.Pool().QueryRow(ctx, `select content from chat_messages where chat_id=$1 and role='assistant'`, f.execution.ChatID).Scan(&report); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report, "[paper](https://example.com/subagent.pdf)") || strings.Contains(report, "https://example.com/unread") {
		t.Fatalf("root failed to distinguish child retrieval from unverified prose: %s", report)
	}
}

func TestRuntimeSubagentDeadlineExpiresEntireFamily(t *testing.T) {
	f := newRuntimeSubagentFixture(t)
	ctx := context.Background()
	childID := f.spawn(t)
	f.claimChild(t, childID)
	if _, err := f.api.db.Pool().Exec(ctx, `update agent_trees set absolute_deadline=now()-interval '1 second' where id=(select tree_id from agent_runs where id=$1)`, f.parent.RunID); err != nil {
		t.Fatal(err)
	}
	if count := expireRunInTransaction(t, f.api, f.parent.RunID); count != 2 {
		t.Fatalf("expired family count=%d want=2", count)
	}
	for _, id := range []string{f.parent.RunID, childID} {
		assertTerminalRunState(t, f.api, id, f.execution.ChatID, "failed", "failed", "run_deadline_exceeded", 0)
	}
}

func TestRuntimeSubagentToolsRecheckCurrentMembership(t *testing.T) {
	f := newRuntimeSubagentFixture(t)
	ctx := context.Background()
	tx, err := f.api.db.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `insert into identity_users(id,canonical_email,display_email) values('new_owner','new-owner@example.com','new-owner@example.com')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `update notebook_memberships set role='viewer' where user_id=$1`, f.execution.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `insert into notebook_memberships(notebook_id,user_id,role) select notebook_id,'new_owner','owner' from notebook_memberships where user_id=$1`, f.execution.UserID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"message":"Run after the principal's research permission was revoked"}`)
	result, err := f.tools["spawn_agent"].Execute(ctx, f.request("spawn_agent", "decision:1/action:0", input, attemptFromClaim(f.parent)))
	if err != nil || result.Status != agent.ActionDomainError || result.ErrorCode != "subagent_tools_not_allowed" {
		t.Fatalf("revoked principal created work: %+v %v", result, err)
	}
	var count int
	if err := f.api.db.Pool().QueryRow(ctx, `select count(*) from agent_subagents`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("revoked principal created %d children", count)
	}
}

func TestRuntimeSubagentUserCancellationEndsRunningChildTrace(t *testing.T) {
	f := newRuntimeSubagentFixture(t)
	ctx := context.Background()
	childID := f.spawn(t)
	child := f.claimChild(t, childID)
	scope, err := agent.NewTraceScope(f.sink)
	if err != nil {
		t.Fatal(err)
	}
	defer scope.Rollback()
	traceCtx := agent.ContextWithTraceScope(ctx, scope)
	if err := f.api.db.WithRequestPrincipal(traceCtx, f.execution.UserID, func(tx pgx.Tx) error {
		_, err := agent.NewStore(tx).Cancel(traceCtx, f.execution.UserID, f.parent.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.runtime.CheckAuthority(ctx, attemptFromClaim(child)); !errors.Is(err, agent.ErrLeaseLost) {
		t.Fatalf("cancelled child retained execution authority: %v", err)
	}
	rootEnded, attemptEnded := false, false
	for _, envelope := range f.sink.envelopes {
		if envelope.Trace.RunID != childID || envelope.Record.Kind != agentobs.RecordSpanEnded || envelope.Record.Status != agentobs.StatusCancelled {
			continue
		}
		rootEnded = rootEnded || envelope.Record.Name == agent.TraceSpanAgentExecution
		attemptEnded = attemptEnded || envelope.Record.Name == agent.TraceSpanJobAttempt
	}
	if !rootEnded || !attemptEnded {
		t.Fatalf("cancelled child trace incomplete: execution=%v attempt=%v", rootEnded, attemptEnded)
	}
}

func TestRuntimeSubagentDatabaseAdmitsTheRuntimeTotal(t *testing.T) {
	f := newRuntimeSubagentFixture(t)
	ctx := context.Background()
	attempt := attemptFromClaim(f.parent)
	for i := 0; i < 17; i++ {
		input, _ := json.Marshal(map[string]string{"message": fmt.Sprintf("Independent investigation %d", i), "task_name": "task"})
		appendResearchProposal(t, f.runtime, attempt, i+1, []models.ActionProposal{{Name: "spawn_agent", Input: input}})
		result, err := f.tools["spawn_agent"].Execute(ctx, f.request("spawn_agent", fmt.Sprintf("decision:%d/action:0", i+1), input, attempt))
		if err != nil || result.Status != agent.ActionSucceeded {
			t.Fatalf("spawn %d=%+v err=%v", i, result, err)
		}
		appendResearchResult(t, f.runtime, attempt, i+1, 0, result)
		// Finish the child so only the total, not concurrency, is exercised.
		if _, err := f.api.db.Pool().Exec(ctx, `update agent_runs set status='completed',finished_at=now(),updated_at=now()
			where id in (select child_run_id from agent_subagents where parent_run_id=$1) and status='queued'`, f.parent.RunID); err != nil {
			t.Fatal(err)
		}
	}
}

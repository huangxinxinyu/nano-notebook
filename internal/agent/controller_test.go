package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

func TestControllerCheckpointsOrderedActionsThenFinalAndPublishesOnce(t *testing.T) {
	executionOrder := make([]string, 0, 2)
	action := &recordingAction{name: "record", order: &executionOrder}
	registry, err := NewActionRegistry(action)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &controllerRuntimeStub{execution: defaultControllerExecution()}
	temperature := 0.0
	runtime.execution.ModelInvocation = models.ModelInvocationPolicy{
		Temperature: &temperature, MaxOutputTokens: 555, Timeout: 4 * time.Second, EnableThinking: true,
	}
	model := &decisionModelStub{decisions: []models.ModelDecision{
		{Proposal: &models.ActionProposalBatch{Actions: []models.ActionProposal{
			{Name: "record", Input: json.RawMessage(`{"value":"first"}`)},
			{Name: "record", Input: json.RawMessage(`{"value":"second"}`)},
		}}},
		{Final: &models.FinalDraft{Text: "Finished in order."}},
	}}
	controller := NewController(runtime, model, registry)

	if err := controller.Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	if len(executionOrder) != 2 || executionOrder[0] != "first" || executionOrder[1] != "second" {
		t.Fatalf("Action execution order = %v", executionOrder)
	}
	if len(action.attempts) != 2 || action.attempts[0] != runtime.execution.Attempt || action.attempts[1] != runtime.execution.Attempt {
		t.Fatalf("Action attempt authority = %+v, want %+v", action.attempts, runtime.execution.Attempt)
	}
	wantKinds := []CheckpointKind{
		CheckpointActionProposal,
		CheckpointActionResult,
		CheckpointActionResult,
		CheckpointFinalDraft,
	}
	if len(runtime.checkpoints) != len(wantKinds) {
		t.Fatalf("checkpoints = %+v", runtime.checkpoints)
	}
	for index, want := range wantKinds {
		if runtime.checkpoints[index].Kind != want || runtime.checkpoints[index].SequenceNo != index+1 {
			t.Fatalf("checkpoint %d = %+v, want kind %q", index, runtime.checkpoints[index], want)
		}
	}
	if len(model.requests) != 2 || len(model.requests[0].ActionDefinitions) != 1 || len(model.requests[1].ActionDefinitions) != 1 {
		t.Fatalf("model requests = %+v", model.requests)
	}
	for _, request := range model.requests {
		if request.InvocationPolicy.Temperature == nil || *request.InvocationPolicy.Temperature != 0 ||
			request.InvocationPolicy.MaxOutputTokens != 555 || request.InvocationPolicy.Timeout != 4*time.Second ||
			!request.InvocationPolicy.EnableThinking {
			t.Fatalf("model invocation policy = %+v", request.InvocationPolicy)
		}
	}
	if len(runtime.published) != 1 || runtime.published[0].Text != "Finished in order." {
		t.Fatalf("published = %+v", runtime.published)
	}
	if len(runtime.failed) != 0 {
		t.Fatalf("terminal failures = %v", runtime.failed)
	}
}

func TestControllerPlanMutationRemainsAvailableWithoutDedicatedBudget(t *testing.T) {
	at := time.Date(2026, 8, 31, 7, 20, 1, 0, time.UTC)
	registry, err := NewActionRegistry(
		NewRewriteTodoListAction(&todoActionLoaderStub{inputMessageID: "msg_1", proposedAt: at}),
		NewCalculateAction(),
	)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &controllerRuntimeStub{execution: defaultControllerExecution()}
	runtime.execution.InputMessageID = "msg_1"
	runtime.execution.ActionDecisionLimit = 1
	runtime.execution.ActionLimit = 1
	runtime.execution.ActionBatchLimit = 1
	model := &decisionModelStub{decisions: []models.ModelDecision{
		{Proposal: &models.ActionProposalBatch{Actions: []models.ActionProposal{{Name: "rewrite_todo_list", Input: json.RawMessage(`{"items":["plan","execute"]}`)}}}},
		{Proposal: &models.ActionProposalBatch{Actions: []models.ActionProposal{{Name: "calculate", Input: json.RawMessage(`{"operation":"add","operands":["1","2"]}`)}}}},
		{Final: &models.FinalDraft{Text: "done"}},
	}}
	if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	prefix, err := LoadCheckpointPrefix(context.Background(), runtime.checkpoints)
	if err != nil {
		t.Fatal(err)
	}
	if acceptedBusinessActions(prefix) != 1 || acceptedBusinessDecisions(prefix) != 1 {
		t.Fatalf("accepted business budget use: actions=%d decisions=%d", acceptedBusinessActions(prefix), acceptedBusinessDecisions(prefix))
	}
	if len(model.requests) != 3 || definitionNames(model.requests[0].ActionDefinitions) != "calculate,rewrite_todo_list" ||
		definitionNames(model.requests[1].ActionDefinitions) != "calculate,rewrite_todo_list" ||
		definitionNames(model.requests[2].ActionDefinitions) != "rewrite_todo_list" {
		t.Fatalf("request definitions = %#v", model.requests)
	}
	if len(runtime.published) != 1 || len(runtime.failed) != 0 {
		t.Fatalf("published=%v failed=%v", runtime.published, runtime.failed)
	}
}

func TestControllerRecoversToolCallNotAdvertisedForCurrentDecision(t *testing.T) {
	executionOrder := make([]string, 0, 1)
	registry, err := NewActionRegistry(
		&unavailableRecordingAction{recordingAction: recordingAction{name: "hidden", order: &executionOrder}},
		NewCalculateAction(),
	)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &invalidResponseRecoveryRuntimeStub{
		controllerRuntimeStub: &controllerRuntimeStub{execution: defaultControllerExecution()},
		limit:                 1,
	}
	model := &decisionModelStub{decisions: []models.ModelDecision{
		{Proposal: &models.ActionProposalBatch{Actions: []models.ActionProposal{{Name: "hidden", Input: json.RawMessage(`{"value":"must-not-run"}`)}}}},
		{Proposal: &models.ActionProposalBatch{Actions: []models.ActionProposal{{Name: "calculate", Input: json.RawMessage(`{"operation":"add","operands":["1","2"]}`)}}}},
		{Final: &models.FinalDraft{Text: "recovered"}},
	}}

	if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	if len(executionOrder) != 0 {
		t.Fatalf("unadvertised Action executed: %v", executionOrder)
	}
	if len(model.requests) != 3 || definitionNames(model.requests[0].ActionDefinitions) != "calculate" ||
		definitionNames(model.requests[1].ActionDefinitions) != "calculate" {
		t.Fatalf("request definitions = %#v", model.requests)
	}
	recoveryMessages := model.requests[1].Messages
	if len(recoveryMessages) == 0 || !strings.Contains(recoveryMessages[len(recoveryMessages)-1].Content, "hidden") ||
		!strings.Contains(recoveryMessages[len(recoveryMessages)-1].Content, "calculate") {
		t.Fatalf("recovery mapping detail = %+v", recoveryMessages)
	}
	if len(runtime.published) != 1 || runtime.published[0].Text != "recovered" || len(runtime.failed) != 0 {
		t.Fatalf("published=%v failed=%v", runtime.published, runtime.failed)
	}
}

func TestControllerRecoversIdenticalCompletedTodoRewriteWithoutExecutingItAgain(t *testing.T) {
	at := time.Date(2026, 9, 2, 15, 11, 9, 0, time.UTC)
	registry, err := NewActionRegistry(
		NewRewriteTodoListAction(&todoActionLoaderStub{inputMessageID: "msg_1", proposedAt: at}),
		NewCalculateAction(),
	)
	if err != nil {
		t.Fatal(err)
	}
	base := &controllerRuntimeStub{execution: defaultControllerExecution()}
	base.execution.InputMessageID = "msg_1"
	runtime := &invalidResponseRecoveryRuntimeStub{controllerRuntimeStub: base, limit: 1}
	rewrite := models.ActionProposal{Name: "rewrite_todo_list", Input: json.RawMessage(`{"items":["discover evidence","write report"]}`)}
	semanticallyIdenticalRewrite := models.ActionProposal{Name: "rewrite_todo_list", Input: json.RawMessage(`{
		"items": ["discover evidence", "write report"]
	}`)}
	model := &decisionModelStub{decisions: []models.ModelDecision{
		{Proposal: &models.ActionProposalBatch{Actions: []models.ActionProposal{rewrite}}},
		{Proposal: &models.ActionProposalBatch{Actions: []models.ActionProposal{semanticallyIdenticalRewrite}}},
		{Proposal: &models.ActionProposalBatch{Actions: []models.ActionProposal{{Name: "calculate", Input: json.RawMessage(`{"operation":"add","operands":["1","2"]}`)}}}},
		{Final: &models.FinalDraft{Text: "recovered"}},
	}}

	if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	prefix, err := LoadCheckpointPrefix(context.Background(), runtime.checkpoints)
	if err != nil {
		t.Fatal(err)
	}
	rewrites := 0
	for _, proposal := range prefix.Proposals {
		for _, action := range proposal.Actions {
			if action.Name == "rewrite_todo_list" {
				rewrites++
			}
		}
	}
	if rewrites != 1 {
		t.Fatalf("accepted identical rewrites=%d, want 1", rewrites)
	}
	if len(model.requests) != 4 {
		t.Fatalf("model requests=%d, want 4", len(model.requests))
	}
	messages := model.requests[2].Messages
	if len(messages) == 0 || !strings.Contains(messages[len(messages)-1].Content, "identical input already completed") ||
		!strings.Contains(messages[len(messages)-1].Content, "rewrite_todo_list") {
		t.Fatalf("duplicate recovery detail=%+v", messages)
	}
	if len(runtime.published) != 1 || runtime.published[0].Text != "recovered" || len(runtime.failed) != 0 {
		t.Fatalf("published=%v failed=%v", runtime.published, runtime.failed)
	}
}

func TestControllerPersistsNewOrdinaryDomainErrorsAsStructuredV2(t *testing.T) {
	registry, err := NewActionRegistry(NewCalculateAction())
	if err != nil {
		t.Fatal(err)
	}
	runtime := &controllerRuntimeStub{execution: defaultControllerExecution()}
	model := &decisionModelStub{decisions: []models.ModelDecision{
		{Proposal: &models.ActionProposalBatch{Actions: []models.ActionProposal{{
			Name: "calculate", Input: json.RawMessage(`{"operation":"divide","operands":["1","0"]}`),
		}}}},
		{Final: &models.FinalDraft{Text: "cannot divide by zero"}},
	}}
	if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	prefix, err := LoadCheckpointPrefix(context.Background(), runtime.checkpoints)
	if err != nil {
		t.Fatal(err)
	}
	result := prefix.Proposals[0].Actions[0].Result
	if result == nil || result.Error == nil || result.ErrorCode != "" ||
		result.Error.Code != "division_by_zero" || result.Error.Suggestion == "" {
		t.Fatalf("structured domain error = %+v", result)
	}
	if runtime.checkpoints[1].PayloadVersion != 2 {
		t.Fatalf("Action Result payload version = %d", runtime.checkpoints[1].PayloadVersion)
	}
}

func definitionNames(definitions []models.ActionDefinition) string {
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		names = append(names, definition.Name)
	}
	return strings.Join(names, ",")
}

func TestControllerUsesIsolatedQueryContextBeforeGroundedComposer(t *testing.T) {
	backend := &evidenceSearchStub{}
	registry, err := NewActionRegistry(NewSearchEvidenceAction(backend))
	if err != nil {
		t.Fatal(err)
	}
	base := &controllerRuntimeStub{execution: defaultControllerExecution()}
	base.execution.SelectedSourceCount = 1
	base.execution.PromptVersion = GroundedPromptVersion
	runtime := &queryContextControllerRuntimeStub{
		controllerRuntimeStub: base,
		queryRequest: models.ModelRequest{
			Model: base.execution.Model,
			Messages: []models.ModelMessage{
				{Role: models.RoleSystem, Content: "query contextualizer"},
				{Role: models.RoleUser, Content: "CURRENT MESSAGE: 你好"},
			},
			RequiredActionName: "search_evidence",
		},
		fallback:     models.ActionProposal{Name: "search_evidence", Input: json.RawMessage(`{"query":"你好","purpose":"answer the current request"}`)},
		historyPairs: 1,
	}
	model := &decisionModelStub{decisions: []models.ModelDecision{
		{Proposal: &models.ActionProposalBatch{Actions: []models.ActionProposal{{
			Name: "search_evidence", Input: json.RawMessage(`{"query":"你好","purpose":"answer the current request"}`),
		}}}},
		{Final: &models.FinalDraft{Text: "你好！"}},
	}}

	if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	if runtime.queryCalls != 1 || runtime.composerCalls != 1 {
		t.Fatalf("contextualizer/composer calls = %d/%d, want 1/1", runtime.queryCalls, runtime.composerCalls)
	}
	if len(model.requests) != 2 || model.requests[0].RequiredActionName != "search_evidence" ||
		len(model.requests[0].Messages) != 2 || model.requests[0].Messages[1].Content != "CURRENT MESSAGE: 你好" {
		t.Fatalf("model requests = %+v", model.requests)
	}
	if backend.query != "你好" || backend.purpose != "answer the current request" {
		t.Fatalf("retrieval input = %q/%q", backend.query, backend.purpose)
	}
}

func TestControllerFallsBackToCurrentMessageWhenQueryContextualizerFails(t *testing.T) {
	backend := &evidenceSearchStub{}
	registry, err := NewActionRegistry(NewSearchEvidenceAction(backend))
	if err != nil {
		t.Fatal(err)
	}
	base := &controllerRuntimeStub{execution: defaultControllerExecution()}
	base.execution.SelectedSourceCount = 1
	base.execution.PromptVersion = GroundedPromptVersion
	runtime := &queryContextControllerRuntimeStub{
		controllerRuntimeStub: base,
		queryRequest: models.ModelRequest{
			Model:              base.execution.Model,
			Messages:           []models.ModelMessage{{Role: models.RoleUser, Content: "CURRENT MESSAGE: 你有哪些工具"}},
			RequiredActionName: "search_evidence",
		},
		fallback: models.ActionProposal{Name: "search_evidence", Input: json.RawMessage(`{"query":"你有哪些工具","purpose":"answer the current request"}`)},
	}
	model := &decisionModelStub{
		decisions: []models.ModelDecision{{Final: &models.FinalDraft{Text: "我可以使用检索工具。"}}},
		errors:    []error{errors.New("contextualizer unavailable")},
	}

	if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	if runtime.queryCalls != 1 || runtime.composerCalls != 1 || backend.query != "你有哪些工具" {
		t.Fatalf("fallback contextualizer/composer/query = %d/%d/%q", runtime.queryCalls, runtime.composerCalls, backend.query)
	}
	if len(runtime.checkpoints) != 3 || runtime.checkpoints[0].Kind != CheckpointActionProposal ||
		runtime.checkpoints[1].Kind != CheckpointActionResult || runtime.checkpoints[2].Kind != CheckpointFinalDraft {
		t.Fatalf("fallback checkpoints = %+v", runtime.checkpoints)
	}
}

func TestControllerFallsBackWhenQueryContextualizerAnswersInsteadOfCallingSearch(t *testing.T) {
	backend := &evidenceSearchStub{}
	registry, err := NewActionRegistry(NewSearchEvidenceAction(backend))
	if err != nil {
		t.Fatal(err)
	}
	base := &controllerRuntimeStub{execution: defaultControllerExecution()}
	base.execution.SelectedSourceCount = 1
	base.execution.PromptVersion = GroundedPromptVersion
	runtime := &queryContextControllerRuntimeStub{
		controllerRuntimeStub: base,
		queryRequest: models.ModelRequest{
			Model:              base.execution.Model,
			Messages:           []models.ModelMessage{{Role: models.RoleUser, Content: "CURRENT MESSAGE: 你好"}},
			RequiredActionName: "search_evidence",
		},
		fallback: models.ActionProposal{Name: "search_evidence", Input: json.RawMessage(`{"query":"你好","purpose":"answer the current request"}`)},
	}
	model := &decisionModelStub{decisions: []models.ModelDecision{
		{Final: &models.FinalDraft{Text: "你好！"}},
		{Final: &models.FinalDraft{Text: "你好！"}},
	}}

	if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	if backend.query != "你好" || runtime.queryCalls != 1 || runtime.composerCalls != 1 {
		t.Fatalf("fallback query/contextualizer/composer = %q/%d/%d", backend.query, runtime.queryCalls, runtime.composerCalls)
	}
}

func TestControllerPreservesCurrentMessageInContextualizedSearchQuery(t *testing.T) {
	backend := &evidenceSearchStub{}
	registry, err := NewActionRegistry(NewSearchEvidenceAction(backend))
	if err != nil {
		t.Fatal(err)
	}
	base := &controllerRuntimeStub{execution: defaultControllerExecution()}
	base.execution.SelectedSourceCount = 1
	base.execution.PromptVersion = GroundedPromptVersion
	runtime := &queryContextControllerRuntimeStub{
		controllerRuntimeStub: base,
		queryRequest: models.ModelRequest{
			Model:              base.execution.Model,
			Messages:           []models.ModelMessage{{Role: models.RoleUser, Content: "CURRENT MESSAGE: Plan II 的研究课上限呢？"}},
			RequiredActionName: "search_evidence",
		},
		fallback: models.ActionProposal{Name: "search_evidence", Input: json.RawMessage(`{"query":"Plan II 的研究课上限呢？","purpose":"answer the current request"}`)},
	}
	model := &decisionModelStub{decisions: []models.ModelDecision{
		{Proposal: &models.ActionProposalBatch{Actions: []models.ActionProposal{{
			Name: "search_evidence", Input: json.RawMessage(`{"query":"degree planner Plan II research course maximum people","purpose":"find the applicable limit"}`),
		}}}},
		{Final: &models.FinalDraft{Text: "No supported answer."}},
	}}

	if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(backend.query, "Plan II 的研究课上限呢？") ||
		!strings.Contains(backend.query, "degree planner Plan II research course maximum people") {
		t.Fatalf("retrieval query = %q, want current Message followed by contextualized expansion", backend.query)
	}
}

func TestControllerTracesQueryContextHistoryAndFallbackWithoutRawText(t *testing.T) {
	tracer, exporter, traceContext := instrumentationTestTracer(t)
	backend := &evidenceSearchStub{}
	registry, err := NewActionRegistry(NewSearchEvidenceAction(backend))
	if err != nil {
		t.Fatal(err)
	}
	base := &controllerRuntimeStub{execution: defaultControllerExecution()}
	base.execution.SelectedSourceCount = 1
	base.execution.PromptVersion = GroundedPromptVersion
	queryRuntime := &queryContextControllerRuntimeStub{
		controllerRuntimeStub: base,
		queryRequest: models.ModelRequest{
			Model:              base.execution.Model,
			Messages:           []models.ModelMessage{{Role: models.RoleUser, Content: "PRIVATE CURRENT MESSAGE"}},
			RequiredActionName: "search_evidence",
		},
		fallback:     models.ActionProposal{Name: "search_evidence", Input: json.RawMessage(`{"query":"PRIVATE CURRENT MESSAGE","purpose":"answer the current request"}`)},
		historyPairs: 2,
	}
	runtime := &queryContextTraceRuntimeStub{queryContextControllerRuntimeStub: queryRuntime, tracer: tracer, traceContext: traceContext}
	model := &decisionModelStub{
		decisions: []models.ModelDecision{{Final: &models.FinalDraft{Text: "Safe final."}}},
		errors:    []error{errors.New("contextualizer unavailable")},
	}
	if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}

	var queryStart, queryEnd *agentobs.Record
	for index := range exporter.Records() {
		record := exporter.Records()[index]
		if record.Name != "nano.rag.query_contextualization" {
			continue
		}
		switch record.Kind {
		case agentobs.RecordSpanStarted:
			copy := record
			queryStart = &copy
		case agentobs.RecordSpanEnded:
			copy := record
			queryEnd = &copy
		}
	}
	if queryStart == nil || queryEnd == nil {
		t.Fatalf("query contextualization span missing: %#v", exporter.Records())
	}
	if got := int64Attribute(*queryStart, "nano.rag.query_context.history_pair_count"); got != 2 {
		t.Fatalf("history pair count = %d, want 2", got)
	}
	if !boolRecordAttribute(*queryEnd, "nano.rag.query_context.fallback_used") {
		t.Fatalf("fallback attribute missing: %#v", *queryEnd)
	}
	for _, record := range exporter.Records() {
		payload, payloadErr := record.CanonicalPayload()
		if payloadErr != nil {
			t.Fatal(payloadErr)
		}
		if strings.Contains(string(payload), "PRIVATE CURRENT MESSAGE") {
			t.Fatalf("query contextualization Trace leaked raw text: %s", payload)
		}
	}
}

func TestControllerRecoveryUsesAcceptedSearchProposalWithoutRecontextualizing(t *testing.T) {
	backend := &evidenceSearchStub{}
	registry, err := NewActionRegistry(NewSearchEvidenceAction(backend))
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := NewProposalCheckpoint(1, models.ActionProposalBatch{Actions: []models.ActionProposal{{
		Name: "search_evidence", Input: json.RawMessage(`{"query":"accepted current topic","purpose":"answer current request"}`),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	base := &controllerRuntimeStub{
		execution:   defaultControllerExecution(),
		checkpoints: []Checkpoint{{SequenceNo: 1, PendingCheckpoint: proposal}},
	}
	base.execution.SelectedSourceCount = 1
	base.execution.PromptVersion = GroundedPromptVersion
	runtime := &queryContextControllerRuntimeStub{
		controllerRuntimeStub: base,
		queryRequest: models.ModelRequest{
			Model: base.execution.Model, RequiredActionName: "search_evidence",
		},
		fallback: models.ActionProposal{Name: "search_evidence", Input: json.RawMessage(`{"query":"must not be used","purpose":"must not be used"}`)},
	}
	model := &decisionModelStub{decisions: []models.ModelDecision{{Final: &models.FinalDraft{Text: "Recovered final."}}}}

	if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	if runtime.queryCalls != 0 || runtime.composerCalls != 1 {
		t.Fatalf("recovery contextualizer/composer calls = %d/%d, want 0/1", runtime.queryCalls, runtime.composerCalls)
	}
	if backend.query != "accepted current topic" || len(model.requests) != 1 || len(runtime.checkpoints) != 3 {
		t.Fatalf("recovery query/model/checkpoints = %q/%d/%+v", backend.query, len(model.requests), runtime.checkpoints)
	}
}

func TestControllerPreparesGroundedFinalBeforeCheckpointAndPublication(t *testing.T) {
	registry, err := NewActionRegistry(NewCalculateAction())
	if err != nil {
		t.Fatal(err)
	}
	base := &controllerRuntimeStub{execution: defaultControllerExecution()}
	base.execution.SelectedSourceCount = 1
	runtime := &finalPreparationRuntimeStub{controllerRuntimeStub: base, prepared: models.FinalDraft{Text: "Verified answer."}}
	model := &decisionModelStub{decisions: []models.ModelDecision{{Final: &models.FinalDraft{Text: "Unverified draft."}}}}
	if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	if runtime.calls != 1 || len(runtime.checkpoints) != 1 || runtime.checkpoints[0].Kind != CheckpointFinalDraft ||
		len(runtime.published) != 1 || runtime.published[0].Text != "Verified answer." {
		t.Fatalf("preparation/checkpoints/publication=%d/%+v/%+v", runtime.calls, runtime.checkpoints, runtime.published)
	}
}

func TestControllerResumesFirstIncompleteActionWithoutRepeatingAcceptedResult(t *testing.T) {
	executionOrder := make([]string, 0, 1)
	registry, err := NewActionRegistry(&recordingAction{name: "record", order: &executionOrder})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &controllerRuntimeStub{execution: defaultControllerExecution()}
	proposal, err := NewProposalCheckpoint(1, models.ActionProposalBatch{Actions: []models.ActionProposal{
		{Name: "record", Input: json.RawMessage(`{"value":"already-accepted"}`)},
		{Name: "record", Input: json.RawMessage(`{"value":"resume-here"}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	acceptedResult, err := NewActionResultCheckpoint(1, 0, "decision:1/action:0", ActionResult{
		Status: ActionSucceeded, Output: json.RawMessage(`{"recorded":"already-accepted"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.checkpoints = []Checkpoint{
		{SequenceNo: 1, PendingCheckpoint: proposal},
		{SequenceNo: 2, PendingCheckpoint: acceptedResult},
	}
	model := &decisionModelStub{decisions: []models.ModelDecision{
		{Final: &models.FinalDraft{Text: "Resumed without duplication."}},
	}}

	if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	if len(executionOrder) != 1 || executionOrder[0] != "resume-here" {
		t.Fatalf("Action execution after recovery = %v", executionOrder)
	}
	if len(model.requests) != 1 {
		t.Fatalf("model calls after recovery = %d, want only next decision", len(model.requests))
	}
	if len(runtime.checkpoints) != 4 || runtime.checkpoints[2].IdentityKey != "decision:1/action:1" || runtime.checkpoints[3].IdentityKey != "decision:2/final" {
		t.Fatalf("recovered checkpoints = %+v", runtime.checkpoints)
	}
	if len(runtime.published) != 1 || runtime.published[0].Text != "Resumed without duplication." {
		t.Fatalf("published = %+v", runtime.published)
	}
}

func TestControllerReconcilesUnknownNonReplaySafeActionAfterCrash(t *testing.T) {
	executionOrder := make([]string, 0, 1)
	action := &recordingAction{name: "record", order: &executionOrder}
	registry, err := NewActionRegistry(action)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &controllerRuntimeStub{execution: defaultControllerExecution()}
	runtime.execution.Attempt.AttemptNo = 2
	proposal, err := NewProposalCheckpoint(1, models.ActionProposalBatch{Actions: []models.ActionProposal{{
		Name: "record", Input: json.RawMessage(`{"value":"completion-unknown"}`),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	runtime.checkpoints = []Checkpoint{{SequenceNo: 1, PendingCheckpoint: proposal}}
	model := &decisionModelStub{decisions: []models.ModelDecision{{Final: &models.FinalDraft{Text: "Recovered."}}}}

	if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	if len(executionOrder) != 0 {
		t.Fatalf("non-replay-safe Action executed after crash: %v", executionOrder)
	}
	prefix, err := LoadCheckpointPrefix(context.Background(), runtime.checkpoints)
	if err != nil {
		t.Fatal(err)
	}
	result := prefix.Proposals[0].Actions[0].Result
	if result == nil || result.Status != ActionDomainError || result.Error == nil || result.Error.Code != ErrorActionInterrupted {
		t.Fatalf("closing Result=%+v", result)
	}
}

func TestControllerReplaysExplicitlySafeActionAfterCrash(t *testing.T) {
	executionOrder := make([]string, 0, 1)
	action := &recordingAction{name: "record", order: &executionOrder, crashReplaySafe: true}
	registry, err := NewActionRegistry(action)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &controllerRuntimeStub{execution: defaultControllerExecution()}
	runtime.execution.Attempt.AttemptNo = 2
	proposal, err := NewProposalCheckpoint(1, models.ActionProposalBatch{Actions: []models.ActionProposal{{
		Name: "record", Input: json.RawMessage(`{"value":"safe-replay"}`),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	runtime.checkpoints = []Checkpoint{{SequenceNo: 1, PendingCheckpoint: proposal}}
	model := &decisionModelStub{decisions: []models.ModelDecision{{Final: &models.FinalDraft{Text: "Recovered."}}}}

	if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	if len(executionOrder) != 1 || executionOrder[0] != "safe-replay" {
		t.Fatalf("safe replay execution=%v", executionOrder)
	}
}

func TestControllerCompactsAndRetriesContextOverflowOnce(t *testing.T) {
	registry, err := NewActionRegistry(&recordingAction{name: "record", order: &[]string{}})
	if err != nil {
		t.Fatal(err)
	}
	base := &controllerRuntimeStub{execution: defaultControllerExecution()}
	base.execution.ModelContext.Policy.OverflowRetryLimit = 1
	runtime := &contextPreparationRuntimeStub{controllerRuntimeStub: base}
	model := &decisionModelStub{
		errors:    []error{&models.ModelError{Kind: models.ErrorContextOverflow, Err: errors.New("private Provider body")}, nil},
		decisions: []models.ModelDecision{{Final: &models.FinalDraft{Text: "Recovered after Compaction."}}},
	}
	if err := NewController(runtime, model, registry).Execute(context.Background(), base.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	if len(model.requests) != 2 || len(runtime.triggerReasons) != 2 || runtime.triggerReasons[0] != "" ||
		runtime.triggerReasons[1] != CompactionTriggerProviderOverflow || len(runtime.published) != 1 {
		t.Fatalf("requests=%d triggers=%v published=%v", len(model.requests), runtime.triggerReasons, runtime.published)
	}
}

func TestControllerStopsAfterSecondContextOverflow(t *testing.T) {
	registry, err := NewActionRegistry(&recordingAction{name: "record", order: &[]string{}})
	if err != nil {
		t.Fatal(err)
	}
	base := &controllerRuntimeStub{execution: defaultControllerExecution()}
	base.execution.ModelContext.Policy.OverflowRetryLimit = 1
	runtime := &contextPreparationRuntimeStub{controllerRuntimeStub: base}
	model := &decisionModelStub{errors: []error{
		&models.ModelError{Kind: models.ErrorContextOverflow, Err: errors.New("first")},
		&models.ModelError{Kind: models.ErrorContextOverflow, Err: errors.New("second")},
	}}
	err = NewController(runtime, model, registry).Execute(context.Background(), base.execution.Attempt)
	if !errors.Is(err, ErrContextBudgetExceeded) {
		t.Fatalf("err=%v", err)
	}
	if len(model.requests) != 2 || len(runtime.failed) != 1 || runtime.failed[0] != ErrContextBudgetExceeded.Error() {
		t.Fatalf("requests=%d failed=%v", len(model.requests), runtime.failed)
	}
}

func TestControllerRecoversOverCapacityBatchWithoutDisablingActions(t *testing.T) {
	executionOrder := make([]string, 0)
	registry, err := NewActionRegistry(&recordingAction{name: "record", order: &executionOrder})
	if err != nil {
		t.Fatal(err)
	}
	base := &controllerRuntimeStub{execution: defaultControllerExecution()}
	base.execution.ActionLimit = 1
	runtime := &invalidResponseRecoveryRuntimeStub{controllerRuntimeStub: base, limit: 1}
	model := &decisionModelStub{decisions: []models.ModelDecision{
		{Proposal: &models.ActionProposalBatch{Actions: []models.ActionProposal{
			{Name: "record", Input: json.RawMessage(`{"value":"one"}`)},
			{Name: "record", Input: json.RawMessage(`{"value":"two"}`)},
		}}},
		{Final: &models.FinalDraft{Text: "Final without Actions."}},
	}}

	if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	if len(model.requests) != 2 || len(model.requests[0].ActionDefinitions) != 1 || len(model.requests[1].ActionDefinitions) != 1 {
		t.Fatalf("Action definitions across budget fallback = %+v", model.requests)
	}
	if messages := model.requests[1].Messages; len(messages) == 0 || !strings.Contains(messages[len(messages)-1].Content, "remaining Action budget") {
		t.Fatalf("budget recovery detail = %+v", messages)
	}
	if len(executionOrder) != 0 {
		t.Fatalf("over-capacity Actions executed = %v", executionOrder)
	}
	if len(runtime.checkpoints) != 1 || runtime.checkpoints[0].IdentityKey != "decision:1/final" {
		t.Fatalf("accepted checkpoints = %+v", runtime.checkpoints)
	}
	if len(runtime.published) != 1 || runtime.published[0].Text != "Final without Actions." {
		t.Fatalf("published = %+v", runtime.published)
	}
}

func TestControllerFailsWhenReservedFinalDecisionProposesAction(t *testing.T) {
	executionOrder := make([]string, 0)
	registry, err := NewActionRegistry(&recordingAction{name: "record", order: &executionOrder})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &controllerRuntimeStub{execution: defaultControllerExecution()}
	runtime.execution.ActionDecisionLimit = 0
	model := &decisionModelStub{decisions: []models.ModelDecision{
		{Proposal: &models.ActionProposalBatch{Actions: []models.ActionProposal{
			{Name: "record", Input: json.RawMessage(`{"value":"forbidden"}`)},
		}}},
	}}

	err = NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt)
	if err == nil {
		t.Fatal("reserved Action proposal error = nil")
	}
	if len(model.requests) != 1 || len(model.requests[0].ActionDefinitions) != 0 {
		t.Fatalf("reserved request = %+v", model.requests)
	}
	if len(runtime.failed) != 1 || runtime.failed[0] != ErrorAgentBudgetExhausted {
		t.Fatalf("failure codes = %v", runtime.failed)
	}
	if len(runtime.checkpoints) != 0 || len(runtime.published) != 0 || len(executionOrder) != 0 {
		t.Fatalf("forbidden side effects checkpoints=%v published=%v Actions=%v", runtime.checkpoints, runtime.published, executionOrder)
	}
}

func TestControllerPublishesAcceptedFinalAfterRecoveryWithoutModelCall(t *testing.T) {
	runtime := &controllerRuntimeStub{execution: defaultControllerExecution()}
	final, err := NewFinalDraftCheckpoint(1, models.FinalDraft{Text: "Already accepted."})
	if err != nil {
		t.Fatal(err)
	}
	runtime.checkpoints = []Checkpoint{{SequenceNo: 1, PendingCheckpoint: final}}
	model := &decisionModelStub{}
	registry, err := NewActionRegistry(NewCalculateAction())
	if err != nil {
		t.Fatal(err)
	}

	if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	if len(model.requests) != 0 || len(runtime.checkpoints) != 1 {
		t.Fatalf("recovery called model or changed checkpoints: calls=%d checkpoints=%+v", len(model.requests), runtime.checkpoints)
	}
	if len(runtime.published) != 1 || runtime.published[0].Text != "Already accepted." {
		t.Fatalf("published = %+v", runtime.published)
	}
}

func TestControllerRejectsAnInvalidWholeBatchWithoutPartialAcceptance(t *testing.T) {
	executionOrder := make([]string, 0)
	registry, err := NewActionRegistry(&recordingAction{name: "record", order: &executionOrder})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &controllerRuntimeStub{execution: defaultControllerExecution()}
	model := &decisionModelStub{decisions: []models.ModelDecision{{Proposal: &models.ActionProposalBatch{Actions: []models.ActionProposal{
		{Name: "record", Input: json.RawMessage(`{"value":"valid-first"}`)},
		{Name: "record", Input: json.RawMessage(`{"value":""}`)},
	}}}}}

	if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err == nil {
		t.Fatal("invalid whole batch returned nil error")
	}
	if len(runtime.failed) != 1 || runtime.failed[0] != string(models.ErrorInvalidResponse) {
		t.Fatalf("failure codes=%v", runtime.failed)
	}
	if len(runtime.checkpoints) != 0 || len(executionOrder) != 0 || len(runtime.published) != 0 {
		t.Fatalf("partially accepted batch checkpoints=%v Actions=%v published=%v", runtime.checkpoints, executionOrder, runtime.published)
	}
}

func TestControllerReturnsTransientModelFailureToRoleExecutorWithoutTerminalizing(t *testing.T) {
	registry, err := NewActionRegistry(NewCalculateAction())
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []models.ErrorKind{models.ErrorTimeout, models.ErrorUnavailable} {
		runtime := &controllerRuntimeStub{execution: defaultControllerExecution()}
		modelErr := &models.ModelError{Kind: kind, Err: errors.New("private provider detail")}
		err := NewController(runtime, &decisionModelStub{err: modelErr}, registry).Execute(context.Background(), runtime.execution.Attempt)
		if !errors.Is(err, modelErr) || len(runtime.failed) != 0 {
			t.Fatalf("kind=%s err=%v terminal failures=%v", kind, err, runtime.failed)
		}
	}
}

func TestControllerRetriesInvalidModelResponseForOptedInRuntime(t *testing.T) {
	registry, err := NewActionRegistry(NewCalculateAction())
	if err != nil {
		t.Fatal(err)
	}
	runtime := &invalidResponseRecoveryRuntimeStub{
		controllerRuntimeStub: &controllerRuntimeStub{execution: defaultControllerExecution()},
		limit:                 2,
	}
	modelErr := &models.ModelError{Kind: models.ErrorInvalidResponse, Err: errors.New("malformed provider response")}
	model := &decisionModelStub{
		errors:    []error{modelErr},
		decisions: []models.ModelDecision{{Final: &models.FinalDraft{Text: "Recovered final."}}},
	}

	if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	if len(model.requests) != 2 {
		t.Fatalf("model calls = %d, want 2", len(model.requests))
	}
	if len(runtime.failed) != 0 {
		t.Fatalf("terminal failures = %v", runtime.failed)
	}
	if len(runtime.published) != 1 || runtime.published[0].Text != "Recovered final." {
		t.Fatalf("published = %+v", runtime.published)
	}
	if len(model.requests[1].Messages) == 0 || !strings.Contains(model.requests[1].Messages[len(model.requests[1].Messages)-1].Content, "invalid") {
		t.Fatalf("retry request is missing repair directive: %+v", model.requests[1].Messages)
	}
}

func TestControllerDoesNotRetryInvalidModelResponseWithoutRuntimeOptIn(t *testing.T) {
	registry, err := NewActionRegistry(NewCalculateAction())
	if err != nil {
		t.Fatal(err)
	}
	runtime := &controllerRuntimeStub{execution: defaultControllerExecution()}
	modelErr := &models.ModelError{Kind: models.ErrorInvalidResponse, Err: errors.New("malformed provider response")}
	model := &decisionModelStub{err: modelErr}

	err = NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt)
	if !errors.Is(err, modelErr) || len(model.requests) != 1 {
		t.Fatalf("err=%v model calls=%d", err, len(model.requests))
	}
	if len(runtime.failed) != 1 || runtime.failed[0] != string(models.ErrorInvalidResponse) {
		t.Fatalf("terminal failures = %v", runtime.failed)
	}
}

func TestControllerStopsInvalidModelResponseRecoveryAtRuntimeLimit(t *testing.T) {
	registry, err := NewActionRegistry(NewCalculateAction())
	if err != nil {
		t.Fatal(err)
	}
	runtime := &invalidResponseRecoveryRuntimeStub{
		controllerRuntimeStub: &controllerRuntimeStub{execution: defaultControllerExecution()},
		limit:                 2,
	}
	modelErr := &models.ModelError{Kind: models.ErrorInvalidResponse, Err: errors.New("malformed provider response")}
	model := &decisionModelStub{err: modelErr}

	err = NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt)
	if !errors.Is(err, modelErr) || len(model.requests) != 3 {
		t.Fatalf("err=%v model calls=%d", err, len(model.requests))
	}
	if len(runtime.failed) != 1 || runtime.failed[0] != string(models.ErrorInvalidResponse) {
		t.Fatalf("terminal failures = %v", runtime.failed)
	}
}

func TestControllerRetriesRuntimeRejectedDecisionBeforeCheckpointAcceptance(t *testing.T) {
	registry, err := NewActionRegistry(NewCalculateAction())
	if err != nil {
		t.Fatal(err)
	}
	runtime := &decisionResponsePreparationRuntimeStub{
		invalidResponseRecoveryRuntimeStub: &invalidResponseRecoveryRuntimeStub{
			controllerRuntimeStub: &controllerRuntimeStub{execution: defaultControllerExecution()},
			limit:                 2,
		},
	}
	model := &decisionModelStub{decisions: []models.ModelDecision{
		{Final: &models.FinalDraft{Text: "invalid plan"}},
		{Final: &models.FinalDraft{Text: "valid plan"}},
	}}

	if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	if len(model.requests) != 2 {
		t.Fatalf("model calls = %d, want 2", len(model.requests))
	}
	if len(runtime.checkpoints) != 1 || runtime.checkpoints[0].Kind != CheckpointFinalDraft {
		t.Fatalf("checkpoints = %+v", runtime.checkpoints)
	}
	if len(runtime.published) != 1 || runtime.published[0].Text != "valid plan" {
		t.Fatalf("published = %+v", runtime.published)
	}
}

func TestControllerReturnsToolCallErrorToRoleExecutorWithoutTerminalizing(t *testing.T) {
	tests := []struct {
		name string
		kind ToolErrorKind
		code string
	}{
		{name: "retryable infrastructure", kind: ToolErrorInfrastructure, code: "tool_execution_failed"},
		{name: "safe terminal invariant", kind: ToolErrorInvariant, code: "materialized_tool_mismatch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry, err := NewActionRegistry(&toolErrorAction{
				name: "tool_error",
				err:  &ToolCallError{Kind: test.kind, Code: test.code},
			})
			if err != nil {
				t.Fatal(err)
			}
			runtime := &controllerRuntimeStub{execution: defaultControllerExecution()}
			model := &decisionModelStub{decisions: []models.ModelDecision{{Proposal: &models.ActionProposalBatch{Actions: []models.ActionProposal{
				{Name: "tool_error", Input: json.RawMessage(`{}`)},
			}}}}}

			err = NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt)
			var toolErr *ToolCallError
			if !errors.As(err, &toolErr) || toolErr.Kind != test.kind || toolErr.Code != test.code {
				t.Fatalf("controller error = %v, want %s/%s", err, test.kind, test.code)
			}
			if len(runtime.failed) != 0 {
				t.Fatalf("ToolCallError must not be terminalized by Controller: %v", runtime.failed)
			}
			if len(runtime.checkpoints) != 1 || runtime.checkpoints[0].Kind != CheckpointActionProposal {
				t.Fatalf("ToolCallError must not append an Action Result checkpoint: %+v", runtime.checkpoints)
			}
		})
	}
}

// TestControllerParallelBatchCommitsSuccessfulSiblingsWhenFirstActionFails is
// the case the plan's relaxed checkpoint ordering exists for: a proposal
// batch where every action is registered ToolParallel, and the *first*
// action (index 0) fails while its siblings succeed. Before the relaxed
// ordering (checkpoint_prefix.go), committing index 1/2 without index 0
// would have been rejected as "out of order" and the whole attempt would
// have been unable to preserve the completed work. This test proves: all
// three actions actually ran (the batch is genuinely concurrent, not
// silently falling back to sequential), the failing action's ToolCallError
// is surfaced without terminalizing the attempt, and the two successful
// siblings are durably committed so a retried attempt only has to redo the
// failed index.
func TestControllerParallelBatchCommitsSuccessfulSiblingsWhenFirstActionFails(t *testing.T) {
	catalog, err := agentcatalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	calculate := testMCPAction("calculate")
	calculate.err = errors.New("boom")
	currentTime := testMCPAction("current_time")
	searchEvidence := testMCPAction("search_evidence")
	registry, err := NewMCPToolRegistry(
		MCPToolRegistration{Action: calculate, Scheduling: agentcatalog.ToolParallel},
		MCPToolRegistration{Action: currentTime, Scheduling: agentcatalog.ToolParallel},
		MCPToolRegistration{Action: searchEvidence, Scheduling: agentcatalog.ToolParallel},
		MCPToolRegistration{Action: testMCPAction("web_search"), Scheduling: agentcatalog.ToolOrderedSync},
		testDelegationMCPRegistration(),
	)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &controllerRuntimeStub{execution: defaultControllerExecution()}
	host, err := NewMCPToolHost(catalog, registry, runtime)
	if err != nil {
		t.Fatal(err)
	}
	directRegistry, err := NewActionRegistry(testMCPAction("calculate"), testMCPAction("current_time"), testMCPAction("search_evidence"))
	if err != nil {
		t.Fatal(err)
	}
	model := &decisionModelStub{decisions: []models.ModelDecision{
		{Proposal: &models.ActionProposalBatch{Actions: []models.ActionProposal{
			{Name: "calculate", Input: json.RawMessage(`{"operation":"add"}`)},
			{Name: "current_time", Input: json.RawMessage(`{}`)},
			{Name: "search_evidence", Input: json.RawMessage(`{}`)},
		}}},
	}}
	controller := NewMCPController(runtime, model, directRegistry, host, agentcatalog.MustParseReference("chat.leader@1"))

	err = controller.Execute(context.Background(), runtime.execution.Attempt)
	var toolErr *ToolCallError
	if !errors.As(err, &toolErr) || toolErr.Kind != ToolErrorInfrastructure || toolErr.Code != "tool_execution_failed" {
		t.Fatalf("controller error = %v", err)
	}
	if len(runtime.failed) != 0 {
		t.Fatalf("a retryable ToolCallError must not terminalize the attempt: %v", runtime.failed)
	}
	if len(calculate.calls) != 1 || len(currentTime.calls) != 1 || len(searchEvidence.calls) != 1 {
		t.Fatalf("all three actions must run concurrently regardless of the failure: calculate=%d current_time=%d search_evidence=%d",
			len(calculate.calls), len(currentTime.calls), len(searchEvidence.calls))
	}

	if len(runtime.checkpoints) != 3 {
		t.Fatalf("checkpoints = %+v", runtime.checkpoints)
	}
	if runtime.checkpoints[0].Kind != CheckpointActionProposal {
		t.Fatalf("checkpoint 0 = %+v", runtime.checkpoints[0])
	}
	gotIndices := map[int]bool{}
	for _, checkpoint := range runtime.checkpoints[1:] {
		if checkpoint.Kind != CheckpointActionResult || checkpoint.ActionIndex == nil {
			t.Fatalf("checkpoint = %+v", checkpoint)
		}
		gotIndices[*checkpoint.ActionIndex] = true
	}
	if gotIndices[0] || !gotIndices[1] || !gotIndices[2] {
		t.Fatalf("committed Action Result indices = %+v, want {1,2} and not 0", gotIndices)
	}

	prefix, err := LoadCheckpointPrefix(context.Background(), runtime.checkpoints)
	if err != nil {
		t.Fatal(err)
	}
	actions := prefix.Proposals[0].Actions
	if len(actions) != 3 || actions[0].Result != nil {
		t.Fatalf("action 0 must remain incomplete for the next attempt to retry: %+v", actions[0])
	}
	if actions[1].Result == nil || actions[1].Result.Status != ActionSucceeded {
		t.Fatalf("action 1 must be durably committed: %+v", actions[1])
	}
	if actions[2].Result == nil || actions[2].Result.Status != ActionSucceeded {
		t.Fatalf("action 2 must be durably committed: %+v", actions[2])
	}
}

func TestControllerDerivesActionResultByteBudgetsFromAcceptedCheckpoints(t *testing.T) {
	t.Run("one result", func(t *testing.T) {
		executionOrder := make([]string, 0, 1)
		registry, err := NewActionRegistry(&recordingAction{name: "record", order: &executionOrder})
		if err != nil {
			t.Fatal(err)
		}
		runtime := &controllerRuntimeStub{execution: defaultControllerExecution()}
		runtime.execution.ActionResultByteLimit = 1
		model := &decisionModelStub{decisions: []models.ModelDecision{{Proposal: &models.ActionProposalBatch{Actions: []models.ActionProposal{
			{Name: "record", Input: json.RawMessage(`{"value":"too-large"}`)},
		}}}}}

		if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err == nil {
			t.Fatal("per-result byte overflow returned nil error")
		}
		if len(runtime.failed) != 1 || runtime.failed[0] != ErrorAgentBudgetExhausted || len(runtime.checkpoints) != 1 || runtime.checkpoints[0].Kind != CheckpointActionProposal {
			t.Fatalf("failure/checkpoints=%v/%+v", runtime.failed, runtime.checkpoints)
		}
	})

	t.Run("run total", func(t *testing.T) {
		executionOrder := make([]string, 0, 1)
		registry, err := NewActionRegistry(&recordingAction{name: "record", order: &executionOrder})
		if err != nil {
			t.Fatal(err)
		}
		proposal, err := NewProposalCheckpoint(1, models.ActionProposalBatch{Actions: []models.ActionProposal{
			{Name: "record", Input: json.RawMessage(`{"value":"accepted"}`)},
			{Name: "record", Input: json.RawMessage(`{"value":"resume"}`)},
		}})
		if err != nil {
			t.Fatal(err)
		}
		acceptedResult, err := NewActionResultCheckpoint(1, 0, "decision:1/action:0", ActionResult{
			Status: ActionSucceeded, Output: json.RawMessage(`{"recorded":"accepted"}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		nextResult, err := NewActionResultCheckpoint(1, 1, "decision:1/action:1", ActionResult{
			Status: ActionSucceeded, Output: json.RawMessage(`{"recorded":"resume"}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		runtime := &controllerRuntimeStub{
			execution: defaultControllerExecution(),
			checkpoints: []Checkpoint{
				{SequenceNo: 1, PendingCheckpoint: proposal},
				{SequenceNo: 2, PendingCheckpoint: acceptedResult},
			},
		}
		runtime.execution.ActionResultsByteLimit = len(acceptedResult.Payload) + len(nextResult.Payload) - 1

		if err := NewController(runtime, &decisionModelStub{}, registry).Execute(context.Background(), runtime.execution.Attempt); err == nil {
			t.Fatal("total result byte overflow returned nil error")
		}
		if len(executionOrder) != 1 || executionOrder[0] != "resume" || len(runtime.checkpoints) != 2 || len(runtime.failed) != 1 || runtime.failed[0] != ErrorAgentBudgetExhausted {
			t.Fatalf("Action/checkpoints/failure=%v/%+v/%v", executionOrder, runtime.checkpoints, runtime.failed)
		}
	})
}

func TestControllerExternalizesEligibleLargeResultBeforeCheckpointBudget(t *testing.T) {
	store := &recordingToolResultStore{}
	externalizer := testToolResultExternalizer(store)
	action := &cacheableResultAction{output: json.RawMessage(`{"markdown":"` + strings.Repeat("decision-evidence-", 300) + `"}`)}
	registry, err := NewActionRegistry(action)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &controllerRuntimeStub{execution: defaultControllerExecution()}
	runtime.execution.AgentConfigID = "research.executor@14"
	runtime.execution.UserID = "user_a"
	runtime.execution.ChatID = "chat_a"
	runtime.execution.ActionResultByteLimit = 1024
	model := &decisionModelStub{decisions: []models.ModelDecision{
		{Proposal: &models.ActionProposalBatch{Actions: []models.ActionProposal{{Name: "cacheable_read", Input: json.RawMessage(`{"key":"paper"}`)}}}},
		{Final: &models.FinalDraft{Text: "done"}},
	}}

	controller := NewController(runtime, model, registry).WithToolResultCache(externalizer, 512)
	if err := controller.Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	if len(store.envelopes) != 1 {
		t.Fatalf("cache writes = %d", len(store.envelopes))
	}
	prefix, err := LoadCheckpointPrefix(context.Background(), runtime.checkpoints)
	if err != nil {
		t.Fatal(err)
	}
	result := prefix.Proposals[0].Actions[0].Result
	if result == nil || len(result.Output) > 512 || strings.Contains(string(result.Output), string(action.output)) {
		t.Fatalf("checkpoint result leaked large body: %#v", result)
	}
	units, err := ProjectChatLane(context.Background(), ChatLane{Turns: []ChatLaneTurn{{
		MessageID: "msg_externalized", Content: "read the paper",
		Runs: []ChatLaneRun{{RunID: runtime.execution.Attempt.RunID, Prefix: &prefix}},
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var adjacentActionMessage *models.ModelMessage
	for _, message := range FlattenContextUnits(units) {
		if message.Role == models.RoleAction {
			copyMessage := message
			adjacentActionMessage = &copyMessage
			break
		}
	}
	if adjacentActionMessage == nil || len(runtime.checkpoints) < 2 || runtime.checkpoints[1].Kind != CheckpointActionResult ||
		adjacentActionMessage.Content != string(runtime.checkpoints[1].Payload) {
		t.Fatalf("adjacent Action message diverged from bounded checkpoint: message=%+v checkpoints=%+v", adjacentActionMessage, runtime.checkpoints)
	}
	if len(adjacentActionMessage.Content) > 512 {
		t.Fatalf("model-visible Tool Result bytes=%d want <=512", len(adjacentActionMessage.Content))
	}
	if strings.Contains(adjacentActionMessage.Content, string(action.output)) || !strings.Contains(adjacentActionMessage.Content, `"next_offset":`) ||
		!strings.Contains(adjacentActionMessage.Content, `read_tool_result`) {
		t.Fatalf("adjacent Action message lost bounded continuation contract: %s", adjacentActionMessage.Content)
	}
	if len(runtime.failed) != 0 || len(runtime.published) != 1 {
		t.Fatalf("failed=%v published=%v", runtime.failed, runtime.published)
	}
}

func TestControllerCallsModelAgainWhenProposalWasNotAccepted(t *testing.T) {
	executionOrder := make([]string, 0, 1)
	registry, err := NewActionRegistry(&recordingAction{name: "record", order: &executionOrder})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &controllerRuntimeStub{
		execution:    defaultControllerExecution(),
		appendErrors: []error{errors.New("simulated process loss before proposal commit")},
	}
	proposal := models.ModelDecision{Proposal: &models.ActionProposalBatch{Actions: []models.ActionProposal{
		{Name: "record", Input: json.RawMessage(`{"value":"repeat-model-only"}`)},
	}}}
	model := &decisionModelStub{decisions: []models.ModelDecision{
		proposal,
		proposal,
		{Final: &models.FinalDraft{Text: "Recovered after an unaccepted response."}},
	}}
	controller := NewController(runtime, model, registry)

	if err := controller.Execute(context.Background(), runtime.execution.Attempt); err == nil {
		t.Fatal("simulated pre-commit loss returned nil error")
	}
	if len(model.requests) != 1 || len(runtime.checkpoints) != 0 || len(executionOrder) != 0 || len(runtime.failed) != 0 {
		t.Fatalf("first attempt model/checkpoints/Actions/failures=%d/%v/%v/%v", len(model.requests), runtime.checkpoints, executionOrder, runtime.failed)
	}
	if err := controller.Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	if len(model.requests) != 3 || len(executionOrder) != 1 || len(runtime.checkpoints) != 3 || len(runtime.published) != 1 {
		t.Fatalf("recovery model/Actions/checkpoints/published=%d/%v/%+v/%v", len(model.requests), executionOrder, runtime.checkpoints, runtime.published)
	}
}

func TestControllerResumesAfterProposalAndAfterLastResultWithoutRepeatingAcceptedNodes(t *testing.T) {
	proposal, err := NewProposalCheckpoint(1, models.ActionProposalBatch{Actions: []models.ActionProposal{
		{Name: "record", Input: json.RawMessage(`{"value":"first"}`)},
		{Name: "record", Input: json.RawMessage(`{"value":"second"}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	firstResult, err := NewActionResultCheckpoint(1, 0, "decision:1/action:0", ActionResult{
		Status: ActionSucceeded, Output: json.RawMessage(`{"recorded":"first"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	secondResult, err := NewActionResultCheckpoint(1, 1, "decision:1/action:1", ActionResult{
		Status: ActionSucceeded, Output: json.RawMessage(`{"recorded":"second"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name          string
		checkpoints   []Checkpoint
		wantExecution []string
		wantCount     int
	}{
		{
			name:          "after proposal",
			checkpoints:   []Checkpoint{{SequenceNo: 1, PendingCheckpoint: proposal}},
			wantExecution: []string{"first", "second"},
			wantCount:     4,
		},
		{
			name: "after last result",
			checkpoints: []Checkpoint{
				{SequenceNo: 1, PendingCheckpoint: proposal},
				{SequenceNo: 2, PendingCheckpoint: firstResult},
				{SequenceNo: 3, PendingCheckpoint: secondResult},
			},
			wantExecution: nil,
			wantCount:     4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executionOrder := make([]string, 0, 2)
			registry, err := NewActionRegistry(&recordingAction{name: "record", order: &executionOrder})
			if err != nil {
				t.Fatal(err)
			}
			runtime := &controllerRuntimeStub{execution: defaultControllerExecution(), checkpoints: append([]Checkpoint(nil), tt.checkpoints...)}
			model := &decisionModelStub{decisions: []models.ModelDecision{{Final: &models.FinalDraft{Text: "Recovered final."}}}}
			if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err != nil {
				t.Fatal(err)
			}
			if strings.Join(executionOrder, ",") != strings.Join(tt.wantExecution, ",") || len(model.requests) != 1 || len(runtime.checkpoints) != tt.wantCount || len(runtime.published) != 1 {
				t.Fatalf("recovery Actions/model/checkpoints/published=%v/%d/%+v/%v", executionOrder, len(model.requests), runtime.checkpoints, runtime.published)
			}
		})
	}
}

func defaultControllerExecution() Execution {
	return Execution{
		Attempt:                Attempt{JobID: "job_controller", RunID: "run_controller", AttemptNo: 1, LeaseToken: "00000000-0000-0000-0000-000000000001"},
		Model:                  "aliyun/qwen-flash",
		PromptVersion:          BarePromptVersion,
		TimeZone:               "Asia/Shanghai",
		DeadlineAt:             time.Now().Add(10 * time.Minute),
		ActionDecisionLimit:    4,
		FinalDecisionLimit:     1,
		ActionLimit:            8,
		ActionBatchLimit:       4,
		ActionResultByteLimit:  16 * 1024,
		ActionResultsByteLimit: 64 * 1024,
	}
}

type controllerRuntimeStub struct {
	execution    Execution
	checkpoints  []Checkpoint
	published    []models.FinalDraft
	failed       []string
	appendErrors []error
}

type queryContextControllerRuntimeStub struct {
	*controllerRuntimeStub
	queryRequest  models.ModelRequest
	fallback      models.ActionProposal
	historyPairs  int
	queryCalls    int
	composerCalls int
}

type contextPreparationRuntimeStub struct {
	*controllerRuntimeStub
	triggerReasons []string
}

type invalidResponseRecoveryRuntimeStub struct {
	*controllerRuntimeStub
	limit int
}

func (r *invalidResponseRecoveryRuntimeStub) InvalidModelResponseRetryLimit() int {
	return r.limit
}

type decisionResponsePreparationRuntimeStub struct {
	*invalidResponseRecoveryRuntimeStub
}

func (*decisionResponsePreparationRuntimeStub) PrepareDecisionResponse(_ context.Context, _ Execution, _ CheckpointPrefix, decision models.ModelDecision) (models.ModelDecision, error) {
	if decision.Final != nil && decision.Final.Text == "invalid plan" {
		return models.ModelDecision{}, errors.New("Research Plan source_strategy is invalid")
	}
	return decision, nil
}

func (r *contextPreparationRuntimeStub) PrepareDecisionRequest(_ context.Context, execution Execution, _ CheckpointPrefix, definitions []models.ActionDefinition, _ DecisionModel, triggerReason string) (models.ModelRequest, error) {
	r.triggerReasons = append(r.triggerReasons, triggerReason)
	return models.ModelRequest{Model: execution.Model, ActionDefinitions: cloneActionDefinitions(definitions)}, nil
}

type queryContextTraceRuntimeStub struct {
	*queryContextControllerRuntimeStub
	tracer       *agentobs.Tracer
	traceContext context.Context
}

func (r *queryContextTraceRuntimeStub) StartAttemptTrace(context.Context, Attempt) (context.Context, *agentobs.Tracer, error) {
	return r.traceContext, r.tracer, nil
}

func (r *queryContextControllerRuntimeStub) BuildQueryContextRequest(_ context.Context, _ Execution, definition models.ActionDefinition) (models.ModelRequest, models.ActionProposal, int, error) {
	r.queryCalls++
	request := r.queryRequest
	request.ActionDefinitions = []models.ActionDefinition{definition}
	return request, r.fallback, r.historyPairs, nil
}

func (r *queryContextControllerRuntimeStub) BuildDecisionRequest(_ context.Context, execution Execution, _ CheckpointPrefix, definitions []models.ActionDefinition) (models.ModelRequest, error) {
	r.composerCalls++
	return models.ModelRequest{
		Model: execution.Model,
		Messages: []models.ModelMessage{
			{Role: models.RoleSystem, Content: GroundedSystemPrompt},
			{Role: models.RoleUser, Content: "CS degree planner 里面有什么毕业要求"},
			{Role: models.RoleAssistant, Content: "A very long answer about degree requirements."},
			{Role: models.RoleUser, Content: "你好"},
		},
		ActionDefinitions: cloneActionDefinitions(definitions),
	}, nil
}

type finalPreparationRuntimeStub struct {
	*controllerRuntimeStub
	prepared models.FinalDraft
	calls    int
}

func (r *finalPreparationRuntimeStub) PrepareFinal(_ context.Context, _ Attempt, _ Execution, _ CheckpointPrefix, _ models.FinalDraft) (models.FinalDraft, error) {
	r.calls++
	return r.prepared, nil
}

func (r *controllerRuntimeStub) Load(_ context.Context, _ Attempt) (Execution, error) {
	return r.execution, nil
}

func (r *controllerRuntimeStub) LoadCheckpointPrefix(ctx context.Context, _ Attempt) (CheckpointPrefix, error) {
	return LoadCheckpointPrefix(ctx, r.checkpoints)
}

func (r *controllerRuntimeStub) BuildDecisionRequest(_ context.Context, execution Execution, _ CheckpointPrefix, definitions []models.ActionDefinition) (models.ModelRequest, error) {
	return models.ModelRequest{Model: execution.Model, ActionDefinitions: cloneActionDefinitions(definitions)}, nil
}

func (r *controllerRuntimeStub) CheckAuthority(context.Context, Attempt) error {
	return nil
}

func (r *controllerRuntimeStub) AppendCheckpoint(ctx context.Context, _ Attempt, pending PendingCheckpoint) (Checkpoint, error) {
	if len(r.appendErrors) > 0 {
		err := r.appendErrors[0]
		r.appendErrors = r.appendErrors[1:]
		return Checkpoint{}, err
	}
	checkpoint := Checkpoint{SequenceNo: len(r.checkpoints) + 1, PendingCheckpoint: pending, CreatedAt: time.Now()}
	candidate := append(append([]Checkpoint(nil), r.checkpoints...), checkpoint)
	if _, err := LoadCheckpointPrefix(ctx, candidate); err != nil {
		return Checkpoint{}, err
	}
	r.checkpoints = candidate
	return checkpoint, nil
}

func (r *controllerRuntimeStub) PublishFinal(_ context.Context, _ Attempt, draft models.FinalDraft) error {
	r.published = append(r.published, draft)
	return nil
}

func (r *controllerRuntimeStub) Fail(_ context.Context, _ Attempt, code string) error {
	r.failed = append(r.failed, code)
	return nil
}

type decisionModelStub struct {
	decisions []models.ModelDecision
	requests  []models.ModelRequest
	err       error
	errors    []error
}

func (m *decisionModelStub) Decide(_ context.Context, request models.ModelRequest) (models.ModelOutcome, error) {
	m.requests = append(m.requests, request)
	if len(m.errors) > 0 {
		err := m.errors[0]
		m.errors = m.errors[1:]
		if err != nil {
			return models.ModelOutcome{}, err
		}
	}
	if m.err != nil {
		return models.ModelOutcome{}, m.err
	}
	if len(m.decisions) == 0 {
		return models.ModelOutcome{}, errors.New("unexpected model decision")
	}
	decision := m.decisions[0]
	m.decisions = m.decisions[1:]
	resultKind := models.ModelResultFinalDraft
	if decision.Proposal != nil {
		resultKind = models.ModelResultActionProposal
	}
	return models.ModelOutcome{ModelDecision: decision, Metadata: models.ModelCallMetadata{
		RequestedModel: request.Model, ResultKind: resultKind,
	}}, nil
}

func boolRecordAttribute(record agentobs.Record, key string) bool {
	for _, item := range record.Attributes {
		if item.Key == key && item.Value.Kind == agentobs.ValueBool {
			return item.Value.Bool
		}
	}
	return false
}

type recordingAction struct {
	name            string
	order           *[]string
	calls           int
	started         chan<- struct{}
	proceed         <-chan struct{}
	attempts        []Attempt
	crashReplaySafe bool
}

type unavailableRecordingAction struct {
	recordingAction
}

func (*unavailableRecordingAction) Available(Execution) (bool, string) {
	return false, "test_unavailable"
}

type cacheableResultAction struct{ output json.RawMessage }

func (*cacheableResultAction) Definition() models.ActionDefinition {
	return models.ActionDefinition{Name: "cacheable_read", Description: "Return cacheable read-only data.", InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func (*cacheableResultAction) ValidateInput(json.RawMessage) error { return nil }

func (a *cacheableResultAction) Execute(context.Context, ActionRequest) (ActionResult, error) {
	return ActionResult{Status: ActionSucceeded, Output: append(json.RawMessage(nil), a.output...)}, nil
}

func (*cacheableResultAction) CacheLongToolResults(definition agentcatalog.Reference) bool {
	return definition.Identity == "research.executor" && definition.Version >= 10
}

func (a *recordingAction) CrashReplaySafe() bool { return a.crashReplaySafe }

type toolErrorAction struct {
	name string
	err  error
}

func (a *toolErrorAction) Definition() models.ActionDefinition {
	return models.ActionDefinition{
		Name: a.name, Description: "Return a classified ToolCallError.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}
}

func (a *toolErrorAction) ValidateInput(raw json.RawMessage) error {
	if len(raw) == 0 || !json.Valid(raw) {
		return errors.New("invalid tool error input")
	}
	return nil
}

func (a *toolErrorAction) Execute(context.Context, ActionRequest) (ActionResult, error) {
	if a.err == nil {
		return ActionResult{}, errors.New("toolErrorAction requires an error")
	}
	return ActionResult{}, a.err
}

func (a *recordingAction) Definition() models.ActionDefinition {
	return models.ActionDefinition{
		Name: a.name, Description: "Record an ordered value.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`),
	}
}

func (a *recordingAction) ValidateInput(raw json.RawMessage) error {
	var input struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &input); err != nil || input.Value == "" {
		return errors.New("invalid record input")
	}
	return nil
}

func (a *recordingAction) Execute(ctx context.Context, request ActionRequest) (ActionResult, error) {
	if err := ctx.Err(); err != nil {
		return ActionResult{}, err
	}
	var input struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(request.Input, &input); err != nil {
		return ActionResult{}, err
	}
	a.calls++
	a.attempts = append(a.attempts, request.Attempt)
	if a.started != nil {
		select {
		case a.started <- struct{}{}:
		case <-ctx.Done():
			return ActionResult{}, ctx.Err()
		}
	}
	if a.proceed != nil {
		select {
		case <-a.proceed:
		case <-ctx.Done():
			return ActionResult{}, ctx.Err()
		}
	}
	*a.order = append(*a.order, input.Value)
	output, _ := json.Marshal(map[string]string{"recorded": input.Value})
	return ActionResult{Status: ActionSucceeded, Output: output}, nil
}

type runStarterRuntimeStub struct {
	*controllerRuntimeStub
	model  *decisionModelStub
	starts []int
	err    error
}

func (r *runStarterRuntimeStub) StartRun(_ context.Context, attempt Attempt, execution Execution) error {
	if attempt != execution.Attempt {
		return errors.New("StartRun received another Attempt")
	}
	r.starts = append(r.starts, len(r.model.requests))
	return r.err
}

func TestControllerStartsRunBeforeFirstDecision(t *testing.T) {
	registry, err := NewActionRegistry(&recordingAction{name: "record", order: &[]string{}})
	if err != nil {
		t.Fatal(err)
	}
	model := &decisionModelStub{decisions: []models.ModelDecision{{Final: &models.FinalDraft{Text: "done"}}}}
	runtime := &runStarterRuntimeStub{controllerRuntimeStub: &controllerRuntimeStub{execution: defaultControllerExecution()}, model: model}
	if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	if len(runtime.starts) != 1 || runtime.starts[0] != 0 {
		t.Fatalf("starts=%v (model requests before each start)", runtime.starts)
	}
	failing := &runStarterRuntimeStub{controllerRuntimeStub: &controllerRuntimeStub{execution: defaultControllerExecution()}, model: &decisionModelStub{}, err: errors.New("scouts unavailable")}
	if err := NewController(failing, failing.model, registry).Execute(context.Background(), failing.execution.Attempt); err == nil || len(failing.model.requests) != 0 {
		t.Fatalf("err=%v requests=%d", err, len(failing.model.requests))
	}
}

type tokenBudgetRuntimeStub struct {
	*controllerRuntimeStub
	limit, consumed int64
}

func (r *tokenBudgetRuntimeStub) RecordModelUsage(_ context.Context, _ Attempt, usage models.ModelCallMetadata) error {
	if usage.InputTokens != nil {
		r.consumed += *usage.InputTokens
	}
	return nil
}

func (r *tokenBudgetRuntimeStub) ModelTokenBudgetExhausted(context.Context, Attempt) (bool, error) {
	return r.consumed >= r.limit, nil
}

type usageModelStub struct {
	*decisionModelStub
	inputTokens int64
}

func (m usageModelStub) Decide(ctx context.Context, request models.ModelRequest) (models.ModelOutcome, error) {
	outcome, err := m.decisionModelStub.Decide(ctx, request)
	tokens := m.inputTokens
	outcome.Metadata.InputTokens = &tokens
	return outcome, err
}

func TestControllerClosesToolUseOnceTheTokenBudgetIsSpent(t *testing.T) {
	executionOrder := make([]string, 0, 1)
	registry, err := NewActionRegistry(&recordingAction{name: "record", order: &executionOrder})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &tokenBudgetRuntimeStub{controllerRuntimeStub: &controllerRuntimeStub{execution: defaultControllerExecution()}, limit: 100}
	decisions := &decisionModelStub{decisions: []models.ModelDecision{
		{Proposal: &models.ActionProposalBatch{Actions: []models.ActionProposal{{Name: "record", Input: json.RawMessage(`{"value":"first"}`)}}}},
		{Final: &models.FinalDraft{Text: "Done within budget."}},
	}}
	if err := NewController(runtime, usageModelStub{decisionModelStub: decisions, inputTokens: 100}, registry).Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	if runtime.consumed != 200 || len(decisions.requests) != 2 {
		t.Fatalf("consumed=%d requests=%d", runtime.consumed, len(decisions.requests))
	}
	if len(decisions.requests[0].ActionDefinitions) == 0 || len(decisions.requests[1].ActionDefinitions) != 0 {
		t.Fatalf("tools offered: first=%d after budget=%d", len(decisions.requests[0].ActionDefinitions), len(decisions.requests[1].ActionDefinitions))
	}
}

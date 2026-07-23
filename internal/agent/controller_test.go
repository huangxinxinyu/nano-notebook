package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

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
	if len(runtime.published) != 1 || runtime.published[0].Text != "Finished in order." {
		t.Fatalf("published = %+v", runtime.published)
	}
	if len(runtime.failed) != 0 {
		t.Fatalf("terminal failures = %v", runtime.failed)
	}
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

func TestControllerRejectsOverCapacityBatchAndUsesActionDisabledFinalDecision(t *testing.T) {
	executionOrder := make([]string, 0)
	registry, err := NewActionRegistry(&recordingAction{name: "record", order: &executionOrder})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &controllerRuntimeStub{execution: defaultControllerExecution()}
	runtime.execution.ActionLimit = 1
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
	if len(model.requests) != 2 || len(model.requests[0].ActionDefinitions) != 1 || len(model.requests[1].ActionDefinitions) != 0 {
		t.Fatalf("Action definitions across budget fallback = %+v", model.requests)
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
	execution       Execution
	checkpoints     []Checkpoint
	published       []models.FinalDraft
	failed          []string
	authorityChecks int
	appendErrors    []error
}

type queryContextControllerRuntimeStub struct {
	*controllerRuntimeStub
	queryRequest  models.ModelRequest
	fallback      models.ActionProposal
	historyPairs  int
	queryCalls    int
	composerCalls int
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
	r.authorityChecks++
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
	name     string
	order    *[]string
	calls    int
	started  chan<- struct{}
	proceed  <-chan struct{}
	attempts []Attempt
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

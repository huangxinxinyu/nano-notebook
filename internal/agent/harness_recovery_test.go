package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

type scriptedModel struct {
	outcomes []models.ModelOutcome
	errs     []error
	requests []models.ModelRequest
}

func (m *scriptedModel) Decide(_ context.Context, request models.ModelRequest) (models.ModelOutcome, error) {
	index := len(m.requests)
	m.requests = append(m.requests, request)
	if index >= len(m.outcomes) {
		return models.ModelOutcome{}, errors.New("unexpected model call")
	}
	var err error
	if index < len(m.errs) {
		err = m.errs[index]
	}
	return m.outcomes[index], err
}

func finalOutcome(text, finishReason string) models.ModelOutcome {
	return models.ModelOutcome{
		ModelDecision: models.ModelDecision{Final: &models.FinalDraft{Text: text}},
		Metadata:      models.ModelCallMetadata{FinishReason: finishReason},
	}
}

func recoveryRuntime(limit int) *invalidResponseRecoveryRuntimeStub {
	return &invalidResponseRecoveryRuntimeStub{
		controllerRuntimeStub: &controllerRuntimeStub{execution: defaultControllerExecution()},
		limit:                 limit,
	}
}

func TestControllerRetriesATruncatedFinalAndNamesTheCause(t *testing.T) {
	registry, err := NewActionRegistry(NewCalculateAction())
	if err != nil {
		t.Fatal(err)
	}
	runtime := recoveryRuntime(2)
	model := &scriptedModel{outcomes: []models.ModelOutcome{finalOutcome("Half an ans", "length"), finalOutcome("A whole answer.", "stop")}}
	if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	if len(model.requests) != 2 || len(runtime.published) != 1 || runtime.published[0].Text != "A whole answer." {
		t.Fatalf("requests=%d published=%+v", len(model.requests), runtime.published)
	}
	messages := model.requests[1].Messages
	if !strings.Contains(messages[len(messages)-1].Content, "output limit") {
		t.Fatalf("retry does not name truncation: %+v", messages[len(messages)-1])
	}
}

func TestControllerPublishesATruncatedFinalWithANoticeOnceRetriesRunOut(t *testing.T) {
	registry, err := NewActionRegistry(NewCalculateAction())
	if err != nil {
		t.Fatal(err)
	}
	runtime := recoveryRuntime(1)
	model := &scriptedModel{outcomes: []models.ModelOutcome{finalOutcome("Half an ans", "length"), finalOutcome("Half again", "length")}}
	if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	if len(runtime.published) != 1 || !strings.HasPrefix(runtime.published[0].Text, "Half again") || !strings.Contains(runtime.published[0].Text, truncatedFinalNotice) {
		t.Fatalf("published=%+v", runtime.published)
	}
}

func TestControllerGivesTheModelTheProviderParseFailureOnRetry(t *testing.T) {
	registry, err := NewActionRegistry(NewCalculateAction())
	if err != nil {
		t.Fatal(err)
	}
	runtime := recoveryRuntime(1)
	model := &scriptedModel{
		outcomes: []models.ModelOutcome{{}, finalOutcome("Recovered.", "stop")},
		errs:     []error{&models.ModelError{Kind: models.ErrorInvalidResponse, Err: errors.New("tool call arguments were cut off")}},
	}
	if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	messages := model.requests[1].Messages
	if !strings.Contains(messages[len(messages)-1].Content, "tool call arguments were cut off") {
		t.Fatalf("retry lost the cause: %q", messages[len(messages)-1].Content)
	}
}

func TestControllerCountsTokensOfAResponseItCouldNotAccept(t *testing.T) {
	registry, err := NewActionRegistry(NewCalculateAction())
	if err != nil {
		t.Fatal(err)
	}
	stub := &controllerRuntimeStub{execution: defaultControllerExecution()}
	runtime := &tokenBudgetRuntimeStub{controllerRuntimeStub: stub, limit: 1000}
	tokens := int64(700)
	model := &scriptedModel{
		outcomes: []models.ModelOutcome{{Metadata: models.ModelCallMetadata{InputTokens: &tokens}}},
		errs:     []error{&models.ModelError{Kind: models.ErrorInvalidResponse, Err: errors.New("bad")}},
	}
	_ = NewController(runtime, model, registry).Execute(context.Background(), stub.execution.Attempt)
	if runtime.consumed != 700 {
		t.Fatalf("consumed=%d, want 700", runtime.consumed)
	}
}

func TestControllerRetriesATransientModelFailureDuringContextPreparation(t *testing.T) {
	registry, err := NewActionRegistry(NewCalculateAction())
	if err != nil {
		t.Fatal(err)
	}
	runtime := &failingPreparationRuntimeStub{
		controllerRuntimeStub: &controllerRuntimeStub{execution: defaultControllerExecution()},
		err:                   &models.ModelError{Kind: models.ErrorUnavailable, Err: errors.New("503")},
	}
	err = NewController(runtime, &scriptedModel{}, registry).Execute(context.Background(), runtime.execution.Attempt)
	if ClassifyAttempt(err, nil).Disposition != AttemptRetryable || len(runtime.failed) != 0 {
		t.Fatalf("err=%v failed=%v", err, runtime.failed)
	}
}

func TestControllerStillFailsContextPreparationThatCannotRecover(t *testing.T) {
	registry, err := NewActionRegistry(NewCalculateAction())
	if err != nil {
		t.Fatal(err)
	}
	runtime := &failingPreparationRuntimeStub{
		controllerRuntimeStub: &controllerRuntimeStub{execution: defaultControllerExecution()},
		err:                   ErrContextBudgetExceeded,
	}
	_ = NewController(runtime, &scriptedModel{}, registry).Execute(context.Background(), runtime.execution.Attempt)
	if len(runtime.failed) != 1 || runtime.failed[0] != "context_failed" {
		t.Fatalf("failed=%v", runtime.failed)
	}
}

type failingPreparationRuntimeStub struct {
	*controllerRuntimeStub
	err error
}

func (r *failingPreparationRuntimeStub) PrepareDecisionRequest(context.Context, Execution, CheckpointPrefix, []models.ActionDefinition, DecisionModel, string) (models.ModelRequest, error) {
	return models.ModelRequest{}, r.err
}

func TestAttemptDispositionHonoursProviderRejectionAndRetryAfter(t *testing.T) {
	rejected := ClassifyAttempt(&models.ModelError{Kind: models.ErrorRejected, Err: errors.New("401")}, nil)
	if rejected.Disposition != AttemptTerminal || rejected.ErrorCode != string(models.ErrorRejected) {
		t.Fatalf("rejected=%+v", rejected)
	}
	limited := ClassifyAttempt(&models.ModelError{Kind: models.ErrorUnavailable, Err: errors.New("429"), RetryAfter: 40 * time.Second}, nil)
	if limited.Disposition != AttemptRetryable || limited.Backoff != 40*time.Second || !limited.Valid() {
		t.Fatalf("limited=%+v", limited)
	}
}

func TestResearchCompactionRetriesInvalidOutputOnceAndKeepsTheCause(t *testing.T) {
	decode := func(payload []byte) (string, error) {
		if string(payload) != "ok" {
			return "", errors.New("bad capsule")
		}
		return string(payload), nil
	}
	model := &scriptedModel{outcomes: []models.ModelOutcome{finalOutcome("nope", "stop"), finalOutcome("ok", "stop")}}
	if got, err := decideResearchCompaction(context.Background(), model, models.ModelRequest{}, decode); err != nil || got != "ok" || len(model.requests) != 2 {
		t.Fatalf("got=%q err=%v calls=%d", got, err, len(model.requests))
	}

	cut := &scriptedModel{outcomes: []models.ModelOutcome{finalOutcome("ok", "length"), finalOutcome("ok", "length")}}
	if _, err := decideResearchCompaction(context.Background(), cut, models.ModelRequest{}, decode); err == nil {
		t.Fatal("truncated capsule was accepted")
	}

	down := &models.ModelError{Kind: models.ErrorUnavailable, Err: errors.New("503")}
	transient := &scriptedModel{outcomes: []models.ModelOutcome{{}}, errs: []error{down}}
	_, err := decideResearchCompaction(context.Background(), transient, models.ModelRequest{}, decode)
	if !errors.Is(err, down) || len(transient.requests) != 1 {
		t.Fatalf("err=%v calls=%d", err, len(transient.requests))
	}
}

func TestResearchCompactionFailureContinuesOnlyWhileTheRequestStillFits(t *testing.T) {
	down := &models.ModelError{Kind: models.ErrorUnavailable, Err: errors.New("503")}
	if !researchCompactionOptional(CompactionTriggerThreshold, 90, 100) {
		t.Fatal("a request under the safe budget must be able to continue")
	}
	if researchCompactionOptional(CompactionTriggerThreshold, 110, 100) || researchCompactionOptional(CompactionTriggerProviderOverflow, 90, 100) {
		t.Fatal("an oversized request cannot continue uncompacted")
	}
	if err := researchCompactionError(down); !errors.Is(err, down) {
		t.Fatalf("transient cause lost: %v", err)
	}
	if err := researchCompactionError(errors.New("bad capsule")); !errors.Is(err, ErrContextBudgetExceeded) {
		t.Fatalf("invalid output err=%v", err)
	}
}

type usageRecorderStub struct{ input int64 }

func (s *usageRecorderStub) RecordModelUsage(_ context.Context, _ Attempt, usage models.ModelCallMetadata) error {
	s.input += *usage.InputTokens
	return nil
}

func TestResearchCompactionCallsCountAgainstTheBudget(t *testing.T) {
	tokens := int64(321)
	recorder := &usageRecorderStub{}
	inner := &scriptedModel{outcomes: []models.ModelOutcome{{Metadata: models.ModelCallMetadata{InputTokens: &tokens}}}}
	metered := meteredCompactionModel{model: inner, usage: recorder}
	if _, err := metered.Decide(context.Background(), models.ModelRequest{}); err != nil {
		t.Fatal(err)
	}
	if recorder.input != 321 {
		t.Fatalf("input=%d", recorder.input)
	}
}

func TestResearchMessagesKeepTheChangingLedgerAfterTheTrajectory(t *testing.T) {
	system := models.ModelMessage{Role: models.RoleSystem, Content: "system"}
	request := models.ModelMessage{Role: models.RoleUser, Content: "request"}
	step := models.ModelMessage{Role: models.RoleAction, Content: "result", ActionCallID: "a"}
	got := assembleThresholdResearchMessages(system, request, []models.ModelMessage{step}, "Evidence Ledger: 3")
	if len(got) != 4 || got[2].Content != "result" || got[3].Content != "Evidence Ledger: 3" {
		t.Fatalf("messages=%+v", got)
	}
	if none := assembleThresholdResearchMessages(system, request, []models.ModelMessage{step}, ""); len(none) != 3 {
		t.Fatalf("empty ledger added a message: %+v", none)
	}
}

func TestEstimateCountsChineseTextCloserToProviderTokens(t *testing.T) {
	request := func(content string) models.ModelRequest {
		return models.ModelRequest{Model: "m", Messages: []models.ModelMessage{{Role: models.RoleUser, Content: content}}}
	}
	base, err := EstimateModelRequestTokens(request(""))
	if err != nil {
		t.Fatal(err)
	}
	chinese, err := EstimateModelRequestTokens(request(strings.Repeat("上下文压缩", 200)))
	if err != nil {
		t.Fatal(err)
	}
	if grown := chinese.Tokens - base.Tokens; grown < 1000 {
		t.Fatalf("1000 Chinese characters estimated at %d tokens", grown)
	}
}

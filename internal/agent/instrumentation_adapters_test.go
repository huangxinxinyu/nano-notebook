package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/memory"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/semconv"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/replay"
	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
)

func TestModelAdapterRecordsNormalizedMetadataWithoutContent(t *testing.T) {
	tracer, exporter, ctx := instrumentationTestTracer(t)
	input, output, total := int64(18), int64(9), int64(27)
	model := outcomeModelFunc(func(context.Context, models.ModelRequest) (models.ModelOutcome, error) {
		return models.ModelOutcome{
			ModelDecision: models.ModelDecision{Final: &models.FinalDraft{Text: "private response"}},
			Metadata: models.ModelCallMetadata{
				RequestedModel: "aliyun/qwen-flash", ResultKind: models.ModelResultFinalDraft,
				FinishReason: "stop", InputTokens: &input, OutputTokens: &output, TotalTokens: &total,
			},
		}, nil
	})
	request := models.ModelRequest{
		Model:    "aliyun/qwen-flash",
		Messages: []models.ModelMessage{{Role: models.RoleUser, Content: "private prompt"}},
	}
	outcome, err := InvokeDecisionModel(ctx, tracer, model, request, 1)
	if err != nil || outcome.Final == nil {
		t.Fatalf("InvokeDecisionModel = %+v, %v", outcome, err)
	}
	records := exporter.Records()
	if len(records) != 4 || records[2].Name != "agent.model.call" || records[3].Name != "agent.model.call" {
		t.Fatalf("records = %#v", records)
	}
	for _, record := range records[2:] {
		payload, err := record.CanonicalPayload()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(payload), "private prompt") || strings.Contains(string(payload), "private response") {
			t.Fatalf("raw model content entered Trace: %s", payload)
		}
	}
}

func TestModelAdapterStagesReplayAndBindsBothSidesOfThePhysicalCall(t *testing.T) {
	tracer, exporter, ctx := instrumentationTestTracer(t)
	stager := &recordingReplayStager{}
	model := outcomeModelFunc(func(context.Context, models.ModelRequest) (models.ModelOutcome, error) {
		return models.ModelOutcome{
			ModelDecision: models.ModelDecision{Final: &models.FinalDraft{Text: "answer"}},
			Metadata: models.ModelCallMetadata{
				RequestedModel: "aliyun/qwen-flash", ResultKind: models.ModelResultFinalDraft,
			},
		}, nil
	})
	_, err := InvokeDecisionModel(ctx, tracer, model, models.ModelRequest{
		Model: "aliyun/qwen-flash", Messages: []models.ModelMessage{{Role: models.RoleUser, Content: "question"}},
	}, 1, ModelTraceOptions{
		StartIdentity:    "run/run-1/attempt/1/model/1/start",
		RequestIdentity:  "run/run-1/attempt/1/model/1/replay/request",
		DecisionIdentity: "run/run-1/attempt/1/model/1/replay/decision",
		ReplayStager:     stager,
	})
	if err != nil {
		t.Fatalf("InvokeDecisionModel: %v", err)
	}
	if len(stager.requests) != 2 || stager.requests[0].Payload.Class != replay.ClassModelRequest || stager.requests[1].Payload.Class != replay.ClassModelDecision {
		t.Fatalf("staged Replay requests = %#v", stager.requests)
	}
	records := exporter.Records()
	if got := stringAttribute(records[2], replay.ModelRequestAttachmentKey); got != "attachment-model_request" {
		t.Fatalf("Model start Replay Attachment = %q", got)
	}
	if got := stringAttribute(records[3], replay.ModelDecisionAttachmentKey); got != "attachment-model_decision" {
		t.Fatalf("Model terminal Replay Attachment = %q", got)
	}
}

func TestActionAdapterPreservesDomainResultAndRecordsPhysicalExecution(t *testing.T) {
	tracer, exporter, ctx := instrumentationTestTracer(t)
	_, prior, err := tracer.StartSpan(ctx, agentobs.SpanStart{Name: "agent.action"})
	if err != nil {
		t.Fatal(err)
	}
	action := adapterAction{}
	result, err := InvokeAgentAction(ctx, tracer, action, "decision:1/action:0", ActionRequest{
		Input: json.RawMessage(`{"secret":"not-for-trace"}`),
	}, ActionTraceOptions{StartIdentity: "physical/action/2/start", RetryTarget: &prior})
	if err != nil || result.Status != ActionDomainError || result.ErrorCode != "clock_unavailable" {
		t.Fatalf("InvokeAgentAction = %+v, %v", result, err)
	}
	records := exporter.Records()
	if len(records) != 6 || records[3].Name != "agent.action" || records[4].Kind != agentobs.RecordLink || records[5].Name != "agent.action" {
		t.Fatalf("records = %#v", records)
	}
	for _, record := range records[3:] {
		payload, err := record.CanonicalPayload()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(payload), "not-for-trace") {
			t.Fatalf("raw Action input entered Trace: %s", payload)
		}
	}
}

func TestActionAdapterStagesReplayAndBindsBothSidesOfThePhysicalCall(t *testing.T) {
	tracer, exporter, ctx := instrumentationTestTracer(t)
	stager := &recordingReplayStager{}
	_, err := InvokeAgentAction(ctx, tracer, adapterAction{}, "decision:1/action:0", ActionRequest{
		Input: json.RawMessage(`{"timezone":"Asia/Shanghai"}`),
	}, ActionTraceOptions{
		StartIdentity:  "run/run-1/attempt/1/action/0/start",
		InputIdentity:  "run/run-1/attempt/1/action/0/replay/input",
		ResultIdentity: "run/run-1/attempt/1/action/0/replay/result",
		ReplayStager:   stager,
	})
	if err != nil {
		t.Fatalf("InvokeAgentAction: %v", err)
	}
	if len(stager.requests) != 2 || stager.requests[0].Payload.Class != replay.ClassActionInput || stager.requests[1].Payload.Class != replay.ClassActionResult {
		t.Fatalf("staged Replay requests = %#v", stager.requests)
	}
	records := exporter.Records()
	if got := stringAttribute(records[2], replay.ActionInputAttachmentKey); got != "attachment-action_input" {
		t.Fatalf("Action start Replay Attachment = %q", got)
	}
	if got := stringAttribute(records[3], replay.ActionResultAttachmentKey); got != "attachment-action_result" {
		t.Fatalf("Action terminal Replay Attachment = %q", got)
	}
}

func TestSearchEvidenceActionRecordsRAGMetadataWithoutQueryOrEvidenceBodies(t *testing.T) {
	tracer, exporter, ctx := instrumentationTestTracer(t)
	backend := &evidenceSearchStub{result: retrieval.SearchResult{
		Candidates: []retrieval.EvidenceCandidate{{ID: "chunk_a", Preview: "secret evidence"}},
		Degraded:   true, Degradations: []string{"reranker_unavailable"},
		Diagnostics: retrieval.SearchDiagnostics{
			Dense:        retrieval.SearchStageDiagnostics{Completed: true, CandidateIDs: []string{"chunk_a", "chunk_b"}},
			BM25:         retrieval.SearchStageDiagnostics{Completed: true, CandidateIDs: []string{"chunk_b"}},
			Fused:        retrieval.SearchStageDiagnostics{Completed: true, CandidateIDs: []string{"chunk_b", "chunk_a"}},
			EvidenceLoad: retrieval.SearchStageDiagnostics{Completed: true, CandidateIDs: []string{"chunk_a"}},
		},
	}}
	action := NewSearchEvidenceAction(backend)
	_, err := InvokeAgentAction(ctx, tracer, action, "decision:1/action:0", ActionRequest{
		Input:   json.RawMessage(`{"query":"private query","purpose":"compare stated methods"}`),
		Attempt: Attempt{RunID: "run"},
	})
	if err != nil {
		t.Fatal(err)
	}
	records := exporter.Records()
	start, terminal := records[len(records)-2], records[len(records)-1]
	if stringAttribute(start, TraceKeySearchPurpose) != "compare stated methods" ||
		stringAttribute(terminal, TraceKeyDenseCandidateIDs) != `["chunk_a","chunk_b"]` ||
		stringAttribute(terminal, TraceKeyRRFTransitionIDs) != `["chunk_b","chunk_a"]` ||
		stringAttribute(terminal, TraceKeyRetrievalDegradations) != `["reranker_unavailable"]` {
		t.Fatalf("RAG records=%#v", records)
	}
	for _, record := range records[len(records)-2:] {
		payload, payloadErr := record.CanonicalPayload()
		if payloadErr != nil {
			t.Fatal(payloadErr)
		}
		if strings.Contains(string(payload), "private query") || strings.Contains(string(payload), "secret evidence") {
			t.Fatalf("sensitive RAG body entered Trace: %s", payload)
		}
	}
}

func TestClaimSupportAdapterStagesReplayAndRecordsOnlySafeRAGMetadata(t *testing.T) {
	tracer, exporter, ctx := instrumentationTestTracer(t)
	stager := &recordingReplayStager{}
	inputTokens, totalTokens := int64(31), int64(39)
	request := models.ClaimSupportRequest{
		Model: "aliyun/qwen-plus", PromptVersion: "claim-support-v1", Answer: "private answer",
		Claims: []models.ClaimSupportInput{{
			Ordinal: 0, Text: "private claim",
			Evidence: []models.ClaimEvidence{{SourceID: "source-1", RevisionID: "revision-1", UnitID: "unit-1", StartRune: 0, EndRune: 14, Text: "private evidence"}},
		}},
	}
	verifier := claimSupportVerifierFunc(func(context.Context, models.ClaimSupportRequest) (models.ClaimSupportOutcome, error) {
		return models.ClaimSupportOutcome{
			Verdicts: []models.ClaimSupportVerdict{{Ordinal: 0, Supported: false}}, UncoveredClaims: []string{"private omitted claim"},
			Metadata: models.CapabilityMetadata{
				RequestedModel: "aliyun/qwen-plus", Provider: "aliyun", Model: "qwen-plus",
				InputTokens: &inputTokens, TotalTokens: &totalTokens,
			},
		}, nil
	})
	outcome, err := InvokeClaimSupportVerifier(ctx, tracer, verifier, request, ClaimSupportTraceOptions{
		StartIdentity:   "run/run-1/attempt/1/grounding/verifier/start",
		RequestIdentity: "run/run-1/attempt/1/grounding/verifier/replay/request",
		VerdictIdentity: "run/run-1/attempt/1/grounding/verifier/replay/verdict",
		ReplayStager:    stager,
	})
	if err != nil || len(outcome.Verdicts) != 1 {
		t.Fatalf("InvokeClaimSupportVerifier = %#v, %v", outcome, err)
	}
	if len(stager.requests) != 2 || stager.requests[0].Payload.Class != replay.ClassModelRequest || stager.requests[1].Payload.Class != replay.ClassModelDecision {
		t.Fatalf("staged Replay requests = %#v", stager.requests)
	}
	if !strings.Contains(string(stager.requests[1].Payload.Bytes), "private omitted claim") {
		t.Fatalf("verdict Replay omitted uncovered claim: %s", stager.requests[1].Payload.Bytes)
	}
	if !strings.Contains(string(stager.requests[0].Payload.Bytes), "private answer") || !strings.Contains(string(stager.requests[0].Payload.Bytes), "private claim") || !strings.Contains(string(stager.requests[0].Payload.Bytes), "private evidence") {
		t.Fatalf("request Replay omitted verifier inputs: %s", stager.requests[0].Payload.Bytes)
	}
	records := exporter.Records()
	start, terminal := records[len(records)-2], records[len(records)-1]
	if stringAttribute(start, TraceKeyVerifierPromptVersion) != "claim-support-v1" ||
		int64Attribute(start, TraceKeyVerifierClaimCount) != 1 ||
		int64Attribute(start, TraceKeyVerifierEvidenceCount) != 1 ||
		int64Attribute(terminal, TraceKeyVerifierUnsupportedCount) != 2 {
		t.Fatalf("claim support records = %#v", records)
	}
	for _, record := range records[len(records)-2:] {
		payload, payloadErr := record.CanonicalPayload()
		if payloadErr != nil {
			t.Fatal(payloadErr)
		}
		if strings.Contains(string(payload), "private answer") || strings.Contains(string(payload), "private claim") || strings.Contains(string(payload), "private evidence") || strings.Contains(string(payload), "private omitted claim") {
			t.Fatalf("sensitive verifier body entered Trace: %s", payload)
		}
	}
}

func TestModelAdapterClassifiesObservedErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantKind   string
		wantStatus agentobs.Status
	}{
		{name: "invalid", err: &models.ModelError{Kind: models.ErrorInvalidResponse, Err: errors.New("bad")}, wantKind: string(models.ErrorInvalidResponse), wantStatus: agentobs.StatusError},
		{name: "timeout", err: &models.ModelError{Kind: models.ErrorTimeout, Err: context.DeadlineExceeded}, wantKind: string(models.ErrorTimeout), wantStatus: agentobs.StatusError},
		{name: "unavailable", err: &models.ModelError{Kind: models.ErrorUnavailable, Err: errors.New("down")}, wantKind: string(models.ErrorUnavailable), wantStatus: agentobs.StatusError},
		{name: "cancelled", err: context.Canceled, wantKind: string(models.ErrorUnavailable), wantStatus: agentobs.StatusCancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracer, exporter, ctx := instrumentationTestTracer(t)
			model := outcomeModelFunc(func(context.Context, models.ModelRequest) (models.ModelOutcome, error) {
				return models.ModelOutcome{}, tt.err
			})
			_, err := InvokeDecisionModel(ctx, tracer, model, models.ModelRequest{
				Model: "aliyun/qwen-flash", Messages: []models.ModelMessage{{Role: models.RoleUser, Content: "private"}},
			}, 1)
			if !errors.Is(err, tt.err) {
				t.Fatalf("error = %v, want %v", err, tt.err)
			}
			records := exporter.Records()
			terminal := records[len(records)-1]
			if terminal.Kind != agentobs.RecordSpanEnded || terminal.Status != tt.wantStatus || stringAttribute(terminal, semconv.ErrorKindKey) != tt.wantKind {
				t.Fatalf("terminal = %#v", terminal)
			}
		})
	}
}

func TestActionAdapterRecordsCancellation(t *testing.T) {
	tracer, exporter, ctx := instrumentationTestTracer(t)
	_, err := InvokeAgentAction(ctx, tracer, cancellingAdapterAction{}, "decision:1/action:0", ActionRequest{Input: json.RawMessage(`{}`)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelling Action error = %v", err)
	}
	records := exporter.Records()
	terminal := records[len(records)-1]
	if terminal.Kind != agentobs.RecordSpanEnded || terminal.Name != semconv.AgentAction || terminal.Status != agentobs.StatusCancelled {
		t.Fatalf("cancelled Action terminal = %#v", terminal)
	}
}

func instrumentationTestTracer(t *testing.T) (*agentobs.Tracer, *memory.Exporter, context.Context) {
	t.Helper()
	exporter := memory.New()
	runtime, err := agentobs.NewRuntime(agentobs.RuntimeConfig{Destinations: []agentobs.Destination{{
		Name: "memory", Class: agentobs.DeliveryRequired, Exporter: exporter,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	tracer, err := agentobs.NewTracer(agentobs.TracerConfig{Recorder: runtime, SemanticConventionVersion: TraceSemanticConventionVersion})
	if err != nil {
		t.Fatal(err)
	}
	rootCtx, _, err := tracer.StartTrace(context.Background(), agentobs.TraceStart{Name: TraceSpanAgentExecution})
	if err != nil {
		t.Fatal(err)
	}
	attemptCtx, _, err := tracer.StartSpan(rootCtx, agentobs.SpanStart{Name: TraceSpanJobAttempt})
	if err != nil {
		t.Fatal(err)
	}
	return tracer, exporter, attemptCtx
}

type outcomeModelFunc func(context.Context, models.ModelRequest) (models.ModelOutcome, error)

func (f outcomeModelFunc) Decide(ctx context.Context, request models.ModelRequest) (models.ModelOutcome, error) {
	return f(ctx, request)
}

type claimSupportVerifierFunc func(context.Context, models.ClaimSupportRequest) (models.ClaimSupportOutcome, error)

func (f claimSupportVerifierFunc) VerifyClaimSupport(ctx context.Context, request models.ClaimSupportRequest) (models.ClaimSupportOutcome, error) {
	return f(ctx, request)
}

type adapterAction struct{}

func (adapterAction) Definition() models.ActionDefinition {
	return models.ActionDefinition{Name: "current_time", Description: "Return current time.", InputSchema: json.RawMessage(`{"type":"object"}`)}
}
func (adapterAction) ValidateInput(json.RawMessage) error { return nil }
func (adapterAction) Execute(context.Context, ActionRequest) (ActionResult, error) {
	return ActionResult{Status: ActionDomainError, ErrorCode: "clock_unavailable"}, nil
}

type cancellingAdapterAction struct{ adapterAction }

func (cancellingAdapterAction) Execute(context.Context, ActionRequest) (ActionResult, error) {
	return ActionResult{}, context.Canceled
}

type recordingReplayStager struct {
	requests []replay.StageRequest
	err      error
}

func (s *recordingReplayStager) Stage(_ context.Context, request replay.StageRequest) (replay.StagedAttachment, error) {
	s.requests = append(s.requests, request)
	if s.err != nil {
		return replay.StagedAttachment{}, s.err
	}
	return replay.StagedAttachment{AttachmentID: "attachment-" + string(request.Payload.Class)}, nil
}

func stringAttribute(record agentobs.Record, key string) string {
	for _, item := range record.Attributes {
		if item.Key == key && item.Value.Kind == agentobs.ValueString {
			return item.Value.String
		}
	}
	return ""
}

func int64Attribute(record agentobs.Record, key string) int64 {
	for _, item := range record.Attributes {
		if item.Key == key && item.Value.Kind == agentobs.ValueInt64 {
			return item.Value.Int64
		}
	}
	return 0
}

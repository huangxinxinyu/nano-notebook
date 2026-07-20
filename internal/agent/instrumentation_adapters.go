package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/instrumentation"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/semconv"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/replay"
)

type ReplayStager interface {
	Stage(context.Context, replay.StageRequest) (replay.StagedAttachment, error)
}

type ModelTraceOptions struct {
	StartIdentity    string
	RequestIdentity  string
	DecisionIdentity string
	ReplayStager     ReplayStager
}

type ClaimSupportTraceOptions struct {
	StartIdentity   string
	RequestIdentity string
	VerdictIdentity string
	ReplayStager    ReplayStager
}

func InvokeClaimSupportVerifier(ctx context.Context, tracer *agentobs.Tracer, verifier ClaimSupportVerifier, request models.ClaimSupportRequest, optionValues ...ClaimSupportTraceOptions) (models.ClaimSupportOutcome, error) {
	if verifier == nil {
		return models.ClaimSupportOutcome{}, errors.New("nil Claim Support verifier")
	}
	var options ClaimSupportTraceOptions
	if len(optionValues) > 0 {
		options = optionValues[0]
	}
	evidenceCount := 0
	for _, claim := range request.Claims {
		evidenceCount += len(claim.Evidence)
	}
	startAttributes := []agentobs.Attribute{
		agentobs.String(semconv.OperationNameKey, "claim_support"),
		agentobs.String(semconv.ModelNameKey, request.Model),
		agentobs.String(TraceKeyVerifierPromptVersion, request.PromptVersion),
		agentobs.Int64(TraceKeyVerifierClaimCount, int64(len(request.Claims))),
		agentobs.Int64(TraceKeyVerifierEvidenceCount, int64(evidenceCount)),
	}
	if options.ReplayStager != nil {
		payload, err := EncodeClaimSupportRequestReplay(request)
		if err != nil {
			return models.ClaimSupportOutcome{}, &instrumentation.RecordingError{Phase: instrumentation.RecordingStart, Err: err}
		}
		attachmentID, err := stageReplayAttachment(ctx, options.ReplayStager, options.RequestIdentity, payload)
		if err != nil {
			return models.ClaimSupportOutcome{}, &instrumentation.RecordingError{Phase: instrumentation.RecordingStart, Err: err}
		}
		startAttributes = append(startAttributes, agentobs.String(replay.ModelRequestAttachmentKey, attachmentID))
	}
	var verdictAttachmentID string
	return instrumentation.Invoke(ctx, tracer, agentobs.SpanStart{
		IdentityKey: options.StartIdentity, Name: TraceSpanClaimSupport, Attributes: startAttributes,
	}, func(callContext context.Context) (models.ClaimSupportOutcome, error) {
		outcome, err := verifier.VerifyClaimSupport(callContext, request)
		if err == nil && options.ReplayStager != nil {
			payload, payloadErr := EncodeClaimSupportVerdictReplay(outcome)
			if payloadErr != nil {
				return outcome, &instrumentation.RecordingError{Phase: instrumentation.RecordingTerminal, Err: payloadErr}
			}
			verdictAttachmentID, payloadErr = stageReplayAttachment(callContext, options.ReplayStager, options.VerdictIdentity, payload)
			if payloadErr != nil {
				return outcome, &instrumentation.RecordingError{Phase: instrumentation.RecordingTerminal, Err: payloadErr}
			}
		}
		return outcome, err
	}, func(outcome models.ClaimSupportOutcome, callErr error) agentobs.SpanEnd {
		terminal := claimSupportTerminal(outcome, callErr)
		if verdictAttachmentID != "" {
			terminal.Attributes = append(terminal.Attributes, agentobs.String(replay.ModelDecisionAttachmentKey, verdictAttachmentID))
		}
		return terminal
	})
}

func InvokeDecisionModel(ctx context.Context, tracer *agentobs.Tracer, model DecisionModel, request models.ModelRequest, decisionOrdinal int, optionValues ...ModelTraceOptions) (models.ModelOutcome, error) {
	if model == nil {
		return models.ModelOutcome{}, errors.New("nil Decision Model")
	}
	var options ModelTraceOptions
	if len(optionValues) > 0 {
		options = optionValues[0]
	}
	startAttributes := []agentobs.Attribute{
		agentobs.String(semconv.OperationNameKey, "decide"),
		agentobs.String(semconv.ModelNameKey, request.Model),
		agentobs.Int64(semconv.DecisionOrdinalKey, int64(decisionOrdinal)),
		agentobs.Int64(semconv.InputMessageCountKey, int64(len(request.Messages))),
		agentobs.String(semconv.InputHashKey, modelRequestHash(request)),
		agentobs.Bool(semconv.ActionDefinitionsKey, len(request.ActionDefinitions) > 0),
		agentobs.Int64(semconv.ActionDefinitionCountKey, int64(len(request.ActionDefinitions))),
	}
	if options.ReplayStager != nil {
		payload, err := EncodeModelRequestReplay(request)
		if err != nil {
			return models.ModelOutcome{}, &instrumentation.RecordingError{Phase: instrumentation.RecordingStart, Err: err}
		}
		attachmentID, err := stageReplayAttachment(ctx, options.ReplayStager, options.RequestIdentity, payload)
		if err != nil {
			return models.ModelOutcome{}, &instrumentation.RecordingError{Phase: instrumentation.RecordingStart, Err: err}
		}
		startAttributes = append(startAttributes, agentobs.String(replay.ModelRequestAttachmentKey, attachmentID))
	}
	var decisionAttachmentID string
	return instrumentation.Invoke(ctx, tracer, agentobs.SpanStart{IdentityKey: options.StartIdentity, Name: semconv.ModelCall, Attributes: startAttributes}, func(callContext context.Context) (models.ModelOutcome, error) {
		outcome, err := model.Decide(callContext, request)
		if err == nil {
			if metadataErr := outcome.Metadata.Validate(); metadataErr != nil {
				return outcome, &models.ModelError{Kind: models.ErrorInvalidResponse, Err: metadataErr}
			}
			if options.ReplayStager != nil {
				payload, payloadErr := EncodeModelDecisionReplay(outcome.ModelDecision)
				if payloadErr != nil {
					return outcome, &instrumentation.RecordingError{Phase: instrumentation.RecordingTerminal, Err: payloadErr}
				}
				decisionAttachmentID, payloadErr = stageReplayAttachment(callContext, options.ReplayStager, options.DecisionIdentity, payload)
				if payloadErr != nil {
					return outcome, &instrumentation.RecordingError{Phase: instrumentation.RecordingTerminal, Err: payloadErr}
				}
			}
		}
		return outcome, err
	}, func(outcome models.ModelOutcome, callErr error) agentobs.SpanEnd {
		terminal := modelTerminal(outcome.Metadata, callErr)
		if decisionAttachmentID != "" {
			terminal.Attributes = append(terminal.Attributes, agentobs.String(replay.ModelDecisionAttachmentKey, decisionAttachmentID))
		}
		return terminal
	})
}

type ActionTraceOptions struct {
	StartIdentity  string
	LinkIdentity   string
	RetryTarget    *agentobs.SpanContext
	InputIdentity  string
	ResultIdentity string
	ReplayStager   ReplayStager
}

func InvokeAgentAction(ctx context.Context, tracer *agentobs.Tracer, action Action, logicalActionID string, request ActionRequest, optionValues ...ActionTraceOptions) (ActionResult, error) {
	if action == nil {
		return ActionResult{}, errors.New("nil Agent Action")
	}
	name := action.Definition().Name
	var options ActionTraceOptions
	if len(optionValues) > 0 {
		options = optionValues[0]
	}
	startAttributes := []agentobs.Attribute{
		agentobs.String(semconv.ActionNameKey, name),
		agentobs.String(semconv.ActionLogicalIDKey, logicalActionID),
	}
	if name == "search_evidence" {
		if input, err := decodeSearchEvidenceInput(request.Input); err == nil {
			startAttributes = append(startAttributes, agentobs.String(TraceKeySearchPurpose, input.Purpose))
		}
	}
	if options.ReplayStager != nil {
		payload, err := EncodeActionInputReplay(name, logicalActionID, request)
		if err != nil {
			return ActionResult{}, &instrumentation.RecordingError{Phase: instrumentation.RecordingStart, Err: err}
		}
		attachmentID, err := stageReplayAttachment(ctx, options.ReplayStager, options.InputIdentity, payload)
		if err != nil {
			return ActionResult{}, &instrumentation.RecordingError{Phase: instrumentation.RecordingStart, Err: err}
		}
		startAttributes = append(startAttributes, agentobs.String(replay.ActionInputAttachmentKey, attachmentID))
	}
	var resultAttachmentID string
	return instrumentation.Invoke(ctx, tracer, agentobs.SpanStart{
		IdentityKey: options.StartIdentity,
		Name:        semconv.AgentAction,
		Attributes:  startAttributes,
	}, func(callContext context.Context) (ActionResult, error) {
		if options.RetryTarget != nil {
			identity := options.LinkIdentity
			if identity == "" {
				identity = options.StartIdentity + "/retries"
			}
			if err := tracer.Link(callContext, agentobs.Link{
				IdentityKey: identity, Name: semconv.LinkRetries, Target: *options.RetryTarget,
			}); err != nil {
				return ActionResult{}, &instrumentation.RecordingError{Phase: instrumentation.RecordingLink, Err: err}
			}
		}
		result, err := action.Execute(callContext, request)
		if err == nil && options.ReplayStager != nil {
			payload, payloadErr := EncodeActionResultReplay(name, logicalActionID, result)
			if payloadErr != nil {
				return result, &instrumentation.RecordingError{Phase: instrumentation.RecordingTerminal, Err: payloadErr}
			}
			resultAttachmentID, payloadErr = stageReplayAttachment(callContext, options.ReplayStager, options.ResultIdentity, payload)
			if payloadErr != nil {
				return result, &instrumentation.RecordingError{Phase: instrumentation.RecordingTerminal, Err: payloadErr}
			}
		}
		return result, err
	}, func(result ActionResult, callErr error) agentobs.SpanEnd {
		status := agentobs.StatusOK
		attributes := []agentobs.Attribute{agentobs.String(semconv.ActionNameKey, name)}
		if callErr != nil {
			status = agentobs.StatusError
			if errors.Is(callErr, context.Canceled) {
				status = agentobs.StatusCancelled
			}
			attributes = append(attributes, agentobs.String(semconv.ErrorKindKey, "execution_error"))
		} else {
			attributes = append(attributes, agentobs.String(semconv.OperationStatusKey, string(result.Status)))
			attributes = append(attributes, result.traceAttributes...)
			if result.ErrorCode != "" {
				attributes = append(attributes, agentobs.String(semconv.ErrorKindKey, result.ErrorCode))
			}
		}
		if resultAttachmentID != "" {
			attributes = append(attributes, agentobs.String(replay.ActionResultAttachmentKey, resultAttachmentID))
		}
		return agentobs.SpanEnd{Name: semconv.AgentAction, Status: status, Attributes: attributes}
	})
}

func stageReplayAttachment(ctx context.Context, stager ReplayStager, identityKey string, payload replay.PlainPayload) (string, error) {
	span, ok := agentobs.SpanContextFromContext(ctx)
	if !ok || strings.TrimSpace(identityKey) == "" {
		return "", errors.New("Replay staging requires Trace and logical identity")
	}
	attachment, err := stager.Stage(ctx, replay.StageRequest{
		TraceID: span.TraceID, IdentityKey: identityKey, Payload: payload,
	})
	if err != nil {
		return "", err
	}
	if attachment.AttachmentID == "" {
		return "", errors.New("Replay Stager returned an empty Attachment identity")
	}
	return attachment.AttachmentID, nil
}

func modelTerminal(metadata models.ModelCallMetadata, callErr error) agentobs.SpanEnd {
	status := agentobs.StatusOK
	attributes := []agentobs.Attribute{agentobs.Bool(semconv.CostKnownKey, metadata.Cost.Known)}
	if metadata.ResultKind != "" {
		attributes = append(attributes, agentobs.String(semconv.ModelResultKindKey, string(metadata.ResultKind)))
	}
	if metadata.FinishReason != "" {
		attributes = append(attributes, agentobs.String(semconv.ModelFinishReasonKey, metadata.FinishReason))
	}
	if metadata.SelectedProvider != "" {
		attributes = append(attributes, agentobs.String(semconv.ModelProviderKey, metadata.SelectedProvider))
	}
	if metadata.SelectedModel != "" {
		attributes = append(attributes, agentobs.String(semconv.ModelNameKey, metadata.SelectedModel))
	}
	appendInt := func(key string, value *int64) {
		if value != nil {
			attributes = append(attributes, agentobs.Int64(key, *value))
		}
	}
	appendInt(semconv.TokenInputKey, metadata.InputTokens)
	appendInt(semconv.TokenOutputKey, metadata.OutputTokens)
	appendInt(semconv.TokenTotalKey, metadata.TotalTokens)
	appendInt(semconv.TokenCachedKey, metadata.CachedTokens)
	appendInt(semconv.TokenReasoningKey, metadata.ReasoningTokens)
	appendInt(semconv.GatewayRetryCountKey, metadata.GatewayRetries)
	appendInt(semconv.GatewayFallbackCountKey, metadata.GatewayFallbacks)
	if metadata.Latency > 0 {
		attributes = append(attributes, agentobs.Int64(semconv.DurationMillisecondsKey, metadata.Latency.Milliseconds()))
	}
	if metadata.Cost.Known && metadata.Cost.Amount != nil {
		attributes = append(attributes,
			agentobs.Float64(semconv.CostAmountKey, *metadata.Cost.Amount),
			agentobs.String(semconv.CostCurrencyKey, metadata.Cost.Currency),
			agentobs.String(semconv.CostSourceKey, metadata.Cost.Source),
		)
	}
	if callErr != nil {
		status = agentobs.StatusError
		if errors.Is(callErr, context.Canceled) {
			status = agentobs.StatusCancelled
		}
		kind := string(models.ErrorUnavailable)
		var modelErr *models.ModelError
		if errors.As(callErr, &modelErr) {
			kind = string(modelErr.Kind)
		}
		attributes = append(attributes, agentobs.String(semconv.ErrorKindKey, kind))
	}
	return agentobs.SpanEnd{Name: semconv.ModelCall, Status: status, Attributes: attributes}
}

func claimSupportTerminal(outcome models.ClaimSupportOutcome, callErr error) agentobs.SpanEnd {
	status := agentobs.StatusOK
	metadata := outcome.Metadata
	attributes := []agentobs.Attribute{agentobs.Bool(semconv.CostKnownKey, metadata.Cost.Known)}
	if metadata.Provider != "" {
		attributes = append(attributes, agentobs.String(semconv.ModelProviderKey, metadata.Provider))
	}
	if metadata.Model != "" {
		attributes = append(attributes, agentobs.String(semconv.ModelNameKey, metadata.Model))
	}
	if metadata.InputTokens != nil {
		attributes = append(attributes, agentobs.Int64(semconv.TokenInputKey, *metadata.InputTokens))
	}
	if metadata.TotalTokens != nil {
		attributes = append(attributes, agentobs.Int64(semconv.TokenTotalKey, *metadata.TotalTokens))
	}
	if metadata.Latency > 0 {
		attributes = append(attributes, agentobs.Int64(semconv.DurationMillisecondsKey, metadata.Latency.Milliseconds()))
	}
	if metadata.Cost.Known && metadata.Cost.Amount != nil {
		attributes = append(attributes,
			agentobs.Float64(semconv.CostAmountKey, *metadata.Cost.Amount),
			agentobs.String(semconv.CostCurrencyKey, metadata.Cost.Currency),
			agentobs.String(semconv.CostSourceKey, metadata.Cost.Source),
		)
	}
	if callErr == nil {
		supported := int64(0)
		for _, verdict := range outcome.Verdicts {
			if verdict.Supported {
				supported++
			}
		}
		attributes = append(attributes,
			agentobs.Int64(TraceKeyVerifierSupportedCount, supported),
			agentobs.Int64(TraceKeyVerifierUnsupportedCount, int64(len(outcome.Verdicts))-supported),
		)
	} else {
		status = agentobs.StatusError
		if errors.Is(callErr, context.Canceled) {
			status = agentobs.StatusCancelled
		}
		kind := string(models.ErrorUnavailable)
		var modelErr *models.ModelError
		if errors.As(callErr, &modelErr) {
			kind = string(modelErr.Kind)
		}
		attributes = append(attributes, agentobs.String(semconv.ErrorKindKey, kind))
	}
	return agentobs.SpanEnd{Name: TraceSpanClaimSupport, Status: status, Attributes: attributes}
}

func modelRequestHash(request models.ModelRequest) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%s\x00", request.Model)
	for _, message := range request.Messages {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00", message.Role, message.Content)
		for _, call := range message.ActionCalls {
			_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\x00", call.ID, call.Name, canonicalHashInput(call.Input))
		}
	}
	for _, definition := range request.ActionDefinitions {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00", definition.Name, canonicalHashInput(definition.InputSchema))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func canonicalHashInput(raw json.RawMessage) string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "invalid"
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "invalid"
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

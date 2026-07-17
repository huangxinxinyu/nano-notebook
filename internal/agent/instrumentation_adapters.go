package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/instrumentation"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/semconv"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

func InvokeDecisionModel(ctx context.Context, tracer *agentobs.Tracer, model DecisionModel, request models.ModelRequest, decisionOrdinal int) (models.ModelOutcome, error) {
	if model == nil {
		return models.ModelOutcome{}, errors.New("nil Decision Model")
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
	return instrumentation.Invoke(ctx, tracer, agentobs.SpanStart{Name: semconv.ModelCall, Attributes: startAttributes}, func(callContext context.Context) (models.ModelOutcome, error) {
		outcome, err := model.Decide(callContext, request)
		if err == nil {
			if metadataErr := outcome.Metadata.Validate(); metadataErr != nil {
				return outcome, &models.ModelError{Kind: models.ErrorInvalidResponse, Err: metadataErr}
			}
		}
		return outcome, err
	}, func(outcome models.ModelOutcome, callErr error) agentobs.SpanEnd {
		return modelTerminal(outcome.Metadata, callErr)
	})
}

type ActionTraceOptions struct {
	StartIdentity string
	LinkIdentity  string
	RetryTarget   *agentobs.SpanContext
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
	return instrumentation.Invoke(ctx, tracer, agentobs.SpanStart{
		IdentityKey: options.StartIdentity,
		Name:        semconv.AgentAction,
		Attributes: []agentobs.Attribute{
			agentobs.String(semconv.ActionNameKey, name),
			agentobs.String(semconv.ActionLogicalIDKey, logicalActionID),
		},
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
		return action.Execute(callContext, request)
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
			if result.ErrorCode != "" {
				attributes = append(attributes, agentobs.String(semconv.ErrorKindKey, result.ErrorCode))
			}
		}
		return agentobs.SpanEnd{Name: semconv.AgentAction, Status: status, Attributes: attributes}
	})
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

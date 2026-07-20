package collector

import (
	"errors"
	"fmt"
	"sort"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/semconv"
	"github.com/huangxinxinyu/nano-notebook/internal/replay"
)

type TraceProjection struct {
	Summary TraceSummary      `json:"summary"`
	Spans   []SpanProjection  `json:"spans"`
	Events  []EventProjection `json:"events"`
	Links   []LinkProjection  `json:"links"`
}

type TraceSummary struct {
	TraceID              agentobs.TraceID `json:"trace_id"`
	WorkloadKind         WorkloadKind     `json:"workload_kind"`
	WorkloadID           string           `json:"workload_id"`
	RunID                string           `json:"run_id"`
	ChatID               string           `json:"chat_id"`
	NotebookID           string           `json:"notebook_id"`
	RootSpanID           agentobs.SpanID  `json:"root_span_id"`
	AgentName            string           `json:"agent_name"`
	StartedAtUnixNano    int64            `json:"started_at_unix_nano"`
	LastObservedUnixNano int64            `json:"last_observed_unix_nano"`
	EndedAtUnixNano      *int64           `json:"ended_at_unix_nano"`
	DurationNanoseconds  *int64           `json:"duration_nanoseconds"`
	Status               agentobs.Status  `json:"status"`
	Active               bool             `json:"active"`
	Models               []string         `json:"models"`
	InputTokens          *int64           `json:"input_tokens"`
	OutputTokens         *int64           `json:"output_tokens"`
	TotalTokens          *int64           `json:"total_tokens"`
	Cost                 CostProjection   `json:"cost"`
	AttemptCount         int              `json:"attempt_count"`
}

type CostProjection struct {
	Known    bool     `json:"known"`
	Amount   *float64 `json:"amount"`
	Currency string   `json:"currency"`
	Source   string   `json:"source"`
}

type SpanProjection struct {
	TraceID             agentobs.TraceID            `json:"trace_id"`
	SpanID              agentobs.SpanID             `json:"span_id"`
	ParentSpanID        agentobs.SpanID             `json:"parent_span_id"`
	Name                string                      `json:"name"`
	StartSequence       int                         `json:"start_sequence"`
	EndSequence         *int                        `json:"end_sequence"`
	StartedAtUnixNano   int64                       `json:"started_at_unix_nano"`
	EndedAtUnixNano     *int64                      `json:"ended_at_unix_nano"`
	DurationNanoseconds *int64                      `json:"duration_nanoseconds"`
	Status              agentobs.Status             `json:"status"`
	StartAttributes     []agentobs.Attribute        `json:"start_attributes"`
	EndAttributes       []agentobs.Attribute        `json:"end_attributes"`
	Replay              []ReplayReferenceProjection `json:"replay"`
	Model               *ModelAnalysisProjection    `json:"model"`
}

type ReplayReferenceProjection struct {
	AttachmentID   string       `json:"attachment_id"`
	Class          replay.Class `json:"class"`
	RecordSequence int          `json:"record_sequence"`
}

type ModelAnalysisProjection struct {
	RequestedModel      string         `json:"requested_model"`
	SelectedModel       string         `json:"selected_model"`
	Provider            string         `json:"provider"`
	InputTokens         *int64         `json:"input_tokens"`
	OutputTokens        *int64         `json:"output_tokens"`
	TotalTokens         *int64         `json:"total_tokens"`
	CachedTokens        *int64         `json:"cached_tokens"`
	ReasoningTokens     *int64         `json:"reasoning_tokens"`
	GatewayRetries      *int64         `json:"gateway_retries"`
	GatewayFallbacks    *int64         `json:"gateway_fallbacks"`
	DurationNanoseconds *int64         `json:"duration_nanoseconds"`
	Cost                CostProjection `json:"cost"`
}

type EventProjection struct {
	TraceID            agentobs.TraceID     `json:"trace_id"`
	Sequence           int                  `json:"sequence"`
	SpanID             agentobs.SpanID      `json:"span_id"`
	Name               string               `json:"name"`
	OccurredAtUnixNano int64                `json:"occurred_at_unix_nano"`
	Attributes         []agentobs.Attribute `json:"attributes"`
}

type LinkProjection struct {
	TraceID            agentobs.TraceID     `json:"trace_id"`
	Sequence           int                  `json:"sequence"`
	SpanID             agentobs.SpanID      `json:"span_id"`
	Name               string               `json:"name"`
	TargetTraceID      agentobs.TraceID     `json:"target_trace_id"`
	TargetSpanID       agentobs.SpanID      `json:"target_span_id"`
	OccurredAtUnixNano int64                `json:"occurred_at_unix_nano"`
	Attributes         []agentobs.Attribute `json:"attributes"`
}

func BuildTraceProjection(stored StoredTrace) (TraceProjection, error) {
	trace, err := CanonicalTraceDescriptor(stored.Trace)
	if err != nil {
		return TraceProjection{}, err
	}
	stored.Trace = trace
	if stored.CommittedThrough < 1 || len(stored.Records) != stored.CommittedThrough {
		return TraceProjection{}, errors.New("Collector projection requires a complete committed prefix")
	}
	projection := TraceProjection{Summary: TraceSummary{
		TraceID: stored.Trace.TraceID, WorkloadKind: stored.Trace.WorkloadKind, WorkloadID: stored.Trace.WorkloadID,
		RunID: stored.Trace.RunID, ChatID: stored.Trace.ChatID,
		NotebookID: stored.Trace.NotebookID, RootSpanID: stored.Trace.RootSpanID,
		AgentName: stored.Trace.AgentName, Active: true, Models: []string{},
	}}
	spanIndex := make(map[agentobs.SpanID]int)
	modelNames := make(map[string]struct{})
	for index, envelope := range stored.Records {
		sequence := index + 1
		if envelope.Sequence != sequence || envelope.Record.TraceID != stored.Trace.TraceID {
			return TraceProjection{}, errors.New("Collector projection record sequence changed")
		}
		if err := envelope.Record.Validate(); err != nil {
			return TraceProjection{}, fmt.Errorf("validate Collector projection record %d: %w", sequence, err)
		}
		observed := envelope.Record.OccurredAt.UnixNano()
		if sequence == 1 {
			projection.Summary.StartedAtUnixNano = observed
		}
		if sequence == 1 || observed > projection.Summary.LastObservedUnixNano {
			projection.Summary.LastObservedUnixNano = observed
		}
		switch envelope.Record.Kind {
		case agentobs.RecordSpanStarted:
			if _, duplicate := spanIndex[envelope.Record.SpanID]; duplicate {
				return TraceProjection{}, errors.New("Collector projection Span start is duplicated")
			}
			span := SpanProjection{
				TraceID: stored.Trace.TraceID, SpanID: envelope.Record.SpanID, ParentSpanID: envelope.Record.ParentSpanID,
				Name: envelope.Record.Name, StartSequence: sequence, StartedAtUnixNano: observed,
				StartAttributes: cloneAttributes(envelope.Record.Attributes),
			}
			references, err := projectionReplayReferences(sequence, envelope.Record.Attributes)
			if err != nil {
				return TraceProjection{}, fmt.Errorf("project Collector Replay references for record %d: %w", sequence, err)
			}
			span.Replay = references
			spanIndex[span.SpanID] = len(projection.Spans)
			projection.Spans = append(projection.Spans, span)
			if span.SpanID == stored.Trace.RootSpanID {
				projection.Summary.StartedAtUnixNano = observed
			}
			if span.Name == "nano.job.attempt" {
				projection.Summary.AttemptCount++
			}
			if span.Name == semconv.ModelCall {
				if model := stringAttributeValue(span.StartAttributes, semconv.ModelNameKey); model != "" {
					modelNames[model] = struct{}{}
				}
			}
		case agentobs.RecordSpanEnded:
			position, found := spanIndex[envelope.Record.SpanID]
			if !found {
				return TraceProjection{}, errors.New("Collector projection Span terminal has no start")
			}
			span := &projection.Spans[position]
			endSequence := sequence
			ended := observed
			duration := ended - span.StartedAtUnixNano
			if duration < 0 {
				return TraceProjection{}, errors.New("Collector projection Span duration is negative")
			}
			span.EndSequence, span.EndedAtUnixNano, span.DurationNanoseconds = &endSequence, &ended, &duration
			span.Status = envelope.Record.Status
			span.EndAttributes = cloneAttributes(envelope.Record.Attributes)
			references, err := projectionReplayReferences(sequence, envelope.Record.Attributes)
			if err != nil {
				return TraceProjection{}, fmt.Errorf("project Collector Replay references for record %d: %w", sequence, err)
			}
			span.Replay = append(span.Replay, references...)
			if span.Name == semconv.ModelCall {
				span.Model = projectModelAnalysis(*span)
				for _, model := range []string{span.Model.RequestedModel, span.Model.SelectedModel} {
					if model != "" {
						modelNames[model] = struct{}{}
					}
				}
			}
			if span.SpanID == stored.Trace.RootSpanID {
				projection.Summary.Active = false
				projection.Summary.Status = span.Status
				projection.Summary.EndedAtUnixNano = &ended
				rootDuration := ended - span.StartedAtUnixNano
				projection.Summary.DurationNanoseconds = &rootDuration
			}
		case agentobs.RecordEvent:
			projection.Events = append(projection.Events, EventProjection{
				TraceID: stored.Trace.TraceID, Sequence: sequence, SpanID: envelope.Record.SpanID,
				Name: envelope.Record.Name, OccurredAtUnixNano: observed,
				Attributes: cloneAttributes(envelope.Record.Attributes),
			})
		case agentobs.RecordLink:
			projection.Links = append(projection.Links, LinkProjection{
				TraceID: stored.Trace.TraceID, Sequence: sequence, SpanID: envelope.Record.SpanID,
				Name: envelope.Record.Name, TargetTraceID: envelope.Record.TargetTraceID,
				TargetSpanID: envelope.Record.TargetSpanID, OccurredAtUnixNano: observed,
				Attributes: cloneAttributes(envelope.Record.Attributes),
			})
		}
	}
	for model := range modelNames {
		projection.Summary.Models = append(projection.Summary.Models, model)
	}
	sort.Strings(projection.Summary.Models)
	projectSummaryAnalysis(&projection)
	return projection, nil
}

func projectionReplayReferences(sequence int, attributes []agentobs.Attribute) ([]ReplayReferenceProjection, error) {
	references, err := replay.AttachmentReferences(attributes)
	if err != nil {
		return nil, err
	}
	result := make([]ReplayReferenceProjection, 0, len(references))
	for _, reference := range references {
		result = append(result, ReplayReferenceProjection{
			AttachmentID: reference.AttachmentID, Class: reference.Class, RecordSequence: sequence,
		})
	}
	return result, nil
}

func projectModelAnalysis(span SpanProjection) *ModelAnalysisProjection {
	analysis := &ModelAnalysisProjection{
		RequestedModel:      stringAttributeValue(span.StartAttributes, semconv.ModelNameKey),
		SelectedModel:       stringAttributeValue(span.EndAttributes, semconv.ModelNameKey),
		Provider:            stringAttributeValue(span.EndAttributes, semconv.ModelProviderKey),
		InputTokens:         intAttributeValue(span.EndAttributes, semconv.TokenInputKey),
		OutputTokens:        intAttributeValue(span.EndAttributes, semconv.TokenOutputKey),
		TotalTokens:         intAttributeValue(span.EndAttributes, semconv.TokenTotalKey),
		CachedTokens:        intAttributeValue(span.EndAttributes, semconv.TokenCachedKey),
		ReasoningTokens:     intAttributeValue(span.EndAttributes, semconv.TokenReasoningKey),
		GatewayRetries:      intAttributeValue(span.EndAttributes, semconv.GatewayRetryCountKey),
		GatewayFallbacks:    intAttributeValue(span.EndAttributes, semconv.GatewayFallbackCountKey),
		DurationNanoseconds: span.DurationNanoseconds,
	}
	known, found := boolAttributeValue(span.EndAttributes, semconv.CostKnownKey)
	amount := floatAttributeValue(span.EndAttributes, semconv.CostAmountKey)
	if found && known && amount != nil {
		analysis.Cost = CostProjection{
			Known: true, Amount: amount,
			Currency: stringAttributeValue(span.EndAttributes, semconv.CostCurrencyKey),
			Source:   stringAttributeValue(span.EndAttributes, semconv.CostSourceKey),
		}
	}
	return analysis
}

func projectSummaryAnalysis(projection *TraceProjection) {
	models := make([]*ModelAnalysisProjection, 0)
	for index := range projection.Spans {
		if projection.Spans[index].Model != nil {
			models = append(models, projection.Spans[index].Model)
		}
	}
	if len(models) == 0 {
		return
	}
	projection.Summary.InputTokens = sumKnownInt(models, func(model *ModelAnalysisProjection) *int64 { return model.InputTokens })
	projection.Summary.OutputTokens = sumKnownInt(models, func(model *ModelAnalysisProjection) *int64 { return model.OutputTokens })
	projection.Summary.TotalTokens = sumKnownInt(models, func(model *ModelAnalysisProjection) *int64 { return model.TotalTokens })
	var amount float64
	currency, source := "", ""
	for index, model := range models {
		if !model.Cost.Known || model.Cost.Amount == nil {
			return
		}
		if index == 0 {
			currency, source = model.Cost.Currency, model.Cost.Source
		} else if currency != model.Cost.Currency {
			return
		}
		amount += *model.Cost.Amount
	}
	projection.Summary.Cost = CostProjection{Known: true, Amount: &amount, Currency: currency, Source: source}
}

func sumKnownInt(models []*ModelAnalysisProjection, get func(*ModelAnalysisProjection) *int64) *int64 {
	var total int64
	for _, model := range models {
		value := get(model)
		if value == nil {
			return nil
		}
		total += *value
	}
	return &total
}

func stringAttributeValue(attributes []agentobs.Attribute, key string) string {
	for _, attribute := range attributes {
		if attribute.Key == key && attribute.Value.Kind == agentobs.ValueString {
			return attribute.Value.String
		}
	}
	return ""
}

func intAttributeValue(attributes []agentobs.Attribute, key string) *int64 {
	for _, attribute := range attributes {
		if attribute.Key == key && attribute.Value.Kind == agentobs.ValueInt64 {
			value := attribute.Value.Int64
			return &value
		}
	}
	return nil
}

func floatAttributeValue(attributes []agentobs.Attribute, key string) *float64 {
	for _, attribute := range attributes {
		if attribute.Key == key && attribute.Value.Kind == agentobs.ValueFloat64 {
			value := attribute.Value.Float64
			return &value
		}
	}
	return nil
}

func boolAttributeValue(attributes []agentobs.Attribute, key string) (bool, bool) {
	for _, attribute := range attributes {
		if attribute.Key == key && attribute.Value.Kind == agentobs.ValueBool {
			return attribute.Value.Bool, true
		}
	}
	return false, false
}

func cloneAttributes(attributes []agentobs.Attribute) []agentobs.Attribute {
	return append([]agentobs.Attribute(nil), attributes...)
}

package otelbridge

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

var ErrShutdown = errors.New("OpenTelemetry Agent Trace bridge is shut down")

type Exporter struct {
	mu       sync.Mutex
	tracer   trace.Tracer
	spans    map[spanKey]trace.Span
	shutdown bool
}

type spanKey struct {
	traceID agentobs.TraceID
	spanID  agentobs.SpanID
}

var _ agentobs.Exporter = (*Exporter)(nil)

func New(tracer trace.Tracer) (*Exporter, error) {
	if tracer == nil {
		return nil, errors.New("OpenTelemetry bridge requires a Tracer")
	}
	return &Exporter{tracer: tracer, spans: make(map[spanKey]trace.Span)}, nil
}

func (e *Exporter) Export(ctx context.Context, record agentobs.Record) error {
	if e == nil {
		return errors.New("nil OpenTelemetry Agent Trace bridge")
	}
	if err := record.Validate(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.shutdown {
		return ErrShutdown
	}
	key := spanKey{traceID: record.TraceID, spanID: record.SpanID}
	switch record.Kind {
	case agentobs.RecordSpanStarted:
		if _, exists := e.spans[key]; exists {
			return fmt.Errorf("OpenTelemetry bridge duplicate Span start %s", record.SpanID)
		}
		parentContext := context.Background()
		if record.ParentSpanID != "" {
			parent, exists := e.spans[spanKey{traceID: record.TraceID, spanID: record.ParentSpanID}]
			if exists {
				parentContext = trace.ContextWithSpan(parentContext, parent)
			}
		}
		_, span := e.tracer.Start(parentContext, record.Name,
			trace.WithTimestamp(record.OccurredAt), trace.WithAttributes(otelAttributes(record)...))
		e.spans[key] = span
	case agentobs.RecordSpanEnded:
		span, exists := e.spans[key]
		if !exists {
			return fmt.Errorf("OpenTelemetry bridge unresolved terminal %s", record.SpanID)
		}
		span.SetAttributes(otelAttributes(record)...)
		switch record.Status {
		case agentobs.StatusOK:
			span.SetStatus(codes.Ok, "")
		case agentobs.StatusCancelled:
			span.SetStatus(codes.Error, "cancelled")
		default:
			span.SetStatus(codes.Error, "error")
		}
		span.End(trace.WithTimestamp(record.OccurredAt))
		delete(e.spans, key)
	case agentobs.RecordEvent:
		span, exists := e.spans[key]
		if !exists {
			return fmt.Errorf("OpenTelemetry bridge unresolved Event source %s", record.SpanID)
		}
		span.AddEvent(record.Name, trace.WithTimestamp(record.OccurredAt), trace.WithAttributes(otelAttributes(record)...))
	case agentobs.RecordLink:
		span, exists := e.spans[key]
		if !exists {
			return fmt.Errorf("OpenTelemetry bridge unresolved Link source %s", record.SpanID)
		}
		attributes := otelAttributes(record)
		attributes = append(attributes,
			attribute.String("agent.link.target_trace_id", string(record.TargetTraceID)),
			attribute.String("agent.link.target_span_id", string(record.TargetSpanID)),
		)
		span.AddEvent("agent.link."+record.Name, trace.WithTimestamp(record.OccurredAt), trace.WithAttributes(attributes...))
	}
	return nil
}

func (e *Exporter) ForceFlush(context.Context) error {
	if e == nil {
		return errors.New("nil OpenTelemetry Agent Trace bridge")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.shutdown {
		return ErrShutdown
	}
	return nil
}

func (e *Exporter) Shutdown(context.Context) error {
	if e == nil {
		return errors.New("nil OpenTelemetry Agent Trace bridge")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.shutdown = true
	e.spans = make(map[spanKey]trace.Span)
	return nil
}

func otelAttributes(record agentobs.Record) []attribute.KeyValue {
	attributes := make([]attribute.KeyValue, 0, len(record.Attributes)+4)
	attributes = append(attributes,
		attribute.String("agent.durable.trace_id", string(record.TraceID)),
		attribute.String("agent.durable.span_id", string(record.SpanID)),
		attribute.String("agent.record.identity", record.IdentityKey),
		attribute.Int("agent.schema.version", record.SchemaVersion),
	)
	for _, item := range record.Attributes {
		switch item.Value.Kind {
		case agentobs.ValueString:
			attributes = append(attributes, attribute.String(item.Key, item.Value.String))
		case agentobs.ValueInt64:
			attributes = append(attributes, attribute.Int64(item.Key, item.Value.Int64))
		case agentobs.ValueFloat64:
			attributes = append(attributes, attribute.Float64(item.Key, item.Value.Float64))
		case agentobs.ValueBool:
			attributes = append(attributes, attribute.Bool(item.Key, item.Value.Bool))
		}
	}
	return attributes
}

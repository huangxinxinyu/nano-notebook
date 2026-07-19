package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/jackc/pgx/v5"
)

type RunTraceRecorder struct {
	tx       pgx.Tx
	runID    string
	traceID  agentobs.TraceID
	rootSpan agentobs.SpanID
	direct   *TraceTransaction
}

var _ agentobs.Recorder = (*RunTraceRecorder)(nil)

func NewRunTraceRecorder(ctx context.Context, tx pgx.Tx, runID string) (*RunTraceRecorder, error) {
	if tx == nil || runID == "" {
		return nil, errors.New("Run Trace Recorder dependencies are incomplete")
	}
	var descriptor collector.TraceDescriptor
	if err := tx.QueryRow(ctx, `
		select trace_id, run_id, chat_id, notebook_id, root_span_id, agent_name,
			schema_version, semantic_convention_version
		from agent_trace_refs where run_id = $1`, runID).Scan(
		&descriptor.TraceID, &descriptor.RunID, &descriptor.ChatID, &descriptor.NotebookID,
		&descriptor.RootSpanID, &descriptor.AgentName, &descriptor.SchemaVersion,
		&descriptor.SemanticConventionVersion,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTraceNotFound
		}
		return nil, err
	}
	return newDirectRunTraceRecorder(ctx, tx, descriptor)
}

func NewOwnedRunTraceRecorder(ctx context.Context, tx pgx.Tx, runID string) (*RunTraceRecorder, error) {
	if tx == nil || runID == "" {
		return nil, errors.New("owned Run Trace Recorder dependencies are incomplete")
	}
	var descriptor collector.TraceDescriptor
	if err := tx.QueryRow(ctx, `
		select trace_id, run_id, chat_id, notebook_id, root_span_id, agent_name,
			schema_version, semantic_convention_version
		from nano_owned_run_trace_descriptor($1)`, runID).Scan(
		&descriptor.TraceID, &descriptor.RunID, &descriptor.ChatID, &descriptor.NotebookID,
		&descriptor.RootSpanID, &descriptor.AgentName, &descriptor.SchemaVersion,
		&descriptor.SemanticConventionVersion,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTraceNotFound
		}
		return nil, err
	}
	return newDirectRunTraceRecorder(ctx, tx, descriptor)
}

func newDirectRunTraceRecorder(ctx context.Context, tx pgx.Tx, descriptor collector.TraceDescriptor) (*RunTraceRecorder, error) {
	scope, ok := TraceScopeFromContext(ctx)
	if !ok {
		return nil, errors.New("Run Trace Recorder requires direct Trace delivery scope")
	}
	direct, err := scope.Transaction(descriptor)
	if err != nil {
		return nil, err
	}
	return &RunTraceRecorder{
		tx: tx, runID: descriptor.RunID, traceID: descriptor.TraceID,
		rootSpan: descriptor.RootSpanID, direct: direct,
	}, nil
}

func (r *RunTraceRecorder) RootSpanContext() agentobs.SpanContext {
	if r == nil {
		return agentobs.SpanContext{}
	}
	return agentobs.SpanContext{TraceID: r.traceID, SpanID: r.rootSpan}
}

func (r *RunTraceRecorder) Descriptor() collector.TraceDescriptor {
	if r == nil || r.direct == nil {
		return collector.TraceDescriptor{}
	}
	return r.direct.Descriptor()
}

func (r *RunTraceRecorder) SpanContextByIdentity(_ context.Context, identityKey string) (agentobs.SpanContext, error) {
	if r == nil {
		return agentobs.SpanContext{}, errors.New("nil Run Trace Recorder")
	}
	spanID, err := DeterministicSpanID(r.traceID, identityKey)
	if err != nil {
		return agentobs.SpanContext{}, err
	}
	return agentobs.SpanContext{TraceID: r.traceID, SpanID: spanID}, nil
}

func (r *RunTraceRecorder) Record(ctx context.Context, record agentobs.Record) error {
	if r == nil || r.direct == nil {
		return errors.New("nil Run Trace Recorder")
	}
	record = normalizeTraceRecord(record)
	if err := record.Validate(); err != nil {
		return err
	}
	if record.TraceID != r.traceID {
		return fmt.Errorf("%w: record changed Run Trace envelope", agentobs.ErrLifecycle)
	}
	return r.direct.Record(ctx, record)
}

func (r *RunTraceRecorder) SpanIDForIdentity(traceID agentobs.TraceID, identityKey string) agentobs.SpanID {
	spanID, _ := DeterministicSpanID(traceID, identityKey)
	return spanID
}

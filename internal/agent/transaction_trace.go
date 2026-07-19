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
	tx            pgx.Tx
	runID         string
	traceID       agentobs.TraceID
	rootSpanID    agentobs.SpanID
	schemaVersion int
	sequence      int
	ownedLookup   bool
	direct        *TraceTransaction
}

var _ agentobs.Recorder = (*RunTraceRecorder)(nil)

func NewRunTraceRecorder(ctx context.Context, tx pgx.Tx, runID string) (*RunTraceRecorder, error) {
	if tx == nil || runID == "" {
		return nil, errors.New("Run Trace Recorder dependencies are incomplete")
	}
	var recorder RunTraceRecorder
	recorder.tx = tx
	recorder.runID = runID
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
	recorder.traceID, recorder.rootSpanID, recorder.schemaVersion = descriptor.TraceID, descriptor.RootSpanID, descriptor.SchemaVersion
	if scope, ok := TraceScopeFromContext(ctx); ok {
		transaction, err := scope.Transaction(descriptor)
		if err != nil {
			return nil, err
		}
		recorder.direct = transaction
		return &recorder, nil
	}
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "agent_trace:"+string(recorder.traceID)); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, `
		select next_sequence - 1
		from agent_trace_refs where trace_id = $1
		for update`, recorder.traceID).Scan(&recorder.sequence); err != nil {
		return nil, err
	}
	if recorder.sequence < 1 {
		return nil, fmt.Errorf("%w: Run Trace has no root record", agentobs.ErrLifecycle)
	}
	return &recorder, nil
}

func NewOwnedRunTraceRecorder(ctx context.Context, tx pgx.Tx, runID string) (*RunTraceRecorder, error) {
	if tx == nil || runID == "" {
		return nil, errors.New("owned Run Trace Recorder dependencies are incomplete")
	}
	recorder := &RunTraceRecorder{tx: tx, runID: runID, ownedLookup: true}
	if err := tx.QueryRow(ctx, `
		select trace_id, root_span_id, schema_version, sequence_no
		from nano_owned_run_trace_state($1)`, runID).Scan(
		&recorder.traceID, &recorder.rootSpanID, &recorder.schemaVersion, &recorder.sequence,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTraceNotFound
		}
		return nil, err
	}
	if scope, ok := TraceScopeFromContext(ctx); ok {
		var descriptor collector.TraceDescriptor
		descriptor.TraceID, descriptor.RunID = recorder.traceID, runID
		descriptor.RootSpanID, descriptor.SchemaVersion = recorder.rootSpanID, recorder.schemaVersion
		descriptor.AgentName = "nano-research-agent"
		descriptor.SemanticConventionVersion = TraceSemanticConventionVersion
		if err := tx.QueryRow(ctx, `
			select r.chat_id, c.notebook_id
			from agent_runs r join chat_chats c on c.id = r.chat_id
			where r.id = $1`, runID).Scan(&descriptor.ChatID, &descriptor.NotebookID); err != nil {
			return nil, err
		}
		transaction, err := scope.Transaction(descriptor)
		if err != nil {
			return nil, err
		}
		recorder.direct = transaction
		return recorder, nil
	}
	if recorder.sequence < 1 {
		return nil, fmt.Errorf("%w: Run Trace has no root record", agentobs.ErrLifecycle)
	}
	return recorder, nil
}

func (r *RunTraceRecorder) RootSpanContext() agentobs.SpanContext {
	if r == nil {
		return agentobs.SpanContext{}
	}
	return agentobs.SpanContext{TraceID: r.traceID, SpanID: r.rootSpanID}
}

func (r *RunTraceRecorder) Descriptor() collector.TraceDescriptor {
	if r == nil {
		return collector.TraceDescriptor{}
	}
	if r.direct != nil {
		return r.direct.Descriptor()
	}
	return collector.TraceDescriptor{
		TraceID: r.traceID, RunID: r.runID, RootSpanID: r.rootSpanID,
		AgentName: "nano-research-agent", SchemaVersion: r.schemaVersion,
		SemanticConventionVersion: TraceSemanticConventionVersion,
	}
}

func (r *RunTraceRecorder) SpanContextByIdentity(ctx context.Context, identityKey string) (agentobs.SpanContext, error) {
	if r == nil || r.tx == nil {
		return agentobs.SpanContext{}, errors.New("nil Run Trace Recorder")
	}
	if r.direct != nil {
		spanID, err := DeterministicSpanID(r.traceID, identityKey)
		if err != nil {
			return agentobs.SpanContext{}, err
		}
		return agentobs.SpanContext{TraceID: r.traceID, SpanID: spanID}, nil
	}
	var traceID agentobs.TraceID
	var spanID agentobs.SpanID
	var err error
	if r.ownedLookup {
		err = r.tx.QueryRow(ctx, `
			select trace_id, span_id
			from nano_owned_trace_span($1, $2)`, r.runID, identityKey).Scan(&traceID, &spanID)
	} else {
		traceID = r.traceID
		err = r.tx.QueryRow(ctx, `
			select span_id
			from agentobs_outbox_records
			where trace_id = $1 and identity_key = $2 and record_kind = 'span_started'`, r.traceID, identityKey).Scan(&spanID)
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return agentobs.SpanContext{}, ErrTraceNotFound
		}
		return agentobs.SpanContext{}, err
	}
	if traceID != r.traceID {
		return agentobs.SpanContext{}, fmt.Errorf("%w: owned Span changed Trace", agentobs.ErrLifecycle)
	}
	return agentobs.SpanContext{TraceID: traceID, SpanID: spanID}, nil
}

func (r *RunTraceRecorder) Record(ctx context.Context, record agentobs.Record) error {
	if r == nil || r.tx == nil {
		return errors.New("nil Run Trace Recorder")
	}
	record = normalizeTraceRecord(record)
	if err := record.Validate(); err != nil {
		return err
	}
	if record.TraceID != r.traceID || record.SchemaVersion != r.schemaVersion {
		return fmt.Errorf("%w: record changed Run Trace envelope", agentobs.ErrLifecycle)
	}
	nextSequence := r.sequence + 1
	if r.direct != nil {
		if err := r.direct.Record(ctx, record); err != nil {
			return err
		}
	} else if err := insertTraceRecord(ctx, r.tx, nextSequence, record); err != nil {
		return classifyTraceDatabaseError(err)
	}
	r.sequence = nextSequence
	return nil
}

func (r *RunTraceRecorder) SpanIDForIdentity(traceID agentobs.TraceID, identityKey string) agentobs.SpanID {
	spanID, _ := DeterministicSpanID(traceID, identityKey)
	return spanID
}

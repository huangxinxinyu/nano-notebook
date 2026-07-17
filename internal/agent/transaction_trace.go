package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
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
}

var _ agentobs.Recorder = (*RunTraceRecorder)(nil)

func NewRunTraceRecorder(ctx context.Context, tx pgx.Tx, runID string) (*RunTraceRecorder, error) {
	if tx == nil || runID == "" {
		return nil, errors.New("Run Trace Recorder dependencies are incomplete")
	}
	var recorder RunTraceRecorder
	recorder.tx = tx
	recorder.runID = runID
	if err := tx.QueryRow(ctx, `
		select trace_id, root_span_id, schema_version
		from agent_traces where run_id = $1`, runID).Scan(
		&recorder.traceID, &recorder.rootSpanID, &recorder.schemaVersion,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTraceNotFound
		}
		return nil, err
	}
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "agent_trace:"+string(recorder.traceID)); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, `
		select coalesce(max(sequence_no), 0)
		from agent_trace_records where trace_id = $1`, recorder.traceID).Scan(&recorder.sequence); err != nil {
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

func (r *RunTraceRecorder) SpanContextByIdentity(ctx context.Context, identityKey string) (agentobs.SpanContext, error) {
	if r == nil || r.tx == nil {
		return agentobs.SpanContext{}, errors.New("nil Run Trace Recorder")
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
		from agent_trace_records
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
	if err := insertTraceRecord(ctx, r.tx, nextSequence, record); err != nil {
		return classifyTraceDatabaseError(err)
	}
	r.sequence = nextSequence
	return nil
}

package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
)

type AttemptTraceRecorder struct {
	runtime *PostgresRuntime
	attempt Attempt
}

var _ agentobs.Recorder = (*AttemptTraceRecorder)(nil)

func (r *PostgresRuntime) StartAttemptTrace(ctx context.Context, attempt Attempt) (context.Context, *agentobs.Tracer, error) {
	tx, err := r.workerTx(ctx)
	if err != nil {
		return ctx, nil, err
	}
	defer tx.Rollback(ctx)
	if err := lockCheckpointAuthority(ctx, tx, attempt); err != nil {
		return ctx, nil, err
	}
	recorder, err := NewRunTraceRecorder(ctx, tx, attempt.RunID)
	if err != nil {
		return ctx, nil, err
	}
	attemptSpan, err := recorder.SpanContextByIdentity(ctx, TraceAttemptStartIdentity(attempt.RunID, attempt.AttemptNo))
	if err != nil {
		return ctx, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ctx, nil, err
	}
	destinations := []agentobs.Destination{{
		Name: "nano-postgres", Class: agentobs.DeliveryRequired,
		Exporter: recorderExporter{recorder: &AttemptTraceRecorder{runtime: r, attempt: attempt}},
	}}
	if r.telemetry != nil {
		destinations = append(destinations, agentobs.Destination{
			Name: "opentelemetry", Class: agentobs.DeliveryBestEffort, Exporter: r.telemetry,
		})
	}
	sdkRuntime, err := agentobs.NewRuntime(agentobs.RuntimeConfig{
		Destinations: destinations,
		OnDiagnostic: func(_ context.Context, diagnostic agentobs.Diagnostic) {
			slog.Warn("Agent Trace best-effort delivery failed", "destination", diagnostic.Destination, "operation", diagnostic.Operation, "error", diagnostic.Err)
		},
	})
	if err != nil {
		return ctx, nil, err
	}
	tracer, err := agentobs.NewTracer(agentobs.TracerConfig{
		Recorder:                  sdkRuntime,
		SemanticConventionVersion: TraceSemanticConventionVersion,
	})
	if err != nil {
		return ctx, nil, err
	}
	return agentobs.ContextWithSpanContext(ctx, attemptSpan), tracer, nil
}

type recorderExporter struct {
	recorder agentobs.Recorder
}

func (e recorderExporter) Export(ctx context.Context, record agentobs.Record) error {
	return e.recorder.Record(ctx, record)
}
func (recorderExporter) ForceFlush(context.Context) error { return nil }
func (recorderExporter) Shutdown(context.Context) error   { return nil }

func (r *PostgresRuntime) PreviousActionSpan(ctx context.Context, attempt Attempt, logicalActionID string) (agentobs.SpanContext, bool, error) {
	tx, err := r.workerTx(ctx)
	if err != nil {
		return agentobs.SpanContext{}, false, err
	}
	defer tx.Rollback(ctx)
	if err := lockCheckpointAuthority(ctx, tx, attempt); err != nil {
		return agentobs.SpanContext{}, false, err
	}
	recorder, err := NewRunTraceRecorder(ctx, tx, attempt.RunID)
	if err != nil {
		return agentobs.SpanContext{}, false, err
	}
	for priorAttempt := attempt.AttemptNo - 1; priorAttempt > 0; priorAttempt-- {
		span, err := recorder.SpanContextByIdentity(ctx, TraceActionStartIdentity(attempt.RunID, priorAttempt, logicalActionID))
		if err == nil {
			if err := tx.Commit(ctx); err != nil {
				return agentobs.SpanContext{}, false, err
			}
			return span, true, nil
		}
		if !errors.Is(err, ErrTraceNotFound) {
			return agentobs.SpanContext{}, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return agentobs.SpanContext{}, false, err
	}
	return agentobs.SpanContext{}, false, nil
}

func (r *AttemptTraceRecorder) Record(ctx context.Context, record agentobs.Record) error {
	if r == nil || r.runtime == nil {
		return errors.New("nil Attempt Trace Recorder")
	}
	record = normalizeTraceRecord(record)
	if err := record.Validate(); err != nil {
		return err
	}
	var appendErr error
	for try := 0; try < 2; try++ {
		appendErr = r.recordOnce(ctx, record)
		if appendErr == nil {
			return nil
		}
		if errors.Is(appendErr, ErrLeaseLost) || errors.Is(appendErr, ErrRunDeadlineExceeded) || errors.Is(appendErr, agentobs.ErrIdentityConflict) || errors.Is(appendErr, agentobs.ErrLifecycle) || errors.Is(appendErr, agentobs.ErrLimitExceeded) || ctx.Err() != nil {
			return appendErr
		}
		matched, current, reconcileErr := r.reconcile(ctx, record)
		if reconcileErr != nil {
			return errors.Join(appendErr, reconcileErr)
		}
		if matched {
			return nil
		}
		if !current {
			return ErrLeaseLost
		}
	}
	return fmt.Errorf("Attempt Trace append exhausted retries: %w", appendErr)
}

func (r *AttemptTraceRecorder) recordOnce(ctx context.Context, record agentobs.Record) error {
	tx, err := r.runtime.workerTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockCheckpointAuthority(ctx, tx, r.attempt); err != nil {
		return err
	}
	recorder, err := NewRunTraceRecorder(ctx, tx, r.attempt.RunID)
	if err != nil {
		return err
	}
	if err := recorder.Record(ctx, record); err != nil {
		return err
	}
	return r.runtime.commit(ctx, tx)
}

func (r *AttemptTraceRecorder) reconcile(ctx context.Context, record agentobs.Record) (matched bool, current bool, resultErr error) {
	tx, err := r.runtime.workerTx(ctx)
	if err != nil {
		return false, false, err
	}
	defer tx.Rollback(ctx)
	var traceID agentobs.TraceID
	var schemaVersion int
	if err := tx.QueryRow(ctx, `select trace_id, schema_version from agent_trace_refs where run_id = $1`, r.attempt.RunID).Scan(&traceID, &schemaVersion); err != nil {
		return false, false, err
	}
	if traceID != record.TraceID || schemaVersion != record.SchemaVersion {
		return false, false, fmt.Errorf("%w: Attempt record changed Trace envelope", agentobs.ErrLifecycle)
	}
	existing, found, err := traceRecordByIdentity(ctx, tx, traceID, record.IdentityKey, schemaVersion)
	if err != nil {
		return false, false, err
	}
	if found {
		if err := reconcileTraceRecord(existing, record); err != nil {
			return false, false, err
		}
		return true, false, tx.Commit(ctx)
	}
	if err := lockCheckpointAuthority(ctx, tx, r.attempt); err != nil {
		if errors.Is(err, ErrLeaseLost) || errors.Is(err, ErrRunDeadlineExceeded) {
			return false, false, nil
		}
		return false, false, err
	}
	return false, true, tx.Commit(ctx)
}

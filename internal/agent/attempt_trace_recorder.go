package agent

import (
	"context"
	"errors"
	"log/slog"

	"github.com/huangxinxinyu/nano-notebook/internal/agentbatch"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/huangxinxinyu/nano-notebook/internal/replay"
)

type AttemptTraceRecorder struct {
	runtime *PostgresRuntime
	attempt Attempt
	trace   collector.TraceDescriptor
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
	traceCtx, traceScope, err := r.beginTraceScope(ctx)
	if err != nil {
		return ctx, nil, err
	}
	if traceScope != nil {
		defer traceScope.Rollback()
	}
	recorder, err := NewRunTraceRecorder(traceCtx, tx, attempt.RunID)
	if err != nil {
		return ctx, nil, err
	}
	attemptSpan, err := recorder.SpanContextByIdentity(traceCtx, TraceAttemptStartIdentity(attempt.RunID, attempt.AttemptNo))
	if err != nil {
		return ctx, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ctx, nil, err
	}
	attemptRecorder := &AttemptTraceRecorder{runtime: r, attempt: attempt, trace: recorder.Descriptor()}
	destinations := []agentobs.Destination{{
		Name: "nano-postgres", Class: agentobs.DeliveryRequired,
		Exporter: recorderExporter{recorder: attemptRecorder},
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
		IdentitySpanIDGenerator:   attemptRecorder,
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
	if attempt.AttemptNo <= 1 || logicalActionID == "" {
		return agentobs.SpanContext{}, false, nil
	}
	tx, err := r.workerTx(ctx)
	if err != nil {
		return agentobs.SpanContext{}, false, err
	}
	defer tx.Rollback(ctx)
	if err := lockCheckpointAuthority(ctx, tx, attempt); err != nil {
		return agentobs.SpanContext{}, false, err
	}
	var traceID agentobs.TraceID
	if err := tx.QueryRow(ctx, `select trace_id from agent_trace_refs where run_id = $1`, attempt.RunID).Scan(&traceID); err != nil {
		return agentobs.SpanContext{}, false, err
	}
	spanID, err := DeterministicSpanID(traceID, TraceActionStartIdentity(attempt.RunID, attempt.AttemptNo-1, logicalActionID))
	if err != nil {
		return agentobs.SpanContext{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return agentobs.SpanContext{}, false, err
	}
	return agentobs.SpanContext{TraceID: traceID, SpanID: spanID}, true, nil
}

func (r *AttemptTraceRecorder) Record(ctx context.Context, record agentobs.Record) error {
	if r == nil || r.runtime == nil {
		return errors.New("nil Attempt Trace Recorder")
	}
	record = normalizeTraceRecord(record)
	if err := record.Validate(); err != nil {
		return err
	}
	if r.runtime.traceSink == nil {
		return nil
	}
	if record.TraceID != r.trace.TraceID || record.SchemaVersion != r.trace.SchemaVersion ||
		record.SemanticConventionVersion != r.trace.SemanticConventionVersion {
		return errors.New("Attempt Trace record changed its direct-delivery envelope")
	}
	attachments, err := directReplayAttachments(r.runtime.replayStager, record)
	if err != nil {
		return err
	}
	return r.runtime.traceSink.Offer(ctx, agentbatch.Envelope{Trace: r.trace, Record: record, Attachments: attachments})
}

type stagedReplaySource interface {
	StagedAttachment(string) (replay.StagedAttachment, bool)
}

func directReplayAttachments(stager ReplayStager, record agentobs.Record) ([]collector.AttachmentDescriptor, error) {
	references, err := replay.AttachmentReferences(record.Attributes)
	if err != nil || len(references) == 0 {
		return nil, err
	}
	source, ok := stager.(stagedReplaySource)
	if !ok {
		return nil, errors.New("direct Replay staging descriptor source is unavailable")
	}
	attachments := make([]collector.AttachmentDescriptor, 0, len(references))
	for _, reference := range references {
		staged, found := source.StagedAttachment(reference.AttachmentID)
		if !found || staged.Class != reference.Class {
			return nil, errors.New("direct Replay staging descriptor does not resolve")
		}
		attachments = append(attachments, collector.AttachmentDescriptor{
			AttachmentID: staged.AttachmentID, RecordIdentityKey: record.IdentityKey,
			Class: staged.Class, SchemaVersion: staged.SchemaVersion,
			PlaintextSHA256: staged.PlaintextSHA256, StagingObjectKey: staged.ObjectKey,
			CiphertextBytes: staged.CiphertextBytes, CiphertextSHA256: staged.CiphertextSHA256,
			Compression: staged.Compression, Encryption: staged.Encryption, KeyID: staged.KeyID,
			WrappedKey: append([]byte(nil), staged.WrappedKey...), Nonce: append([]byte(nil), staged.Nonce...),
			ExpiresAt: staged.ExpiresAt,
		})
	}
	return attachments, nil
}

func (r *AttemptTraceRecorder) SpanIDForIdentity(traceID agentobs.TraceID, identityKey string) agentobs.SpanID {
	spanID, _ := DeterministicSpanID(traceID, identityKey)
	return spanID
}

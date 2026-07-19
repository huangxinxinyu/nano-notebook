package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/jackc/pgx/v5"
)

type AdmissionTraceRecorder struct {
	tx            pgx.Tx
	runID         string
	traceID       agentobs.TraceID
	schemaVersion int
	sequence      int
	direct        *TraceTransaction
}

var _ agentobs.Recorder = (*AdmissionTraceRecorder)(nil)

func NewAdmissionTraceRecorder(tx pgx.Tx, runID string) (*AdmissionTraceRecorder, error) {
	if tx == nil || strings.TrimSpace(runID) == "" {
		return nil, errors.New("admission Trace Recorder dependencies are incomplete")
	}
	return &AdmissionTraceRecorder{tx: tx, runID: runID}, nil
}

func (r *AdmissionTraceRecorder) Record(ctx context.Context, record agentobs.Record) error {
	if r == nil || r.tx == nil {
		return errors.New("nil admission Trace Recorder")
	}
	record = normalizeTraceRecord(record)
	if err := record.Validate(); err != nil {
		return err
	}
	if r.sequence == 0 {
		scope, direct := TraceScopeFromContext(ctx)
		if !direct {
			return errors.New("admission Trace Recorder requires direct Trace delivery scope")
		}
		descriptor, err := createTraceAnchorInTx(ctx, r.tx, r.runID, record)
		if err != nil {
			return err
		}
		r.direct, err = scope.Transaction(descriptor)
		if err != nil {
			return err
		}
		if err := r.direct.Record(ctx, record); err != nil {
			return err
		}
		r.traceID = record.TraceID
		r.schemaVersion = record.SchemaVersion
		r.sequence = 1
		return nil
	}
	if record.TraceID != r.traceID || record.SchemaVersion != r.schemaVersion {
		return errors.New("admission Trace record changed its envelope")
	}
	nextSequence := r.sequence + 1
	if err := r.direct.Record(ctx, record); err != nil {
		return err
	}
	r.sequence = nextSequence
	return nil
}

func (*AdmissionTraceRecorder) SpanIDForIdentity(traceID agentobs.TraceID, identityKey string) agentobs.SpanID {
	spanID, _ := DeterministicSpanID(traceID, identityKey)
	return spanID
}

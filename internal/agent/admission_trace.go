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
		if err := CreateTraceInTx(ctx, r.tx, r.runID, record); err != nil {
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
	if err := insertTraceRecord(ctx, r.tx, nextSequence, record); err != nil {
		return err
	}
	r.sequence = nextSequence
	return nil
}

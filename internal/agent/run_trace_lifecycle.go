package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/jackc/pgx/v5"
)

type RunTerminalTrace struct {
	CauseEvent string
	RunStatus  string
	SpanStatus agentobs.Status
	ErrorCode  string
	AttemptNo  int
}

func RecordAttemptWaitingInTx(ctx context.Context, tx pgx.Tx, runID, jobID string, attemptNo int) error {
	if tx == nil || runID == "" || jobID == "" || attemptNo < 1 {
		return errors.New("waiting Attempt Trace is incomplete")
	}
	recorder, err := NewRunTraceRecorder(ctx, tx, runID)
	if err != nil {
		return err
	}
	tracer, err := agentobs.NewTracer(agentobs.TracerConfig{Recorder: recorder, SemanticConventionVersion: TraceSemanticConventionVersion})
	if err != nil {
		return err
	}
	attemptSpan, err := recorder.SpanContextByIdentity(ctx, TraceAttemptStartIdentity(runID, attemptNo))
	if err != nil {
		return err
	}
	attemptContext := agentobs.ContextWithSpanContext(ctx, attemptSpan)
	return tracer.EndSpan(attemptContext, agentobs.SpanEnd{
		Name: TraceSpanJobAttempt, Status: agentobs.StatusOK,
		Attributes: []agentobs.Attribute{
			agentobs.String(TraceKeyJobID, jobID),
			agentobs.Int64(TraceKeyAttemptNumber, int64(attemptNo)),
			agentobs.String(TraceKeyRunStatus, "waiting"),
			agentobs.String(TraceKeyAttemptDisposition, string(AttemptWaiting)),
		},
	})
}

func RecordAttemptRetryableInTx(ctx context.Context, tx pgx.Tx, runID, jobID string, attemptNo int, errorCode string, backoff time.Duration) error {
	if tx == nil || runID == "" || jobID == "" || attemptNo < 1 || !safeAttemptErrorCode.MatchString(errorCode) || backoff <= 0 {
		return errors.New("retryable Attempt Trace is incomplete")
	}
	recorder, err := NewRunTraceRecorder(ctx, tx, runID)
	if err != nil {
		return err
	}
	tracer, err := agentobs.NewTracer(agentobs.TracerConfig{Recorder: recorder, SemanticConventionVersion: TraceSemanticConventionVersion})
	if err != nil {
		return err
	}
	attemptSpan, err := recorder.SpanContextByIdentity(ctx, TraceAttemptStartIdentity(runID, attemptNo))
	if err != nil {
		return err
	}
	attemptContext := agentobs.ContextWithSpanContext(ctx, attemptSpan)
	attributes := []agentobs.Attribute{
		agentobs.String(TraceKeyJobID, jobID),
		agentobs.Int64(TraceKeyAttemptNumber, int64(attemptNo)),
		agentobs.String(TraceKeyAttemptDisposition, string(AttemptRetryable)),
		agentobs.String(TraceKeyErrorCode, errorCode),
		agentobs.Int64(TraceKeyAttemptBackoffMilliseconds, backoff.Milliseconds()),
	}
	if err := tracer.Event(attemptContext, agentobs.Event{
		IdentityKey: fmt.Sprintf("run/%s/attempt/%d/disposition", runID, attemptNo),
		Name:        TraceEventAttemptDisposition, Attributes: attributes,
	}); err != nil {
		return err
	}
	return tracer.EndSpan(attemptContext, agentobs.SpanEnd{Name: TraceSpanJobAttempt, Status: agentobs.StatusError, Attributes: attributes})
}

func RecordAttemptLeaseExpiredInTx(ctx context.Context, tx pgx.Tx, runID, jobID string, attemptNo int) error {
	if tx == nil || runID == "" || jobID == "" || attemptNo < 1 {
		return errors.New("Attempt lease-expiry Trace is incomplete")
	}
	recorder, err := NewRunTraceRecorder(ctx, tx, runID)
	if err != nil {
		return err
	}
	tracer, err := agentobs.NewTracer(agentobs.TracerConfig{
		Recorder: recorder, SemanticConventionVersion: TraceSemanticConventionVersion,
	})
	if err != nil {
		return err
	}
	attemptSpan, err := recorder.SpanContextByIdentity(ctx, TraceAttemptStartIdentity(runID, attemptNo))
	if err != nil {
		return err
	}
	attemptContext := agentobs.ContextWithSpanContext(ctx, attemptSpan)
	return tracer.Event(attemptContext, agentobs.Event{
		IdentityKey: fmt.Sprintf("run/%s/attempt/%d/lease-expired", runID, attemptNo),
		Name:        TraceEventLeaseExpired,
		Attributes: []agentobs.Attribute{
			agentobs.String(TraceKeyJobID, jobID),
			agentobs.Int64(TraceKeyAttemptNumber, int64(attemptNo)),
			agentobs.String(TraceKeyAttemptDisposition, string(AttemptAbandoned)),
			agentobs.String(TraceKeyAttemptAbandonedCause, AttemptCauseLeaseLost),
		},
	})
}

func RecordRunTerminalInTx(ctx context.Context, tx pgx.Tx, runID string, terminal RunTerminalTrace) error {
	if tx == nil || runID == "" || terminal.RunStatus == "" || terminal.SpanStatus == "" {
		return errors.New("Run terminal Trace is incomplete")
	}
	var databaseRole string
	if err := tx.QueryRow(ctx, `select current_user`).Scan(&databaseRole); err != nil {
		return err
	}
	var recorder *RunTraceRecorder
	var err error
	if databaseRole == "nano_app" {
		recorder, err = NewOwnedRunTraceRecorder(ctx, tx, runID)
	} else {
		recorder, err = NewRunTraceRecorder(ctx, tx, runID)
	}
	if err != nil {
		return err
	}
	tracer, err := agentobs.NewTracer(agentobs.TracerConfig{
		Recorder: recorder, SemanticConventionVersion: TraceSemanticConventionVersion,
	})
	if err != nil {
		return err
	}
	rootContext := agentobs.ContextWithSpanContext(ctx, recorder.RootSpanContext())
	if terminal.CauseEvent != "" {
		attributes := []agentobs.Attribute{agentobs.String(TraceKeyRunStatus, terminal.RunStatus)}
		if terminal.ErrorCode != "" {
			attributes = append(attributes, agentobs.String(TraceKeyErrorCode, terminal.ErrorCode))
		}
		if err := tracer.Event(rootContext, agentobs.Event{
			IdentityKey: "run/" + runID + "/cause/" + terminal.CauseEvent,
			Name:        terminal.CauseEvent,
			Attributes:  attributes,
		}); err != nil {
			return err
		}
	}
	if terminal.AttemptNo > 0 {
		attemptSpan, err := recorder.SpanContextByIdentity(ctx, TraceAttemptStartIdentity(runID, terminal.AttemptNo))
		if err != nil {
			return err
		}
		attemptContext := agentobs.ContextWithSpanContext(ctx, attemptSpan)
		attributes := []agentobs.Attribute{agentobs.Int64(TraceKeyAttemptNumber, int64(terminal.AttemptNo))}
		disposition := AttemptTerminal
		if terminal.RunStatus == "completed" {
			disposition = AttemptCompleted
		} else if terminal.RunStatus == "cancelled" {
			disposition = AttemptAbandoned
		}
		attributes = append(attributes, agentobs.String(TraceKeyAttemptDisposition, string(disposition)))
		if disposition == AttemptAbandoned {
			attributes = append(attributes, agentobs.String(TraceKeyAttemptAbandonedCause, AttemptCauseCancelled))
		}
		if terminal.ErrorCode != "" {
			attributes = append(attributes, agentobs.String(TraceKeyErrorCode, terminal.ErrorCode))
		}
		if err := tracer.EndSpan(attemptContext, agentobs.SpanEnd{
			Name: TraceSpanJobAttempt, Status: terminal.SpanStatus, Attributes: attributes,
		}); err != nil {
			return err
		}
	}
	terminalAttributes := []agentobs.Attribute{agentobs.String(TraceKeyRunStatus, terminal.RunStatus)}
	if terminal.ErrorCode != "" {
		terminalAttributes = append(terminalAttributes, agentobs.String(TraceKeyErrorCode, terminal.ErrorCode))
	}
	if err := tracer.Event(rootContext, agentobs.Event{
		IdentityKey: fmt.Sprintf("run/%s/terminal/%s", runID, terminal.RunStatus),
		Name:        TraceEventRunTerminal,
		Attributes:  terminalAttributes,
	}); err != nil {
		return err
	}
	if err := tracer.EndSpan(rootContext, agentobs.SpanEnd{
		Name: TraceSpanAgentExecution, Status: terminal.SpanStatus, Attributes: terminalAttributes,
	}); err != nil {
		return err
	}
	return recordCancelledRuntimeSubagentsInTx(ctx, tx, runID)
}

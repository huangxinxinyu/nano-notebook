package instrumentation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/instrumentation"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/memory"
)

func TestInvokeRecordsAroundWrappedCallAndPreservesResult(t *testing.T) {
	exporter := memory.New()
	runtime, err := agentobs.NewRuntime(agentobs.RuntimeConfig{Destinations: []agentobs.Destination{{
		Name: "memory", Class: agentobs.DeliveryRequired, Exporter: exporter,
	}}})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	tracer, err := agentobs.NewTracer(agentobs.TracerConfig{Recorder: runtime})
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}
	rootCtx, _, err := tracer.StartTrace(context.Background(), agentobs.TraceStart{Name: "agent.execution"})
	if err != nil {
		t.Fatalf("StartTrace: %v", err)
	}
	called := false

	result, err := instrumentation.Invoke(rootCtx, tracer,
		agentobs.SpanStart{Name: "agent.model.call"},
		func(ctx context.Context) (string, error) {
			called = true
			if span, ok := agentobs.SpanContextFromContext(ctx); !ok || span.SpanID == "" {
				t.Fatal("wrapped call did not receive child Span Context")
			}
			return "output", nil
		},
		func(result string, err error) agentobs.SpanEnd {
			return agentobs.SpanEnd{
				Name:       "agent.model.call",
				Status:     agentobs.StatusOK,
				Attributes: []agentobs.Attribute{agentobs.String("agent.model.result_kind", result)},
			}
		},
	)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !called || result != "output" {
		t.Fatalf("wrapped result/call = %q/%t", result, called)
	}
	records := exporter.Records()
	if len(records) != 3 || records[1].Kind != agentobs.RecordSpanStarted || records[2].Kind != agentobs.RecordSpanEnded || records[1].SpanID != records[2].SpanID {
		t.Fatalf("wrapper records = %#v", records)
	}
}

func TestInvokeDoesNotCallWrappedSideEffectWhenStartFails(t *testing.T) {
	wantErr := errors.New("durable start failed")
	tracer, err := agentobs.NewTracer(agentobs.TracerConfig{Recorder: failingRecorder{err: wantErr}})
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}
	parent := agentobs.ContextWithSpanContext(context.Background(), agentobs.SpanContext{TraceID: "trace-1", SpanID: "root-1"})
	called := false

	result, err := instrumentation.Invoke(parent, tracer,
		agentobs.SpanStart{Name: "agent.action"},
		func(context.Context) (string, error) {
			called = true
			return "changed", nil
		}, nil,
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Invoke error = %v, want start failure", err)
	}
	if called || result != "" {
		t.Fatalf("side effect/result after failed start = %t/%q", called, result)
	}
}

func TestInvokeReturnsBusinessAndTerminalFailures(t *testing.T) {
	businessErr := errors.New("model unavailable")
	terminalErr := errors.New("durable terminal failed")
	recorder := &terminalFailRecorder{terminalErr: terminalErr}
	tracer, err := agentobs.NewTracer(agentobs.TracerConfig{Recorder: recorder})
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}
	parent := agentobs.ContextWithSpanContext(context.Background(), agentobs.SpanContext{TraceID: "trace-1", SpanID: "root-1"})

	result, err := instrumentation.Invoke(parent, tracer,
		agentobs.SpanStart{Name: "agent.model.call"},
		func(context.Context) (string, error) { return "partial", businessErr }, nil,
	)
	if result != "partial" || !errors.Is(err, businessErr) || !errors.Is(err, terminalErr) {
		t.Fatalf("result/error = %q/%v, want both failures", result, err)
	}
	if recorder.records[1].Status != agentobs.StatusError {
		t.Fatalf("default terminal status = %q, want error", recorder.records[1].Status)
	}
}

func TestInvokeClassifiesContextCancellation(t *testing.T) {
	recorder := &terminalFailRecorder{}
	tracer, err := agentobs.NewTracer(agentobs.TracerConfig{Recorder: recorder})
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}
	parent := agentobs.ContextWithSpanContext(context.Background(), agentobs.SpanContext{TraceID: "trace-1", SpanID: "root-1"})

	_, err = instrumentation.Invoke(parent, tracer,
		agentobs.SpanStart{Name: "agent.action"},
		func(context.Context) (struct{}, error) { return struct{}{}, context.Canceled }, nil,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Invoke error = %v", err)
	}
	if recorder.records[1].Status != agentobs.StatusCancelled {
		t.Fatalf("cancellation terminal status = %q", recorder.records[1].Status)
	}
}

type failingRecorder struct{ err error }

func (r failingRecorder) Record(context.Context, agentobs.Record) error { return r.err }

type terminalFailRecorder struct {
	records     []agentobs.Record
	terminalErr error
}

func (r *terminalFailRecorder) Record(_ context.Context, record agentobs.Record) error {
	r.records = append(r.records, record)
	if record.Kind == agentobs.RecordSpanEnded {
		return r.terminalErr
	}
	return nil
}

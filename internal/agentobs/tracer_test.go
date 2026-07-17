package agentobs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTracerRecordsRootAndChildHierarchy(t *testing.T) {
	recorder := &recordingRecorder{}
	clock := &sequenceClock{times: []time.Time{
		time.Unix(1_700_000_000, 0).UTC(),
		time.Unix(1_700_000_001, 0).UTC(),
	}}
	tracer, err := NewTracer(TracerConfig{
		Recorder:                  recorder,
		IDGenerator:               &sequenceIDs{traceIDs: []TraceID{"trace-1"}, spanIDs: []SpanID{"root-1", "child-1"}},
		Clock:                     clock.Now,
		SchemaVersion:             2,
		SemanticConventionVersion: 3,
		PayloadVersion:            4,
	})
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}

	rootCtx, root, err := tracer.StartTrace(context.Background(), TraceStart{
		Name:       "agent.execution",
		Attributes: []Attribute{String("agent.name", "fixture")},
	})
	if err != nil {
		t.Fatalf("StartTrace: %v", err)
	}
	childCtx, child, err := tracer.StartSpan(rootCtx, SpanStart{Name: "agent.model.call"})
	if err != nil {
		t.Fatalf("StartSpan: %v", err)
	}

	if root != (SpanContext{TraceID: "trace-1", SpanID: "root-1"}) {
		t.Fatalf("root context = %#v", root)
	}
	if child != (SpanContext{TraceID: "trace-1", SpanID: "child-1"}) {
		t.Fatalf("child context = %#v", child)
	}
	if got, ok := SpanContextFromContext(childCtx); !ok || got != child {
		t.Fatalf("child Go context = %#v, %t", got, ok)
	}

	rootRecord := recorder.records[0]
	if rootRecord.IdentityKey != "span/root-1/start" || rootRecord.ParentSpanID != "" {
		t.Fatalf("root record identity/parent = %q/%q", rootRecord.IdentityKey, rootRecord.ParentSpanID)
	}
	if rootRecord.SchemaVersion != 2 || rootRecord.SemanticConventionVersion != 3 || rootRecord.PayloadVersion != 4 {
		t.Fatalf("root record versions = %#v", rootRecord)
	}
	childRecord := recorder.records[1]
	if childRecord.TraceID != root.TraceID || childRecord.ParentSpanID != root.SpanID {
		t.Fatalf("child hierarchy = %#v", childRecord)
	}
}

func TestTracerRecordsTerminalEventAndCrossTraceLink(t *testing.T) {
	recorder := &recordingRecorder{}
	observed := time.Unix(1_700_000_100, 0).UTC()
	tracer, err := NewTracer(TracerConfig{Recorder: recorder})
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}
	ctx := ContextWithSpanContext(context.Background(), SpanContext{TraceID: "trace-1", SpanID: "span-1"})

	if err := tracer.EndSpan(ctx, SpanEnd{Name: "agent.action", Status: StatusOK, OccurredAt: observed}); err != nil {
		t.Fatalf("EndSpan: %v", err)
	}
	if err := tracer.Event(ctx, Event{
		IdentityKey: "checkpoint/proposal/1",
		Name:        "nano.checkpoint.accepted",
		OccurredAt:  observed.Add(time.Second),
	}); err != nil {
		t.Fatalf("Event: %v", err)
	}
	if err := tracer.Link(ctx, Link{
		IdentityKey: "attempt/2/continues",
		Name:        "continues",
		Target:      SpanContext{TraceID: "trace-0", SpanID: "attempt-1"},
		OccurredAt:  observed.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("Link: %v", err)
	}

	if got := recorder.records[0]; got.Kind != RecordSpanEnded || got.IdentityKey != "span/span-1/end" || got.Status != StatusOK || !got.OccurredAt.Equal(observed) {
		t.Fatalf("terminal record = %#v", got)
	}
	if got := recorder.records[1]; got.Kind != RecordEvent || got.IdentityKey != "checkpoint/proposal/1" {
		t.Fatalf("Event record = %#v", got)
	}
	if got := recorder.records[2]; got.Kind != RecordLink || got.TargetTraceID != "trace-0" || got.TargetSpanID != "attempt-1" {
		t.Fatalf("Link record = %#v", got)
	}
}

func TestTracerSupportsCallerSuppliedRootIdentity(t *testing.T) {
	recorder := &recordingRecorder{}
	tracer, err := NewTracer(TracerConfig{Recorder: recorder})
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}

	_, span, err := tracer.StartTrace(context.Background(), TraceStart{
		TraceID:     "admission-trace",
		SpanID:      "admission-root",
		IdentityKey: "run/run-1/root-start",
		Name:        "agent.execution",
	})
	if err != nil {
		t.Fatalf("StartTrace: %v", err)
	}
	if span.TraceID != "admission-trace" || span.SpanID != "admission-root" {
		t.Fatalf("caller-supplied Span Context = %#v", span)
	}
	if got := recorder.records[0].IdentityKey; got != "run/run-1/root-start" {
		t.Fatalf("identity key = %q", got)
	}
}

func TestTracerDoesNotPublishContextWhenRequiredStartFails(t *testing.T) {
	wantErr := errors.New("durable exporter unavailable")
	tracer, err := NewTracer(TracerConfig{
		Recorder:    &recordingRecorder{err: wantErr},
		IDGenerator: &sequenceIDs{traceIDs: []TraceID{"trace-1"}, spanIDs: []SpanID{"span-1"}},
	})
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}
	original := context.Background()

	gotCtx, gotSpan, err := tracer.StartTrace(original, TraceStart{Name: "agent.execution"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("StartTrace error = %v, want %v", err, wantErr)
	}
	if gotCtx != original || gotSpan != (SpanContext{}) {
		t.Fatalf("failed start published context/span: %#v %#v", gotCtx, gotSpan)
	}
}

func TestTracerRejectsChildWithoutParentBeforeRecording(t *testing.T) {
	recorder := &recordingRecorder{}
	tracer, err := NewTracer(TracerConfig{Recorder: recorder})
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}

	if _, _, err := tracer.StartSpan(context.Background(), SpanStart{Name: "agent.model.call"}); err == nil {
		t.Fatal("StartSpan without parent succeeded")
	}
	if len(recorder.records) != 0 {
		t.Fatalf("recorded %d records", len(recorder.records))
	}
}

func TestTracerValidatesRecordBeforeCallingRecorder(t *testing.T) {
	recorder := &recordingRecorder{}
	tracer, err := NewTracer(TracerConfig{Recorder: recorder})
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}
	ctx := ContextWithSpanContext(context.Background(), SpanContext{TraceID: "trace-1", SpanID: "span-1"})

	if err := tracer.Event(ctx, Event{Name: "agent.invalid.missing_identity"}); err == nil {
		t.Fatal("Event without stable identity succeeded")
	}
	if len(recorder.records) != 0 {
		t.Fatalf("invalid record reached Recorder: %#v", recorder.records)
	}
}

func TestTracerUsesInstalledRecordLimits(t *testing.T) {
	recorder := &recordingRecorder{}
	limits := DefaultLimits()
	limits.MaxPayloadBytes = 32 * 1024
	tracer, err := NewTracer(TracerConfig{Recorder: recorder, RecordLimits: &limits})
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}
	ctx := ContextWithSpanContext(context.Background(), SpanContext{TraceID: "trace-1", SpanID: "span-1"})
	attributes := make([]Attribute, 4)
	for index := range attributes {
		attributes[index] = String("payload.part"+string(rune('a'+index)), strings.Repeat("x", 4096))
	}

	if err := tracer.Event(ctx, Event{IdentityKey: "event/large", Name: "agent.large", Attributes: attributes}); err != nil {
		t.Fatalf("custom Tracer limits rejected Event: %v", err)
	}
	if len(recorder.records) != 1 {
		t.Fatalf("record count = %d, want 1", len(recorder.records))
	}
}

type recordingRecorder struct {
	records []Record
	err     error
}

func (r *recordingRecorder) Record(_ context.Context, record Record) error {
	r.records = append(r.records, record)
	return r.err
}

type sequenceIDs struct {
	traceIDs []TraceID
	spanIDs  []SpanID
}

func (s *sequenceIDs) NewTraceID() TraceID {
	id := s.traceIDs[0]
	s.traceIDs = s.traceIDs[1:]
	return id
}

func (s *sequenceIDs) NewSpanID() SpanID {
	id := s.spanIDs[0]
	s.spanIDs = s.spanIDs[1:]
	return id
}

type sequenceClock struct {
	times []time.Time
}

func (s *sequenceClock) Now() time.Time {
	now := s.times[0]
	s.times = s.times[1:]
	return now
}

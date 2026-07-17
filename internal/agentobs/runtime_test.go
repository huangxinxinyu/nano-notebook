package agentobs

import (
	"context"
	"errors"
	"testing"
)

func TestRuntimeRecordReturnsRequiredFailureAfterDispatchingAllDestinations(t *testing.T) {
	requiredErr := errors.New("durable unavailable")
	required := &recordingExporter{exportErr: requiredErr}
	bestEffort := &recordingExporter{}
	runtime, err := NewRuntime(RuntimeConfig{Destinations: []Destination{
		{Name: "durable", Class: DeliveryRequired, Exporter: required},
		{Name: "otel", Class: DeliveryBestEffort, Exporter: bestEffort},
	}})
	if err != nil {
		t.Fatalf("new Runtime: %v", err)
	}

	err = runtime.Record(context.Background(), validRecord(RecordEvent))
	if !errors.Is(err, requiredErr) {
		t.Fatalf("Record error = %v, want required failure", err)
	}
	if len(required.records) != 1 || len(bestEffort.records) != 1 {
		t.Fatalf("dispatch counts = required %d best-effort %d, want 1 each", len(required.records), len(bestEffort.records))
	}
}

func TestRuntimeRecordReportsBestEffortFailureWithoutReturningIt(t *testing.T) {
	bestEffortErr := errors.New("otel unavailable")
	bestEffort := &recordingExporter{exportErr: bestEffortErr}
	var diagnostics []Diagnostic
	runtime, err := NewRuntime(RuntimeConfig{
		Destinations: []Destination{{Name: "otel", Class: DeliveryBestEffort, Exporter: bestEffort}},
		OnDiagnostic: func(_ context.Context, diagnostic Diagnostic) {
			diagnostics = append(diagnostics, diagnostic)
		},
	})
	if err != nil {
		t.Fatalf("new Runtime: %v", err)
	}

	if err := runtime.Record(context.Background(), validRecord(RecordEvent)); err != nil {
		t.Fatalf("Record returned best-effort failure: %v", err)
	}
	if len(diagnostics) != 1 || diagnostics[0].Destination != "otel" || !errors.Is(diagnostics[0].Err, bestEffortErr) {
		t.Fatalf("diagnostics = %#v, want otel failure", diagnostics)
	}
}

func TestRuntimeRecordRejectsInvalidRecordBeforeExporterDispatch(t *testing.T) {
	exporter := &recordingExporter{}
	runtime, err := NewRuntime(RuntimeConfig{Destinations: []Destination{{Name: "durable", Class: DeliveryRequired, Exporter: exporter}}})
	if err != nil {
		t.Fatalf("new Runtime: %v", err)
	}

	if err := runtime.Record(context.Background(), Record{}); err == nil {
		t.Fatal("expected invalid Record error")
	}
	if len(exporter.records) != 0 {
		t.Fatalf("invalid Record dispatch count = %d, want 0", len(exporter.records))
	}
}

func TestRuntimeUsesInstalledRecordLimits(t *testing.T) {
	exporter := &recordingExporter{}
	limits := DefaultLimits()
	limits.MaxAttributes = 1
	runtime, err := NewRuntime(RuntimeConfig{
		RecordLimits: &limits,
		Destinations: []Destination{{Name: "durable", Class: DeliveryRequired, Exporter: exporter}},
	})
	if err != nil {
		t.Fatalf("new Runtime: %v", err)
	}
	record := validRecord(RecordEvent)
	record.Attributes = []Attribute{String("agent.name", "fixture"), String("agent.version", "1")}

	if err := runtime.Record(context.Background(), record); err == nil {
		t.Fatal("Runtime accepted a record outside installed limits")
	}
	if len(exporter.records) != 0 {
		t.Fatalf("out-of-policy Record dispatch count = %d", len(exporter.records))
	}
}

func TestRuntimeForceFlushSeparatesRequiredAndBestEffortFailures(t *testing.T) {
	requiredErr := errors.New("durable flush failed")
	bestEffortErr := errors.New("otel flush failed")
	var diagnostics []Diagnostic
	runtime, err := NewRuntime(RuntimeConfig{
		Destinations: []Destination{
			{Name: "durable", Class: DeliveryRequired, Exporter: &recordingExporter{flushErr: requiredErr}},
			{Name: "otel", Class: DeliveryBestEffort, Exporter: &recordingExporter{flushErr: bestEffortErr}},
		},
		OnDiagnostic: func(_ context.Context, diagnostic Diagnostic) {
			diagnostics = append(diagnostics, diagnostic)
		},
	})
	if err != nil {
		t.Fatalf("new Runtime: %v", err)
	}

	err = runtime.ForceFlush(context.Background())
	if !errors.Is(err, requiredErr) {
		t.Fatalf("ForceFlush error = %v, want required failure", err)
	}
	if len(diagnostics) != 1 || diagnostics[0].Operation != "flush" || !errors.Is(diagnostics[0].Err, bestEffortErr) {
		t.Fatalf("diagnostics = %#v, want best-effort flush failure", diagnostics)
	}
}

func TestRuntimeShutdownIsIdempotentAndRejectsLaterRecords(t *testing.T) {
	exporter := &recordingExporter{}
	runtime, err := NewRuntime(RuntimeConfig{Destinations: []Destination{{Name: "durable", Class: DeliveryRequired, Exporter: exporter}}})
	if err != nil {
		t.Fatalf("new Runtime: %v", err)
	}

	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
	if exporter.shutdownCalls != 1 {
		t.Fatalf("Shutdown calls = %d, want 1", exporter.shutdownCalls)
	}
	if err := runtime.Record(context.Background(), validRecord(RecordEvent)); !errors.Is(err, ErrRuntimeShutdown) {
		t.Fatalf("Record after Shutdown error = %v, want ErrRuntimeShutdown", err)
	}
}

type recordingExporter struct {
	records       []Record
	exportErr     error
	flushErr      error
	closeErr      error
	shutdownCalls int
}

func (e *recordingExporter) Export(_ context.Context, record Record) error {
	e.records = append(e.records, record)
	return e.exportErr
}

func (e *recordingExporter) ForceFlush(context.Context) error {
	return e.flushErr
}

func (e *recordingExporter) Shutdown(context.Context) error {
	e.shutdownCalls++
	return e.closeErr
}

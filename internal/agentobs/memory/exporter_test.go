package memory_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/memory"
)

func TestExporterOwnsStoredAndReturnedRecords(t *testing.T) {
	exporter := memory.New()
	record := validRecord("record-1")

	if err := exporter.Export(context.Background(), record); err != nil {
		t.Fatalf("Export: %v", err)
	}
	record.Attributes[0] = agentobs.String("agent.name", "mutated-input")

	first := exporter.Records()
	if got := first[0].Attributes[0].Value.String; got != "fixture" {
		t.Fatalf("stored attribute = %q, want fixture", got)
	}
	first[0].Attributes[0] = agentobs.String("agent.name", "mutated-output")

	second := exporter.Records()
	if got := second[0].Attributes[0].Value.String; got != "fixture" {
		t.Fatalf("second snapshot attribute = %q, want fixture", got)
	}
}

func TestExporterLifecycle(t *testing.T) {
	exporter := memory.New()
	ctx := context.Background()

	if err := exporter.ForceFlush(ctx); err != nil {
		t.Fatalf("ForceFlush before shutdown: %v", err)
	}
	if err := exporter.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := exporter.Shutdown(ctx); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
	if err := exporter.Export(ctx, validRecord("late")); !errors.Is(err, memory.ErrShutdown) {
		t.Fatalf("Export after shutdown error = %v, want ErrShutdown", err)
	}
	if err := exporter.ForceFlush(ctx); !errors.Is(err, memory.ErrShutdown) {
		t.Fatalf("ForceFlush after shutdown error = %v, want ErrShutdown", err)
	}
}

func TestExporterLimitsAreConfigurable(t *testing.T) {
	exporter, err := memory.NewWithConfig(memory.Config{MaxRecordsPerTrace: 1})
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	if err := exporter.Export(context.Background(), validRecord("root")); err != nil {
		t.Fatalf("root Export: %v", err)
	}
	event := validRecord("event")
	event.Kind = agentobs.RecordEvent
	if err := exporter.Export(context.Background(), event); !errors.Is(err, agentobs.ErrLimitExceeded) {
		t.Fatalf("second record error = %v, want ErrLimitExceeded", err)
	}

	if _, err := memory.NewWithConfig(memory.Config{MaxLinksPerSpan: -1}); err == nil {
		t.Fatal("negative exporter limit succeeded")
	}
}

func TestExporterSupportsConcurrentSnapshotsAndExports(t *testing.T) {
	exporter := memory.New()
	const count = 64

	var writers sync.WaitGroup
	writers.Add(count)
	for index := 0; index < count; index++ {
		go func(index int) {
			defer writers.Done()
			record := validRecord(fmt.Sprintf("record-%d", index))
			record.TraceID = agentobs.TraceID(fmt.Sprintf("trace-%d", index))
			record.SpanID = agentobs.SpanID(fmt.Sprintf("span-%d", index))
			if err := exporter.Export(context.Background(), record); err != nil {
				t.Errorf("Export %d: %v", index, err)
			}
		}(index)
	}

	for index := 0; index < count; index++ {
		_ = exporter.Records()
	}
	writers.Wait()

	if got := len(exporter.Records()); got != count {
		t.Fatalf("record count = %d, want %d", got, count)
	}
}

func validRecord(identity string) agentobs.Record {
	return agentobs.Record{
		SchemaVersion:             1,
		SemanticConventionVersion: 1,
		IdentityKey:               identity,
		Kind:                      agentobs.RecordSpanStarted,
		TraceID:                   "trace-1",
		SpanID:                    "span-1",
		Name:                      "agent.execution",
		OccurredAt:                time.Unix(1_700_000_000, 0).UTC(),
		PayloadVersion:            1,
		Attributes:                []agentobs.Attribute{agentobs.String("agent.name", "fixture")},
	}
}

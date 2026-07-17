package exportertest

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
)

type Harness struct {
	New     func(*testing.T) agentobs.Exporter
	Records func(*testing.T, agentobs.Exporter, agentobs.TraceID) []agentobs.Record
}

func Run(t *testing.T, harness Harness) {
	t.Helper()
	if harness.New == nil || harness.Records == nil {
		t.Fatal("exporter conformance Harness is incomplete")
	}

	t.Run("same identity and canonical fact reconcile", func(t *testing.T) {
		record := rootStart("trace-1", "root-1")
		replay := record
		replay.Attributes = []agentobs.Attribute{agentobs.Int64("agent.token.total", 3), agentobs.String("agent.model.name", "fixture")}
		record.Attributes = []agentobs.Attribute{agentobs.String("agent.model.name", "fixture"), agentobs.Int64("agent.token.total", 3)}

		exporter := harness.New(t)
		exportOK(t, exporter, record)
		exportOK(t, exporter, replay)
		if got := len(harness.Records(t, exporter, record.TraceID)); got != 1 {
			t.Fatalf("reconciled record count = %d, want 1", got)
		}

		conflict := record
		conflict.Name = "agent.changed"
		if err := exporter.Export(context.Background(), conflict); err == nil {
			t.Fatal("conflicting identity succeeded")
		}
		if got := len(harness.Records(t, exporter, record.TraceID)); got != 1 {
			t.Fatalf("conflict changed record count to %d", got)
		}
	})

	t.Run("Span hierarchy and terminal lifecycle", func(t *testing.T) {
		exporter := harness.New(t)
		child := childStart("trace-1", "child-1", "root-1")
		if err := exporter.Export(context.Background(), child); err == nil {
			t.Fatal("child before parent succeeded")
		}
		exportOK(t, exporter, rootStart("trace-1", "root-1"))
		exportOK(t, exporter, child)
		if err := exporter.Export(context.Background(), spanEnd("trace-1", "missing", "agent.model.call", "span/missing/end")); err == nil {
			t.Fatal("terminal record without start succeeded")
		}
		terminal := spanEnd("trace-1", "child-1", "agent.model.call", "span/child-1/end")
		exportOK(t, exporter, terminal)
		duplicateTerminal := terminal
		duplicateTerminal.IdentityKey = "another-terminal-identity"
		if err := exporter.Export(context.Background(), duplicateTerminal); err == nil {
			t.Fatal("second terminal fact succeeded")
		}
		records := harness.Records(t, exporter, "trace-1")
		if len(records) != 3 || records[0].SpanID != "root-1" || records[1].SpanID != "child-1" || records[2].Kind != agentobs.RecordSpanEnded {
			t.Fatalf("ordered lifecycle records = %#v", records)
		}
	})

	t.Run("started Span remains visible without terminal", func(t *testing.T) {
		exporter := harness.New(t)
		exportOK(t, exporter, rootStart("trace-incomplete", "root-incomplete"))
		records := harness.Records(t, exporter, "trace-incomplete")
		if len(records) != 1 || records[0].Kind != agentobs.RecordSpanStarted {
			t.Fatalf("incomplete lifecycle records = %#v", records)
		}
	})

	t.Run("Event is immutable and source must resolve", func(t *testing.T) {
		exporter := harness.New(t)
		event := eventRecord("trace-1", "root-1", "event/accepted")
		if err := exporter.Export(context.Background(), event); err == nil {
			t.Fatal("Event before source Span succeeded")
		}
		exportOK(t, exporter, rootStart("trace-1", "root-1"))
		exportOK(t, exporter, event)
		exportOK(t, exporter, event)
		conflict := event
		conflict.Attributes = []agentobs.Attribute{agentobs.String("agent.operation.status", "changed")}
		if err := exporter.Export(context.Background(), conflict); err == nil {
			t.Fatal("conflicting Event identity succeeded")
		}
		if got := len(harness.Records(t, exporter, "trace-1")); got != 2 {
			t.Fatalf("Event reconciliation record count = %d, want 2", got)
		}
	})

	t.Run("Link resolves target without changing parentage", func(t *testing.T) {
		exporter := harness.New(t)
		exportOK(t, exporter, rootStart("trace-prior", "root-prior"))
		exportOK(t, exporter, rootStart("trace-current", "root-current"))
		link := linkRecord("trace-current", "root-current", "trace-prior", "root-prior", "retried_from")
		exportOK(t, exporter, link)

		unresolved := linkRecord("trace-current", "root-current", "trace-missing", "root-missing", "missing-target")
		if err := exporter.Export(context.Background(), unresolved); err == nil {
			t.Fatal("unresolved Link target succeeded")
		}
		records := harness.Records(t, exporter, "trace-current")
		if len(records) != 2 || records[0].ParentSpanID != "" || records[1].Kind != agentobs.RecordLink {
			t.Fatalf("Link changed hierarchy or ordering: %#v", records)
		}
	})

	t.Run("schema versions round-trip", func(t *testing.T) {
		exporter := harness.New(t)
		record := rootStart("trace-version", "root-version")
		record.SchemaVersion = 7
		record.SemanticConventionVersion = 5
		record.PayloadVersion = 3
		exportOK(t, exporter, record)
		got := harness.Records(t, exporter, record.TraceID)[0]
		if got.SchemaVersion != 7 || got.SemanticConventionVersion != 5 || got.PayloadVersion != 3 {
			t.Fatalf("round-trip versions = %#v", got)
		}
	})

	t.Run("concurrent append preserves every accepted fact", func(t *testing.T) {
		exporter := harness.New(t)
		exportOK(t, exporter, rootStart("trace-concurrent", "root-concurrent"))
		const count = 32
		var group sync.WaitGroup
		group.Add(count)
		for index := 0; index < count; index++ {
			go func(index int) {
				defer group.Done()
				record := eventRecord("trace-concurrent", "root-concurrent", fmt.Sprintf("event/%d", index))
				if err := exporter.Export(context.Background(), record); err != nil {
					t.Errorf("concurrent Export %d: %v", index, err)
				}
			}(index)
		}
		group.Wait()
		if got := len(harness.Records(t, exporter, "trace-concurrent")); got != count+1 {
			t.Fatalf("concurrent record count = %d, want %d", got, count+1)
		}
	})
}

func exportOK(t *testing.T, exporter agentobs.Exporter, record agentobs.Record) {
	t.Helper()
	if err := exporter.Export(context.Background(), record); err != nil {
		t.Fatalf("Export %#v: %v", record, err)
	}
}

func rootStart(traceID agentobs.TraceID, spanID agentobs.SpanID) agentobs.Record {
	return baseRecord(traceID, spanID, "span/"+string(spanID)+"/start", agentobs.RecordSpanStarted, "agent.execution")
}

func childStart(traceID agentobs.TraceID, spanID, parentID agentobs.SpanID) agentobs.Record {
	record := baseRecord(traceID, spanID, "span/"+string(spanID)+"/start", agentobs.RecordSpanStarted, "agent.model.call")
	record.ParentSpanID = parentID
	return record
}

func spanEnd(traceID agentobs.TraceID, spanID agentobs.SpanID, name, identity string) agentobs.Record {
	record := baseRecord(traceID, spanID, identity, agentobs.RecordSpanEnded, name)
	record.Status = agentobs.StatusOK
	return record
}

func eventRecord(traceID agentobs.TraceID, spanID agentobs.SpanID, identity string) agentobs.Record {
	return baseRecord(traceID, spanID, identity, agentobs.RecordEvent, "agent.fixture.event")
}

func linkRecord(sourceTrace agentobs.TraceID, sourceSpan agentobs.SpanID, targetTrace agentobs.TraceID, targetSpan agentobs.SpanID, identity string) agentobs.Record {
	record := baseRecord(sourceTrace, sourceSpan, identity, agentobs.RecordLink, "retried_from")
	record.TargetTraceID = targetTrace
	record.TargetSpanID = targetSpan
	return record
}

func baseRecord(traceID agentobs.TraceID, spanID agentobs.SpanID, identity string, kind agentobs.RecordKind, name string) agentobs.Record {
	return agentobs.Record{
		SchemaVersion:             1,
		SemanticConventionVersion: 1,
		IdentityKey:               identity,
		Kind:                      kind,
		TraceID:                   traceID,
		SpanID:                    spanID,
		Name:                      name,
		OccurredAt:                time.Unix(1_700_000_000, 0).UTC(),
		PayloadVersion:            1,
	}
}

package otelbridge_test

import (
	"context"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/otelbridge"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestExporterBridgesHierarchyEventsAndTerminalStatus(t *testing.T) {
	collector := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(collector))
	defer provider.Shutdown(context.Background())
	exporter, err := otelbridge.New(provider.Tracer("agentobs-test"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	records := []agentobs.Record{
		record(agentobs.RecordSpanStarted, "root-start", "root", "", "agent.execution", now),
		record(agentobs.RecordSpanStarted, "child-start", "child", "root", "agent.model.call", now.Add(time.Millisecond)),
		record(agentobs.RecordEvent, "accepted", "child", "", "nano.checkpoint.accepted", now.Add(2*time.Millisecond)),
		terminalRecord("child-end", "child", "agent.model.call", agentobs.StatusOK, now.Add(3*time.Millisecond)),
		terminalRecord("root-end", "root", "agent.execution", agentobs.StatusOK, now.Add(4*time.Millisecond)),
	}
	for _, item := range records {
		if err := exporter.Export(context.Background(), item); err != nil {
			t.Fatalf("Export %s: %v", item.IdentityKey, err)
		}
	}
	spans := collector.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("OTel spans = %#v", spans)
	}
	var root, child tracetest.SpanStub
	for _, span := range spans {
		switch span.Name {
		case "agent.execution":
			root = span
		case "agent.model.call":
			child = span
		}
	}
	if !root.SpanContext.IsValid() || child.Parent.SpanID() != root.SpanContext.SpanID() || len(child.Events) != 1 || child.Events[0].Name != "nano.checkpoint.accepted" {
		t.Fatalf("bridged hierarchy root=%#v child=%#v", root, child)
	}
}

func record(kind agentobs.RecordKind, identity, spanID, parentID, name string, at time.Time) agentobs.Record {
	return agentobs.Record{SchemaVersion: 1, SemanticConventionVersion: 1, PayloadVersion: 1,
		TraceID: "durable-trace", SpanID: agentobs.SpanID(spanID), ParentSpanID: agentobs.SpanID(parentID),
		IdentityKey: identity, Kind: kind, Name: name, OccurredAt: at,
	}
}

func terminalRecord(identity, spanID, name string, status agentobs.Status, at time.Time) agentobs.Record {
	item := record(agentobs.RecordSpanEnded, identity, spanID, "", name, at)
	item.Status = status
	return item
}

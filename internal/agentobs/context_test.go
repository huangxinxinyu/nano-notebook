package agentobs

import (
	"context"
	"testing"
)

func TestSpanContextRoundTripsThroughText(t *testing.T) {
	original := SpanContext{TraceID: TraceID("trace-1"), SpanID: SpanID("span-1")}

	encoded, err := original.MarshalText()
	if err != nil {
		t.Fatalf("marshal Span Context: %v", err)
	}
	var decoded SpanContext
	if err := decoded.UnmarshalText(encoded); err != nil {
		t.Fatalf("unmarshal Span Context: %v", err)
	}
	if decoded != original {
		t.Fatalf("round trip = %#v, want %#v", decoded, original)
	}
}

func TestSpanContextRejectsUnsupportedVersion(t *testing.T) {
	var decoded SpanContext
	if err := decoded.UnmarshalText([]byte(`{"version":2,"trace_id":"trace-1","span_id":"span-1"}`)); err == nil {
		t.Fatal("expected unsupported version error")
	}
}

func TestSpanContextPropagatesThroughGoContext(t *testing.T) {
	want := SpanContext{TraceID: TraceID("trace-1"), SpanID: SpanID("span-1")}
	ctx := ContextWithSpanContext(context.Background(), want)

	got, ok := SpanContextFromContext(ctx)
	if !ok || got != want {
		t.Fatalf("Span Context = %#v, %v; want %#v, true", got, ok, want)
	}
}

func TestSpanContextDoesNotInstallInvalidIdentity(t *testing.T) {
	ctx := ContextWithSpanContext(context.Background(), SpanContext{})
	if _, ok := SpanContextFromContext(ctx); ok {
		t.Fatal("invalid Span Context was installed")
	}
}

package agentobs

import (
	"bytes"
	"math"
	"strings"
	"testing"
	"time"
)

func TestRecordValidateAcceptsRootSpanStart(t *testing.T) {
	record := Record{
		SchemaVersion:             1,
		SemanticConventionVersion: 1,
		IdentityKey:               "span:root:started",
		Kind:                      RecordSpanStarted,
		TraceID:                   TraceID("trace-1"),
		SpanID:                    SpanID("span-root"),
		Name:                      "agent.execution",
		OccurredAt:                time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
		PayloadVersion:            1,
	}

	if err := record.Validate(); err != nil {
		t.Fatalf("validate root Span start: %v", err)
	}
}

func TestRecordValidateAcceptsChildSpanStart(t *testing.T) {
	record := validRecord(RecordSpanStarted)
	record.ParentSpanID = SpanID("span-parent")

	if err := record.Validate(); err != nil {
		t.Fatalf("validate child Span start: %v", err)
	}
}

func TestRecordValidateAcceptsSpanEnd(t *testing.T) {
	record := validRecord(RecordSpanEnded)
	record.Status = StatusOK

	if err := record.Validate(); err != nil {
		t.Fatalf("validate Span end: %v", err)
	}
}

func TestRecordValidateAcceptsEvent(t *testing.T) {
	record := validRecord(RecordEvent)
	record.Name = "checkpoint.accepted"

	if err := record.Validate(); err != nil {
		t.Fatalf("validate Event: %v", err)
	}
}

func TestRecordValidateAcceptsCrossTraceLink(t *testing.T) {
	record := validRecord(RecordLink)
	record.Name = "retried_from"
	record.TargetTraceID = TraceID("trace-prior")
	record.TargetSpanID = SpanID("span-prior-root")

	if err := record.Validate(); err != nil {
		t.Fatalf("validate cross-Trace Link: %v", err)
	}
}

func TestRecordValidateRejectsKindSpecificShapeConflicts(t *testing.T) {
	tests := []struct {
		name   string
		record Record
	}{
		{
			name: "Span start with target",
			record: func() Record {
				record := validRecord(RecordSpanStarted)
				record.TargetTraceID = TraceID("target")
				return record
			}(),
		},
		{
			name:   "Span end without status",
			record: validRecord(RecordSpanEnded),
		},
		{
			name: "Event with parent",
			record: func() Record {
				record := validRecord(RecordEvent)
				record.ParentSpanID = SpanID("parent")
				return record
			}(),
		},
		{
			name: "Link without target Span",
			record: func() Record {
				record := validRecord(RecordLink)
				record.TargetTraceID = TraceID("target")
				return record
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.record.Validate(); err == nil {
				t.Fatal("expected shape validation error")
			}
		})
	}
}

func TestRecordValidateAcceptsTypedAttributes(t *testing.T) {
	record := validRecord(RecordEvent)
	record.Attributes = []Attribute{
		String("model.name", "qwen-flash"),
		Int64("token.input", 42),
		Float64("cost.usd", 0.00012),
		Bool("cost.known", true),
	}

	if err := record.Validate(); err != nil {
		t.Fatalf("validate typed attributes: %v", err)
	}
}

func TestRecordValidateRejectsInvalidAttributes(t *testing.T) {
	tests := []struct {
		name       string
		attributes []Attribute
	}{
		{name: "duplicate key", attributes: []Attribute{String("model.name", "a"), String("model.name", "b")}},
		{name: "invalid key", attributes: []Attribute{String("Model Name", "a")}},
		{name: "oversized string", attributes: []Attribute{String("model.name", strings.Repeat("a", 4097))}},
		{name: "non-finite float", attributes: []Attribute{Float64("cost.usd", math.Inf(1))}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := validRecord(RecordEvent)
			record.Attributes = tt.attributes
			if err := record.Validate(); err == nil {
				t.Fatal("expected attribute validation error")
			}
		})
	}
}

func TestRecordValidateRejectsMoreThanSixtyFourAttributes(t *testing.T) {
	record := validRecord(RecordEvent)
	for index := 0; index < 65; index++ {
		record.Attributes = append(record.Attributes, Int64("value."+strings.Repeat("x", index+1), int64(index)))
	}

	if err := record.Validate(); err == nil {
		t.Fatal("expected attribute count validation error")
	}
}

func TestCanonicalPayloadDoesNotDependOnAttributeOrder(t *testing.T) {
	first := validRecord(RecordSpanEnded)
	first.Status = StatusOK
	first.Attributes = []Attribute{String("model.name", "model-a"), Int64("token.total", 12)}
	second := first
	second.Attributes = []Attribute{Int64("token.total", 12), String("model.name", "model-a")}

	firstPayload, err := first.CanonicalPayload()
	if err != nil {
		t.Fatalf("first CanonicalPayload: %v", err)
	}
	secondPayload, err := second.CanonicalPayload()
	if err != nil {
		t.Fatalf("second CanonicalPayload: %v", err)
	}
	if !bytes.Equal(firstPayload, secondPayload) {
		t.Fatalf("canonical payloads differ:\n%s\n%s", firstPayload, secondPayload)
	}
	if !bytes.Contains(firstPayload, []byte(`"semantic_convention_version":1`)) || !bytes.Contains(firstPayload, []byte(`"status":"ok"`)) {
		t.Fatalf("canonical payload omits version or status: %s", firstPayload)
	}
}

func TestCanonicalRecordHashCoversFactButNormalizesAttributes(t *testing.T) {
	first := validRecord(RecordEvent)
	first.Attributes = []Attribute{String("model.name", "model-a"), Int64("token.total", 12)}
	second := first
	second.Attributes = []Attribute{Int64("token.total", 12), String("model.name", "model-a")}

	firstHash, err := first.CanonicalHash()
	if err != nil {
		t.Fatalf("first CanonicalHash: %v", err)
	}
	secondHash, err := second.CanonicalHash()
	if err != nil {
		t.Fatalf("second CanonicalHash: %v", err)
	}
	if firstHash != secondHash {
		t.Fatalf("hash depends on attribute order: %x != %x", firstHash, secondHash)
	}
	second.OccurredAt = second.OccurredAt.Add(time.Nanosecond)
	changedHash, err := second.CanonicalHash()
	if err != nil {
		t.Fatalf("changed CanonicalHash: %v", err)
	}
	if changedHash == firstHash {
		t.Fatal("hash ignored changed fact timestamp")
	}
}

func TestRecordPayloadLimitIsConfigurable(t *testing.T) {
	record := validRecord(RecordEvent)
	for index := 0; index < 4; index++ {
		record.Attributes = append(record.Attributes, String("payload.part"+string(rune('a'+index)), strings.Repeat("x", 4096)))
	}
	if err := record.Validate(); err == nil {
		t.Fatal("default 16 KiB payload limit accepted oversized record")
	}

	limits := DefaultLimits()
	limits.MaxPayloadBytes = 32 * 1024
	if err := record.ValidateWithLimits(limits); err != nil {
		t.Fatalf("custom payload limit rejected record: %v", err)
	}
}

func validRecord(kind RecordKind) Record {
	return Record{
		SchemaVersion:             1,
		SemanticConventionVersion: 1,
		IdentityKey:               "record:1",
		Kind:                      kind,
		TraceID:                   TraceID("trace-1"),
		SpanID:                    SpanID("span-1"),
		Name:                      "agent.operation",
		OccurredAt:                time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
		PayloadVersion:            1,
	}
}

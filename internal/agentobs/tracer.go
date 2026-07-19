package agentobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type IDGenerator interface {
	NewTraceID() TraceID
	NewSpanID() SpanID
}

// IdentitySpanIDGenerator lets a Recorder make a semantic Span identity stable
// across processes without requiring a shared Trace-record database.
type IdentitySpanIDGenerator interface {
	SpanIDForIdentity(TraceID, string) SpanID
}

type TracerConfig struct {
	Recorder                  Recorder
	IDGenerator               IDGenerator
	IdentitySpanIDGenerator   IdentitySpanIDGenerator
	Clock                     func() time.Time
	SchemaVersion             int
	SemanticConventionVersion int
	PayloadVersion            int
	RecordLimits              *Limits
}

type TraceStart struct {
	TraceID     TraceID
	SpanID      SpanID
	IdentityKey string
	Name        string
	OccurredAt  time.Time
	Attributes  []Attribute
}

type SpanStart struct {
	SpanID      SpanID
	IdentityKey string
	Name        string
	OccurredAt  time.Time
	Attributes  []Attribute
}

type SpanEnd struct {
	IdentityKey string
	Name        string
	Status      Status
	OccurredAt  time.Time
	Attributes  []Attribute
}

type Event struct {
	IdentityKey string
	Name        string
	OccurredAt  time.Time
	Attributes  []Attribute
}

type Link struct {
	IdentityKey string
	Name        string
	Target      SpanContext
	OccurredAt  time.Time
	Attributes  []Attribute
}

type Tracer struct {
	recorder                  Recorder
	ids                       IDGenerator
	identityIDs               IdentitySpanIDGenerator
	now                       func() time.Time
	schemaVersion             int
	semanticConventionVersion int
	payloadVersion            int
	recordLimits              Limits
}

func NewTracer(config TracerConfig) (*Tracer, error) {
	if config.Recorder == nil {
		return nil, errors.New("Tracer requires a Recorder")
	}
	if config.SchemaVersion < 0 || config.SemanticConventionVersion < 0 || config.PayloadVersion < 0 {
		return nil, errors.New("Tracer versions cannot be negative")
	}
	ids := config.IDGenerator
	if ids == nil {
		ids = uuidIDs{}
	}
	now := config.Clock
	if now == nil {
		now = time.Now
	}
	recordLimits := DefaultLimits()
	if config.RecordLimits != nil {
		recordLimits = *config.RecordLimits
		if err := recordLimits.Validate(); err != nil {
			return nil, err
		}
	}
	identityIDs := config.IdentitySpanIDGenerator
	if identityIDs == nil {
		identityIDs = identitySpanIDs(config.Recorder)
	}
	return &Tracer{
		recorder:                  config.Recorder,
		ids:                       ids,
		identityIDs:               identityIDs,
		now:                       now,
		schemaVersion:             defaultVersion(config.SchemaVersion),
		semanticConventionVersion: defaultVersion(config.SemanticConventionVersion),
		payloadVersion:            defaultVersion(config.PayloadVersion),
		recordLimits:              recordLimits,
	}, nil
}

func (t *Tracer) StartTrace(ctx context.Context, start TraceStart) (context.Context, SpanContext, error) {
	ctx = nonNilContext(ctx)
	traceID := start.TraceID
	if traceID == "" {
		traceID = t.ids.NewTraceID()
	}
	spanID := start.SpanID
	if spanID == "" {
		if t.identityIDs != nil && strings.TrimSpace(start.IdentityKey) != "" {
			spanID = t.identityIDs.SpanIDForIdentity(traceID, strings.TrimSpace(start.IdentityKey))
		} else {
			spanID = t.ids.NewSpanID()
		}
	}
	span := SpanContext{TraceID: traceID, SpanID: spanID}
	record := t.baseRecord(start.IdentityKey, RecordSpanStarted, span, start.Name, start.OccurredAt, start.Attributes)
	if record.IdentityKey == "" {
		record.IdentityKey = spanStartIdentity(span.SpanID)
	}
	if err := t.record(ctx, record); err != nil {
		return ctx, SpanContext{}, err
	}
	return ContextWithSpanContext(ctx, span), span, nil
}

func (t *Tracer) StartSpan(ctx context.Context, start SpanStart) (context.Context, SpanContext, error) {
	ctx = nonNilContext(ctx)
	parent, ok := SpanContextFromContext(ctx)
	if !ok {
		return ctx, SpanContext{}, errors.New("child Span requires a parent Span Context")
	}
	spanID := start.SpanID
	if spanID == "" {
		if t.identityIDs != nil && strings.TrimSpace(start.IdentityKey) != "" {
			spanID = t.identityIDs.SpanIDForIdentity(parent.TraceID, strings.TrimSpace(start.IdentityKey))
		} else {
			spanID = t.ids.NewSpanID()
		}
	}
	span := SpanContext{TraceID: parent.TraceID, SpanID: spanID}
	record := t.baseRecord(start.IdentityKey, RecordSpanStarted, span, start.Name, start.OccurredAt, start.Attributes)
	record.ParentSpanID = parent.SpanID
	if record.IdentityKey == "" {
		record.IdentityKey = spanStartIdentity(span.SpanID)
	}
	if err := t.record(ctx, record); err != nil {
		return ctx, SpanContext{}, err
	}
	return ContextWithSpanContext(ctx, span), span, nil
}

func (t *Tracer) EndSpan(ctx context.Context, end SpanEnd) error {
	ctx = nonNilContext(ctx)
	span, err := requiredSpanContext(ctx)
	if err != nil {
		return err
	}
	record := t.baseRecord(end.IdentityKey, RecordSpanEnded, span, end.Name, end.OccurredAt, end.Attributes)
	record.Status = end.Status
	if record.IdentityKey == "" {
		record.IdentityKey = fmt.Sprintf("span/%s/end", span.SpanID)
	}
	return t.record(ctx, record)
}

func (t *Tracer) Event(ctx context.Context, event Event) error {
	ctx = nonNilContext(ctx)
	span, err := requiredSpanContext(ctx)
	if err != nil {
		return err
	}
	record := t.baseRecord(event.IdentityKey, RecordEvent, span, event.Name, event.OccurredAt, event.Attributes)
	return t.record(ctx, record)
}

func (t *Tracer) Link(ctx context.Context, link Link) error {
	ctx = nonNilContext(ctx)
	span, err := requiredSpanContext(ctx)
	if err != nil {
		return err
	}
	if err := link.Target.Validate(); err != nil {
		return fmt.Errorf("Link target: %w", err)
	}
	record := t.baseRecord(link.IdentityKey, RecordLink, span, link.Name, link.OccurredAt, link.Attributes)
	record.TargetTraceID = link.Target.TraceID
	record.TargetSpanID = link.Target.SpanID
	return t.record(ctx, record)
}

func (t *Tracer) record(ctx context.Context, record Record) error {
	if err := record.ValidateWithLimits(t.recordLimits); err != nil {
		return err
	}
	return t.recorder.Record(ctx, record)
}

func (t *Tracer) baseRecord(identityKey string, kind RecordKind, span SpanContext, name string, occurredAt time.Time, attributes []Attribute) Record {
	if occurredAt.IsZero() {
		occurredAt = t.now()
	}
	return Record{
		SchemaVersion:             t.schemaVersion,
		SemanticConventionVersion: t.semanticConventionVersion,
		IdentityKey:               strings.TrimSpace(identityKey),
		Kind:                      kind,
		TraceID:                   span.TraceID,
		SpanID:                    span.SpanID,
		Name:                      name,
		OccurredAt:                occurredAt,
		PayloadVersion:            t.payloadVersion,
		Attributes:                append([]Attribute(nil), attributes...),
	}
}

func requiredSpanContext(ctx context.Context) (SpanContext, error) {
	span, ok := SpanContextFromContext(ctx)
	if !ok {
		return SpanContext{}, errors.New("operation requires a Span Context")
	}
	return span, nil
}

func spanStartIdentity(spanID SpanID) string {
	return fmt.Sprintf("span/%s/start", spanID)
}

func defaultVersion(version int) int {
	if version == 0 {
		return 1
	}
	return version
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func identitySpanIDs(recorder Recorder) IdentitySpanIDGenerator {
	ids, _ := recorder.(IdentitySpanIDGenerator)
	return ids
}

type uuidIDs struct{}

func (uuidIDs) NewTraceID() TraceID {
	return TraceID(uuid.NewString())
}

func (uuidIDs) NewSpanID() SpanID {
	return SpanID(uuid.NewString())
}

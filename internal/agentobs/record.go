package agentobs

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const DefaultMaxPayloadBytes = 16 * 1024

type Limits struct {
	MaxAttributes           int
	MaxAttributeStringBytes int
	MaxPayloadBytes         int
}

func DefaultLimits() Limits {
	return Limits{
		MaxAttributes:           DefaultMaxAttributes,
		MaxAttributeStringBytes: DefaultMaxAttributeStringBytes,
		MaxPayloadBytes:         DefaultMaxPayloadBytes,
	}
}

func (l Limits) Validate() error {
	if l.MaxAttributes < 1 || l.MaxAttributeStringBytes < 1 || l.MaxPayloadBytes < 1 {
		return errors.New("record limits must be positive")
	}
	return nil
}

type TraceID string

type SpanID string

type RecordKind string

const (
	RecordSpanStarted RecordKind = "span_started"
	RecordSpanEnded   RecordKind = "span_ended"
	RecordEvent       RecordKind = "event"
	RecordLink        RecordKind = "link"
)

type Status string

const (
	StatusOK        Status = "ok"
	StatusError     Status = "error"
	StatusCancelled Status = "cancelled"
)

type Record struct {
	SchemaVersion             int
	SemanticConventionVersion int
	IdentityKey               string
	Kind                      RecordKind
	TraceID                   TraceID
	SpanID                    SpanID
	ParentSpanID              SpanID
	TargetTraceID             TraceID
	TargetSpanID              SpanID
	Name                      string
	Status                    Status
	OccurredAt                time.Time
	PayloadVersion            int
	Attributes                []Attribute
}

func (r Record) Validate() error {
	return r.ValidateWithLimits(DefaultLimits())
}

func (r Record) ValidateWithLimits(limits Limits) error {
	if err := limits.Validate(); err != nil {
		return err
	}
	if r.SchemaVersion < 1 || r.SemanticConventionVersion < 1 || r.PayloadVersion < 1 {
		return errors.New("record versions must be positive")
	}
	if strings.TrimSpace(r.IdentityKey) == "" || strings.TrimSpace(string(r.TraceID)) == "" || strings.TrimSpace(string(r.SpanID)) == "" {
		return errors.New("record identity is incomplete")
	}
	if strings.TrimSpace(r.Name) == "" || r.OccurredAt.IsZero() {
		return errors.New("record fact is incomplete")
	}
	if err := validateAttributes(r.Attributes, limits); err != nil {
		return err
	}
	hasParent := strings.TrimSpace(string(r.ParentSpanID)) != ""
	hasTargetTrace := strings.TrimSpace(string(r.TargetTraceID)) != ""
	hasTargetSpan := strings.TrimSpace(string(r.TargetSpanID)) != ""
	switch r.Kind {
	case RecordSpanStarted:
		if hasTargetTrace || hasTargetSpan || r.Status != "" {
			return errors.New("Span start has conflicting fields")
		}
	case RecordSpanEnded:
		if hasParent || hasTargetTrace || hasTargetSpan || !r.Status.validTerminal() {
			return errors.New("Span end has conflicting fields")
		}
	case RecordEvent:
		if hasParent || hasTargetTrace || hasTargetSpan || r.Status != "" {
			return errors.New("Event has conflicting fields")
		}
	case RecordLink:
		if hasParent || !hasTargetTrace || !hasTargetSpan || r.Status != "" {
			return errors.New("Link has conflicting fields")
		}
	default:
		return errors.New("unsupported record kind")
	}
	payload, err := r.canonicalPayload()
	if err != nil {
		return fmt.Errorf("encode record payload: %w", err)
	}
	if len(payload) > limits.MaxPayloadBytes {
		return errors.New("record payload is too large")
	}
	return nil
}

func (r Record) CanonicalPayload() ([]byte, error) {
	return r.CanonicalPayloadWithLimits(DefaultLimits())
}

func (r Record) CanonicalPayloadWithLimits(limits Limits) ([]byte, error) {
	if err := r.ValidateWithLimits(limits); err != nil {
		return nil, err
	}
	return r.canonicalPayload()
}

func (r Record) CanonicalHash() ([sha256.Size]byte, error) {
	return r.CanonicalHashWithLimits(DefaultLimits())
}

func (r Record) CanonicalHashWithLimits(limits Limits) ([sha256.Size]byte, error) {
	if err := r.ValidateWithLimits(limits); err != nil {
		return [sha256.Size]byte{}, err
	}
	payload, err := r.canonicalPayload()
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	encoded, err := json.Marshal(struct {
		SchemaVersion  int             `json:"schema_version"`
		IdentityKey    string          `json:"identity_key"`
		Kind           RecordKind      `json:"kind"`
		TraceID        TraceID         `json:"trace_id"`
		SpanID         SpanID          `json:"span_id"`
		ParentSpanID   SpanID          `json:"parent_span_id,omitempty"`
		TargetTraceID  TraceID         `json:"target_trace_id,omitempty"`
		TargetSpanID   SpanID          `json:"target_span_id,omitempty"`
		Name           string          `json:"name"`
		OccurredAt     string          `json:"occurred_at"`
		PayloadVersion int             `json:"payload_version"`
		Payload        json.RawMessage `json:"payload"`
	}{
		SchemaVersion:  r.SchemaVersion,
		IdentityKey:    r.IdentityKey,
		Kind:           r.Kind,
		TraceID:        r.TraceID,
		SpanID:         r.SpanID,
		ParentSpanID:   r.ParentSpanID,
		TargetTraceID:  r.TargetTraceID,
		TargetSpanID:   r.TargetSpanID,
		Name:           r.Name,
		OccurredAt:     r.OccurredAt.UTC().Format(time.RFC3339Nano),
		PayloadVersion: r.PayloadVersion,
		Payload:        payload,
	})
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

func (r Record) canonicalPayload() ([]byte, error) {
	attributes := append([]Attribute(nil), r.Attributes...)
	sort.Slice(attributes, func(i, j int) bool { return attributes[i].Key < attributes[j].Key })
	canonicalAttributes := make([]canonicalAttribute, 0, len(attributes))
	for _, attribute := range attributes {
		canonical := canonicalAttribute{Key: attribute.Key, Kind: attribute.Value.Kind}
		switch attribute.Value.Kind {
		case ValueString:
			value := attribute.Value.String
			canonical.String = &value
		case ValueInt64:
			value := attribute.Value.Int64
			canonical.Int64 = &value
		case ValueFloat64:
			value := attribute.Value.Float64
			canonical.Float64 = &value
		case ValueBool:
			value := attribute.Value.Bool
			canonical.Bool = &value
		}
		canonicalAttributes = append(canonicalAttributes, canonical)
	}
	return json.Marshal(struct {
		SemanticConventionVersion int                  `json:"semantic_convention_version"`
		Status                    Status               `json:"status,omitempty"`
		Attributes                []canonicalAttribute `json:"attributes"`
	}{
		SemanticConventionVersion: r.SemanticConventionVersion,
		Status:                    r.Status,
		Attributes:                canonicalAttributes,
	})
}

type canonicalAttribute struct {
	Key     string    `json:"key"`
	Kind    ValueKind `json:"kind"`
	String  *string   `json:"string,omitempty"`
	Int64   *int64    `json:"int64,omitempty"`
	Float64 *float64  `json:"float64,omitempty"`
	Bool    *bool     `json:"bool,omitempty"`
}

func (s Status) validTerminal() bool {
	return s == StatusOK || s == StatusError || s == StatusCancelled
}

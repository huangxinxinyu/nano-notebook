package collector

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
)

type wireSequencedRecord struct {
	Sequence         int                 `json:"sequence"`
	SchemaVersion    int                 `json:"schema_version"`
	IdentityKey      string              `json:"identity_key"`
	Kind             agentobs.RecordKind `json:"kind"`
	TraceID          agentobs.TraceID    `json:"trace_id"`
	SpanID           agentobs.SpanID     `json:"span_id"`
	ParentSpanID     agentobs.SpanID     `json:"parent_span_id,omitempty"`
	TargetTraceID    agentobs.TraceID    `json:"target_trace_id,omitempty"`
	TargetSpanID     agentobs.SpanID     `json:"target_span_id,omitempty"`
	Name             string              `json:"name"`
	OccurredAt       string              `json:"occurred_at"`
	PayloadVersion   int                 `json:"payload_version"`
	CanonicalPayload json.RawMessage     `json:"canonical_payload"`
	CanonicalSHA256  string              `json:"canonical_sha256"`
}

func (r SequencedRecord) MarshalJSON() ([]byte, error) {
	payload, err := r.Record.CanonicalPayload()
	if err != nil {
		return nil, err
	}
	return json.Marshal(wireSequencedRecord{
		Sequence: r.Sequence, SchemaVersion: r.Record.SchemaVersion,
		IdentityKey: r.Record.IdentityKey, Kind: r.Record.Kind, TraceID: r.Record.TraceID,
		SpanID: r.Record.SpanID, ParentSpanID: r.Record.ParentSpanID,
		TargetTraceID: r.Record.TargetTraceID, TargetSpanID: r.Record.TargetSpanID,
		Name: r.Record.Name, OccurredAt: r.Record.OccurredAt.UTC().Format(time.RFC3339Nano),
		PayloadVersion: r.Record.PayloadVersion, CanonicalPayload: payload,
		CanonicalSHA256: r.CanonicalSHA256,
	})
}

func (r *SequencedRecord) UnmarshalJSON(encoded []byte) error {
	if r == nil {
		return errors.New("nil Collector Sequenced Record")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var wire wireSequencedRecord
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("Collector Sequenced Record has trailing data")
	}
	payload, err := agentobs.DecodeCanonicalPayload(wire.CanonicalPayload)
	if err != nil {
		return err
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, wire.OccurredAt)
	if err != nil {
		return err
	}
	record := agentobs.Record{
		SchemaVersion: wire.SchemaVersion, SemanticConventionVersion: payload.SemanticConventionVersion,
		IdentityKey: wire.IdentityKey, Kind: wire.Kind, TraceID: wire.TraceID, SpanID: wire.SpanID,
		ParentSpanID: wire.ParentSpanID, TargetTraceID: wire.TargetTraceID, TargetSpanID: wire.TargetSpanID,
		Name: wire.Name, Status: payload.Status, OccurredAt: occurredAt,
		PayloadVersion: wire.PayloadVersion, Attributes: payload.Attributes,
	}
	if err := record.Validate(); err != nil {
		return err
	}
	*r = SequencedRecord{Sequence: wire.Sequence, Record: record, CanonicalSHA256: wire.CanonicalSHA256}
	return nil
}

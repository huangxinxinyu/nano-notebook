package collector

import (
	"context"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
)

const ProtocolVersion = 1

const (
	SupportedRecordSchemaVersion = 1
	SupportedSemanticConvention  = 1
)

type ChunkStatus string

const (
	ChunkCommitted ChunkStatus = "committed"
	ChunkRejected  ChunkStatus = "rejected"
	ChunkRetryable ChunkStatus = "retryable"
)

const (
	CodeIdentityConflict  = "identity_conflict"
	CodeCanonicalHash     = "canonical_hash_mismatch"
	CodeInvalidChunk      = "invalid_chunk"
	CodeInvalidLifecycle  = "invalid_lifecycle"
	CodeSequenceGap       = "sequence_gap"
	CodeTombstoned        = "tombstoned"
	CodeUnsupportedSchema = "unsupported_schema"
)

type Batch struct {
	ProtocolVersion int          `json:"protocol_version"`
	BatchID         string       `json:"batch_id"`
	ProducerID      string       `json:"producer_id"`
	CreatedAt       time.Time    `json:"created_at"`
	Chunks          []TraceChunk `json:"chunks"`
}

type TraceDescriptor struct {
	TraceID                   agentobs.TraceID `json:"trace_id"`
	RunID                     string           `json:"run_id"`
	ChatID                    string           `json:"chat_id"`
	NotebookID                string           `json:"notebook_id"`
	RootSpanID                agentobs.SpanID  `json:"root_span_id"`
	AgentName                 string           `json:"agent_name"`
	SchemaVersion             int              `json:"schema_version"`
	SemanticConventionVersion int              `json:"semantic_convention_version"`
}

type TraceChunk struct {
	Trace         TraceDescriptor   `json:"trace"`
	FirstSequence int               `json:"first_sequence"`
	Records       []SequencedRecord `json:"records"`
}

type SequencedRecord struct {
	Sequence        int             `json:"-"`
	Record          agentobs.Record `json:"-"`
	CanonicalSHA256 string          `json:"-"`
}

type BatchResult struct {
	BatchID string        `json:"batch_id"`
	Chunks  []ChunkResult `json:"chunks"`
}

type ChunkResult struct {
	TraceID          agentobs.TraceID `json:"trace_id"`
	Status           ChunkStatus      `json:"status"`
	CommittedThrough int              `json:"committed_through"`
	Code             string           `json:"code,omitempty"`
}

type ChunkError struct {
	Code             string
	CommittedThrough int
	Retryable        bool
	Err              error
}

func (e *ChunkError) Error() string {
	if e == nil || e.Err == nil {
		return "Collector Trace Chunk rejected"
	}
	return e.Err.Error()
}

func (e *ChunkError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type Store interface {
	CommitTraceChunk(context.Context, TraceChunk) (int, error)
}

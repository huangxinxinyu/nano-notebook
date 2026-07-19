package collector

import (
	"context"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/replay"
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
	CodeIdentityConflict      = "identity_conflict"
	CodeCanonicalHash         = "canonical_hash_mismatch"
	CodeInvalidChunk          = "invalid_chunk"
	CodeInvalidLifecycle      = "invalid_lifecycle"
	CodeSequenceGap           = "sequence_gap"
	CodeDependencyMissing     = "dependency_missing"
	CodeTombstoned            = "tombstoned"
	CodeUnsupportedSchema     = "unsupported_schema"
	CodeAttachmentUnavailable = "attachment_unavailable"
	CodeAttachmentIntegrity   = "attachment_integrity"
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
	Trace         TraceDescriptor        `json:"trace"`
	FirstSequence int                    `json:"first_sequence"`
	Records       []SequencedRecord      `json:"records"`
	Attachments   []AttachmentDescriptor `json:"attachments,omitempty"`
}

type AttachmentDescriptor struct {
	AttachmentID     string       `json:"attachment_id"`
	RecordSequence   int          `json:"record_sequence"`
	Class            replay.Class `json:"class"`
	SchemaVersion    int          `json:"schema_version"`
	PlaintextSHA256  string       `json:"plaintext_sha256"`
	StagingObjectKey string       `json:"staging_object_key"`
	CiphertextBytes  int          `json:"ciphertext_bytes"`
	CiphertextSHA256 string       `json:"ciphertext_sha256"`
	Compression      string       `json:"compression"`
	Encryption       string       `json:"encryption"`
	KeyID            string       `json:"key_id"`
	WrappedKey       []byte       `json:"wrapped_key"`
	Nonce            []byte       `json:"nonce"`
	ExpiresAt        time.Time    `json:"expires_at"`
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

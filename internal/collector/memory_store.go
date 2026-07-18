package collector

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/memory"
)

type MemoryStore struct {
	mu     sync.RWMutex
	traces map[agentobs.TraceID]memoryTrace
}

type memoryTrace struct {
	descriptor TraceDescriptor
	records    []SequencedRecord
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{traces: make(map[agentobs.TraceID]memoryTrace)}
}

func (s *MemoryStore) CommitTraceChunk(_ context.Context, chunk TraceChunk) (int, error) {
	if s == nil {
		return 0, errors.New("nil Collector Memory Store")
	}
	if err := validateTraceDescriptor(chunk.Trace); err != nil {
		return 0, &ChunkError{Code: CodeInvalidChunk, Err: err}
	}
	if chunk.FirstSequence < 1 || len(chunk.Records) == 0 {
		return 0, &ChunkError{Code: CodeInvalidChunk, Err: errors.New("Collector Trace Chunk is empty or unsequenced")}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	existing := s.traces[chunk.Trace.TraceID]
	if len(existing.records) > 0 && existing.descriptor != chunk.Trace {
		return 0, &ChunkError{
			Code: CodeIdentityConflict, CommittedThrough: len(existing.records),
			Err: errors.New("Collector Trace descriptor changed"),
		}
	}
	if chunk.FirstSequence > len(existing.records)+1 {
		return 0, &ChunkError{
			Code: CodeSequenceGap, CommittedThrough: len(existing.records), Retryable: true,
			Err: errors.New("Collector Trace Chunk sequence is not contiguous"),
		}
	}

	validator := memory.New()
	for _, stored := range existing.records {
		if err := validator.Export(context.Background(), stored.Record); err != nil {
			return 0, fmt.Errorf("validate stored Collector record: %w", err)
		}
	}
	candidate := append([]SequencedRecord(nil), existing.records...)
	for index, envelope := range chunk.Records {
		sequence := chunk.FirstSequence + index
		if envelope.Sequence != sequence || envelope.Record.TraceID != chunk.Trace.TraceID || envelope.Record.SchemaVersion != chunk.Trace.SchemaVersion || envelope.Record.SemanticConventionVersion != chunk.Trace.SemanticConventionVersion {
			return 0, &ChunkError{
				Code: CodeInvalidChunk, CommittedThrough: len(existing.records),
				Err: errors.New("Collector record changed its Trace envelope"),
			}
		}
		hash, err := envelope.Record.CanonicalHash()
		if err != nil {
			return 0, &ChunkError{Code: CodeInvalidChunk, CommittedThrough: len(existing.records), Err: err}
		}
		if envelope.CanonicalSHA256 != hex.EncodeToString(hash[:]) {
			return 0, &ChunkError{
				Code: CodeCanonicalHash, CommittedThrough: len(existing.records), Err: agentobs.ErrIdentityConflict,
			}
		}
		if sequence <= len(existing.records) {
			stored := existing.records[sequence-1]
			if stored.CanonicalSHA256 != envelope.CanonicalSHA256 {
				return 0, &ChunkError{
					Code: CodeIdentityConflict, CommittedThrough: len(existing.records), Err: agentobs.ErrIdentityConflict,
				}
			}
			continue
		}
		if sequence != len(candidate)+1 {
			return 0, &ChunkError{
				Code: CodeSequenceGap, CommittedThrough: len(existing.records), Retryable: true,
				Err: errors.New("Collector Trace Chunk sequence is not contiguous"),
			}
		}
		if sequence == 1 && (envelope.Record.Kind != agentobs.RecordSpanStarted || envelope.Record.SpanID != chunk.Trace.RootSpanID || envelope.Record.ParentSpanID != "") {
			return 0, &ChunkError{
				Code: CodeInvalidLifecycle, CommittedThrough: len(existing.records),
				Err: fmt.Errorf("%w: first Collector record is not the Trace root", agentobs.ErrLifecycle),
			}
		}
		if err := validator.Export(context.Background(), envelope.Record); err != nil {
			if errors.Is(err, agentobs.ErrLifecycle) || errors.Is(err, agentobs.ErrUnresolvedLink) || errors.Is(err, agentobs.ErrLimitExceeded) {
				return 0, &ChunkError{Code: CodeInvalidLifecycle, CommittedThrough: len(existing.records), Err: err}
			}
			return 0, err
		}
		candidate = append(candidate, cloneSequencedRecord(envelope))
	}
	s.traces[chunk.Trace.TraceID] = memoryTrace{descriptor: chunk.Trace, records: candidate}
	return candidate[len(candidate)-1].Sequence, nil
}

func (s *MemoryStore) Records(traceID agentobs.TraceID) []SequencedRecord {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	records := s.traces[traceID].records
	result := make([]SequencedRecord, len(records))
	for index, record := range records {
		result[index] = cloneSequencedRecord(record)
	}
	return result
}

func validateTraceDescriptor(trace TraceDescriptor) error {
	if strings.TrimSpace(string(trace.TraceID)) == "" || strings.TrimSpace(trace.RunID) == "" || strings.TrimSpace(trace.ChatID) == "" || strings.TrimSpace(trace.NotebookID) == "" || strings.TrimSpace(string(trace.RootSpanID)) == "" || strings.TrimSpace(trace.AgentName) == "" || trace.SchemaVersion < 1 || trace.SemanticConventionVersion < 1 {
		return errors.New("Collector Trace descriptor is incomplete")
	}
	return nil
}

func cloneSequencedRecord(envelope SequencedRecord) SequencedRecord {
	envelope.Record.Attributes = append([]agentobs.Attribute(nil), envelope.Record.Attributes...)
	return envelope
}

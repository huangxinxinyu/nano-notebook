package collector

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/memory"
	"github.com/huangxinxinyu/nano-notebook/internal/replay"
)

type MemoryStore struct {
	mu            sync.RWMutex
	traces        map[agentobs.TraceID]memoryTrace
	tombstones    map[agentobs.TraceID]PurgeCommand
	purgeCommands map[string]PurgeCommand
}

type memoryTrace struct {
	descriptor  TraceDescriptor
	records     []SequencedRecord
	attachments map[string]AttachmentDescriptor
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		traces: make(map[agentobs.TraceID]memoryTrace), tombstones: make(map[agentobs.TraceID]PurgeCommand),
		purgeCommands: make(map[string]PurgeCommand),
	}
}

func (s *MemoryStore) CommitTraceChunk(ctx context.Context, chunk TraceChunk) (int, error) {
	if s == nil {
		return 0, errors.New("nil Collector Memory Store")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	trace, err := CanonicalTraceDescriptor(chunk.Trace)
	if err != nil {
		return 0, &ChunkError{Code: CodeInvalidChunk, Err: err}
	}
	chunk.Trace = trace
	if _, tombstoned := s.tombstones[chunk.Trace.TraceID]; tombstoned {
		return 0, &ChunkError{Code: CodeTombstoned, Err: errors.New("Collector Trace is tombstoned")}
	}
	merged, committedThrough, err := validateAndMergeTraceChunk(ctx, s.traces[chunk.Trace.TraceID], chunk, func(_ context.Context, traceID agentobs.TraceID, spanID agentobs.SpanID) (bool, error) {
		return memoryTraceHasSpan(s.traces[traceID], spanID), nil
	})
	if err != nil {
		return 0, err
	}
	s.traces[chunk.Trace.TraceID] = merged
	return committedThrough, nil
}

func (s *MemoryStore) TombstoneTrace(_ context.Context, command PurgeCommand) error {
	if s == nil {
		return errors.New("nil Collector Memory Store")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, exists := s.purgeCommands[command.CommandID]; exists {
		if existing != command {
			return &PurgeCommandError{Code: CodeIdentityConflict, Err: errors.New("Collector purge command identity changed")}
		}
		return nil
	}
	if _, exists := s.tombstones[command.TraceID]; exists {
		s.purgeCommands[command.CommandID] = command
		return nil
	}
	s.tombstones[command.TraceID] = command
	s.purgeCommands[command.CommandID] = command
	return nil
}

type linkDependencyResolver func(context.Context, agentobs.TraceID, agentobs.SpanID) (bool, error)

type linkTarget struct {
	traceID agentobs.TraceID
	spanID  agentobs.SpanID
}

func validateAndMergeTraceChunk(ctx context.Context, existing memoryTrace, chunk TraceChunk, resolveDependency linkDependencyResolver) (memoryTrace, int, error) {
	if err := validateTraceDescriptor(chunk.Trace); err != nil {
		return memoryTrace{}, 0, &ChunkError{Code: CodeInvalidChunk, Err: err}
	}
	collectorSequence := chunk.SequenceAuthority == SequenceAuthorityCollector
	if (!collectorSequence && chunk.FirstSequence < 1) || (collectorSequence && chunk.FirstSequence != 0) || len(chunk.Records) == 0 {
		return memoryTrace{}, 0, &ChunkError{Code: CodeInvalidChunk, Err: errors.New("Collector Trace Chunk is empty or unsequenced")}
	}
	var err error
	chunk, err = resolveDirectAttachmentSequences(existing.records, chunk)
	if err != nil {
		return memoryTrace{}, 0, &ChunkError{Code: CodeInvalidChunk, CommittedThrough: len(existing.records), Err: err}
	}
	if err := validateAttachmentDescriptors(chunk); err != nil {
		return memoryTrace{}, 0, &ChunkError{Code: CodeInvalidChunk, CommittedThrough: len(existing.records), Err: err}
	}
	for _, attachment := range chunk.Attachments {
		stored, exists := existing.attachments[attachment.AttachmentID]
		if exists && !sameAttachmentIdentity(stored, attachment) {
			return memoryTrace{}, 0, &ChunkError{
				Code: CodeIdentityConflict, CommittedThrough: len(existing.records),
				Err: errors.New("Collector Replay Attachment identity changed"),
			}
		}
	}
	if len(existing.records) > 0 && existing.descriptor != chunk.Trace {
		return memoryTrace{}, 0, &ChunkError{
			Code: CodeIdentityConflict, CommittedThrough: len(existing.records),
			Err: errors.New("Collector Trace descriptor changed"),
		}
	}
	if !collectorSequence && chunk.FirstSequence > len(existing.records)+1 {
		return memoryTrace{}, 0, &ChunkError{
			Code: CodeSequenceGap, CommittedThrough: len(existing.records), Retryable: true,
			Err: errors.New("Collector Trace Chunk sequence is not contiguous"),
		}
	}

	resolvedLinks := make(map[linkTarget]struct{})
	for _, stored := range existing.records {
		if stored.Record.Kind == agentobs.RecordLink && stored.Record.TargetTraceID != chunk.Trace.TraceID {
			resolvedLinks[linkTarget{traceID: stored.Record.TargetTraceID, spanID: stored.Record.TargetSpanID}] = struct{}{}
		}
	}
	validator, err := memory.NewWithConfig(memory.Config{ResolveLink: func(traceID agentobs.TraceID, spanID agentobs.SpanID) bool {
		_, found := resolvedLinks[linkTarget{traceID: traceID, spanID: spanID}]
		return found
	}})
	if err != nil {
		return memoryTrace{}, 0, err
	}
	for _, stored := range existing.records {
		if err := validator.Export(ctx, stored.Record); err != nil {
			if collectorSequence && (errors.Is(err, agentobs.ErrLifecycle) || errors.Is(err, agentobs.ErrUnresolvedLink)) {
				continue
			}
			return memoryTrace{}, 0, fmt.Errorf("validate stored Collector record: %w", err)
		}
	}
	candidate := append([]SequencedRecord(nil), existing.records...)
	byIdentity := make(map[string]SequencedRecord, len(existing.records)+len(chunk.Records))
	for _, stored := range existing.records {
		byIdentity[stored.Record.IdentityKey] = stored
	}
	for index, envelope := range chunk.Records {
		sequence := chunk.FirstSequence + index
		if collectorSequence {
			sequence = len(candidate) + 1
		}
		if (!collectorSequence && envelope.Sequence != sequence) || (collectorSequence && envelope.Sequence != 0) || envelope.Record.TraceID != chunk.Trace.TraceID || envelope.Record.SchemaVersion != chunk.Trace.SchemaVersion || envelope.Record.SemanticConventionVersion != chunk.Trace.SemanticConventionVersion {
			return memoryTrace{}, 0, &ChunkError{
				Code: CodeInvalidChunk, CommittedThrough: len(existing.records),
				Err: errors.New("Collector record changed its Trace envelope"),
			}
		}
		hash, err := envelope.Record.CanonicalHash()
		if err != nil {
			return memoryTrace{}, 0, &ChunkError{Code: CodeInvalidChunk, CommittedThrough: len(existing.records), Err: err}
		}
		if envelope.CanonicalSHA256 != hex.EncodeToString(hash[:]) {
			return memoryTrace{}, 0, &ChunkError{
				Code: CodeCanonicalHash, CommittedThrough: len(existing.records), Err: agentobs.ErrIdentityConflict,
			}
		}
		if collectorSequence {
			if stored, found := byIdentity[envelope.Record.IdentityKey]; found {
				if stored.CanonicalSHA256 != envelope.CanonicalSHA256 {
					return memoryTrace{}, 0, &ChunkError{
						Code: CodeIdentityConflict, CommittedThrough: len(existing.records), Err: agentobs.ErrIdentityConflict,
					}
				}
				continue
			}
			envelope.Sequence = sequence
		}
		if sequence <= len(existing.records) {
			stored := existing.records[sequence-1]
			if stored.CanonicalSHA256 != envelope.CanonicalSHA256 {
				return memoryTrace{}, 0, &ChunkError{
					Code: CodeIdentityConflict, CommittedThrough: len(existing.records), Err: agentobs.ErrIdentityConflict,
				}
			}
			continue
		}
		if sequence != len(candidate)+1 {
			return memoryTrace{}, 0, &ChunkError{
				Code: CodeSequenceGap, CommittedThrough: len(existing.records), Retryable: true,
				Err: errors.New("Collector Trace Chunk sequence is not contiguous"),
			}
		}
		if !collectorSequence && sequence == 1 && (envelope.Record.Kind != agentobs.RecordSpanStarted || envelope.Record.SpanID != chunk.Trace.RootSpanID || envelope.Record.ParentSpanID != "") {
			return memoryTrace{}, 0, &ChunkError{
				Code: CodeInvalidLifecycle, CommittedThrough: len(existing.records),
				Err: fmt.Errorf("%w: first Collector record is not the Trace root", agentobs.ErrLifecycle),
			}
		}
		if envelope.Record.Kind == agentobs.RecordLink && envelope.Record.TargetTraceID != chunk.Trace.TraceID {
			target := linkTarget{traceID: envelope.Record.TargetTraceID, spanID: envelope.Record.TargetSpanID}
			if _, found := resolvedLinks[target]; !found {
				if resolveDependency == nil {
					return memoryTrace{}, 0, missingLinkDependency(len(existing.records), target)
				}
				found, err := resolveDependency(ctx, target.traceID, target.spanID)
				if err != nil {
					return memoryTrace{}, 0, err
				}
				if !found && !collectorSequence {
					return memoryTrace{}, 0, missingLinkDependency(len(existing.records), target)
				}
				if found {
					resolvedLinks[target] = struct{}{}
				}
			}
		}
		if err := validator.Export(ctx, envelope.Record); err != nil {
			if collectorSequence && (errors.Is(err, agentobs.ErrLifecycle) || errors.Is(err, agentobs.ErrUnresolvedLink)) {
				candidate = append(candidate, cloneSequencedRecord(envelope))
				byIdentity[envelope.Record.IdentityKey] = envelope
				continue
			}
			if errors.Is(err, agentobs.ErrLifecycle) || errors.Is(err, agentobs.ErrUnresolvedLink) || errors.Is(err, agentobs.ErrLimitExceeded) {
				return memoryTrace{}, 0, &ChunkError{Code: CodeInvalidLifecycle, CommittedThrough: len(existing.records), Err: err}
			}
			return memoryTrace{}, 0, err
		}
		candidate = append(candidate, cloneSequencedRecord(envelope))
		byIdentity[envelope.Record.IdentityKey] = envelope
	}
	attachments := make(map[string]AttachmentDescriptor, len(existing.attachments)+len(chunk.Attachments))
	for attachmentID, attachment := range existing.attachments {
		attachments[attachmentID] = cloneAttachmentDescriptor(attachment)
	}
	for _, attachment := range chunk.Attachments {
		attachments[attachment.AttachmentID] = cloneAttachmentDescriptor(attachment)
	}
	return memoryTrace{descriptor: chunk.Trace, records: candidate, attachments: attachments}, candidate[len(candidate)-1].Sequence, nil
}

func missingLinkDependency(committedThrough int, target linkTarget) *ChunkError {
	return &ChunkError{
		Code: CodeDependencyMissing, CommittedThrough: committedThrough, Retryable: true,
		Err: fmt.Errorf("%w: %s/%s", agentobs.ErrUnresolvedLink, target.traceID, target.spanID),
	}
}

func memoryTraceHasSpan(trace memoryTrace, spanID agentobs.SpanID) bool {
	for _, record := range trace.records {
		if record.Record.Kind == agentobs.RecordSpanStarted && record.Record.SpanID == spanID {
			return true
		}
	}
	return false
}

func sameAttachmentIdentity(left, right AttachmentDescriptor) bool {
	return left.AttachmentID == right.AttachmentID &&
		left.RecordSequence == right.RecordSequence &&
		left.RecordIdentityKey == right.RecordIdentityKey &&
		left.Class == right.Class &&
		left.SchemaVersion == right.SchemaVersion &&
		left.PlaintextSHA256 == right.PlaintextSHA256 &&
		left.CiphertextBytes == right.CiphertextBytes &&
		left.CiphertextSHA256 == right.CiphertextSHA256 &&
		left.Compression == right.Compression &&
		left.Encryption == right.Encryption &&
		left.KeyID == right.KeyID &&
		bytes.Equal(left.WrappedKey, right.WrappedKey) &&
		bytes.Equal(left.Nonce, right.Nonce) &&
		left.ExpiresAt.UnixNano() == right.ExpiresAt.UnixNano()
}

func cloneAttachmentDescriptor(attachment AttachmentDescriptor) AttachmentDescriptor {
	attachment.WrappedKey = append([]byte(nil), attachment.WrappedKey...)
	attachment.Nonce = append([]byte(nil), attachment.Nonce...)
	return attachment
}

func validateAttachmentDescriptors(chunk TraceChunk) error {
	if len(chunk.Attachments) > 32 {
		return errors.New("Collector Trace Chunk has too many Replay Attachments")
	}
	byID := make(map[string]AttachmentDescriptor, len(chunk.Attachments))
	byRecordClass := make(map[string]struct{}, len(chunk.Attachments))
	firstSequence := chunk.FirstSequence
	lastSequence := firstSequence + len(chunk.Records) - 1
	direct := chunk.SequenceAuthority == SequenceAuthorityCollector
	for _, attachment := range chunk.Attachments {
		if _, err := uuid.Parse(attachment.AttachmentID); err != nil ||
			(!direct && (attachment.RecordSequence < firstSequence || attachment.RecordSequence > lastSequence)) ||
			(direct && (attachment.RecordSequence < 1 || !validDescriptorText(attachment.RecordIdentityKey, 200))) ||
			!attachment.Class.Valid() || attachment.SchemaVersion != 1 ||
			!validSHA256(attachment.PlaintextSHA256) ||
			!validDescriptorText(attachment.StagingObjectKey, 512) ||
			attachment.CiphertextBytes < 1 || attachment.CiphertextBytes > replay.MaxCiphertextBytes ||
			!validSHA256(attachment.CiphertextSHA256) || attachment.Compression != replay.CompressionGZIP ||
			attachment.Encryption != replay.EncryptionAES256GCM || !validDescriptorText(attachment.KeyID, 160) ||
			len(attachment.WrappedKey) < 1 || len(attachment.WrappedKey) > 1024 ||
			len(attachment.Nonce) < 1 || len(attachment.Nonce) > 64 || attachment.ExpiresAt.IsZero() {
			return errors.New("Collector Replay Attachment descriptor is invalid")
		}
		if _, duplicate := byID[attachment.AttachmentID]; duplicate {
			return errors.New("Collector Replay Attachment identity is duplicated")
		}
		recordClass := fmt.Sprintf("%d/%s", attachment.RecordSequence, attachment.Class)
		if _, duplicate := byRecordClass[recordClass]; duplicate {
			return errors.New("Collector Replay Attachment record class is duplicated")
		}
		byID[attachment.AttachmentID] = attachment
		byRecordClass[recordClass] = struct{}{}
	}
	for index, envelope := range chunk.Records {
		references, err := replay.AttachmentReferences(envelope.Record.Attributes)
		if err != nil {
			return err
		}
		for _, reference := range references {
			descriptor, found := byID[reference.AttachmentID]
			if !found || descriptor.Class != reference.Class ||
				(!direct && descriptor.RecordSequence != firstSequence+index) ||
				(direct && descriptor.RecordIdentityKey != envelope.Record.IdentityKey) {
				return errors.New("Collector record Replay Attachment does not resolve")
			}
		}
	}
	for _, descriptor := range byID {
		var record agentobs.Record
		if direct {
			for _, envelope := range chunk.Records {
				if envelope.Record.IdentityKey == descriptor.RecordIdentityKey {
					record = envelope.Record
					break
				}
			}
		} else {
			record = chunk.Records[descriptor.RecordSequence-firstSequence].Record
		}
		if record.IdentityKey == "" {
			return errors.New("Collector Replay Attachment record identity does not resolve")
		}
		references, _ := replay.AttachmentReferences(record.Attributes)
		found := false
		for _, reference := range references {
			found = found || (reference.AttachmentID == descriptor.AttachmentID && reference.Class == descriptor.Class)
		}
		if !found {
			return errors.New("Collector Replay Attachment is not referenced by its record")
		}
	}
	return nil
}

func resolveDirectAttachmentSequences(existing []SequencedRecord, chunk TraceChunk) (TraceChunk, error) {
	if chunk.SequenceAuthority != SequenceAuthorityCollector || len(chunk.Attachments) == 0 {
		return chunk, nil
	}
	sequences := make(map[string]int, len(existing)+len(chunk.Records))
	for _, record := range existing {
		sequences[record.Record.IdentityKey] = record.Sequence
	}
	next := len(existing)
	for _, record := range chunk.Records {
		if _, found := sequences[record.Record.IdentityKey]; found {
			continue
		}
		next++
		sequences[record.Record.IdentityKey] = next
	}
	chunk.Attachments = append([]AttachmentDescriptor(nil), chunk.Attachments...)
	for index := range chunk.Attachments {
		sequence, found := sequences[chunk.Attachments[index].RecordIdentityKey]
		if !found {
			return TraceChunk{}, errors.New("Collector Replay Attachment record identity is unknown")
		}
		chunk.Attachments[index].RecordSequence = sequence
	}
	return chunk, nil
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
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
	_, err := CanonicalTraceDescriptor(trace)
	return err
}

// CanonicalTraceDescriptor validates a Trace identity and fills the legacy
// Agent-run workload fields used before workload-aware descriptors existed.
func CanonicalTraceDescriptor(trace TraceDescriptor) (TraceDescriptor, error) {
	if trace.WorkloadKind == "" {
		trace.WorkloadKind = WorkloadAgentRun
	}
	if trace.WorkloadID == "" && trace.WorkloadKind == WorkloadAgentRun {
		trace.WorkloadID = trace.RunID
	}
	if !validDescriptorText(string(trace.TraceID), 128) ||
		!validDescriptorText(string(trace.WorkloadKind), 64) ||
		!validDescriptorText(trace.WorkloadID, 160) ||
		!validDescriptorText(trace.NotebookID, 128) || !validDescriptorText(string(trace.RootSpanID), 128) ||
		!validDescriptorText(trace.AgentName, 160) || trace.SchemaVersion < 1 || trace.SemanticConventionVersion < 1 {
		return TraceDescriptor{}, errors.New("Collector Trace descriptor is incomplete")
	}
	switch trace.WorkloadKind {
	case WorkloadAgentRun:
		if !validDescriptorText(trace.RunID, 128) || !validDescriptorText(trace.ChatID, 128) || trace.WorkloadID != trace.RunID {
			return TraceDescriptor{}, errors.New("Collector Agent-run Trace identity is inconsistent")
		}
	case WorkloadSourceProcessing:
		if trace.RunID != "" || trace.ChatID != "" {
			return TraceDescriptor{}, errors.New("Collector Source-processing Trace cannot carry Agent-run identity")
		}
	default:
		return TraceDescriptor{}, errors.New("Collector Trace workload kind is unsupported")
	}
	return trace, nil
}

func validDescriptorText(value string, maxRunes int) bool {
	return strings.TrimSpace(value) != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxRunes
}

func cloneSequencedRecord(envelope SequencedRecord) SequencedRecord {
	envelope.Record.Attributes = append([]agentobs.Attribute(nil), envelope.Record.Attributes...)
	return envelope
}

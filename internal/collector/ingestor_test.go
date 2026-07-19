package collector_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
)

func TestIngestorCommitsContiguousTraceChunk(t *testing.T) {
	store := collector.NewMemoryStore()
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{
		ProducerID: "nano-worker",
		Store:      store,
	})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}

	batch := validCollectorBatch(t)
	result, err := ingestor.Ingest(context.Background(), batch)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(result.Chunks) != 1 {
		t.Fatalf("chunk results = %d, want 1", len(result.Chunks))
	}
	if got := result.Chunks[0]; got.Status != collector.ChunkCommitted || got.CommittedThrough != 2 || got.Code != "" {
		t.Fatalf("chunk result = %#v", got)
	}

	stored := store.Records("trace-1")
	if len(stored) != 2 || stored[0].Sequence != 1 || stored[1].Record.IdentityKey != batch.Chunks[0].Records[1].Record.IdentityKey {
		t.Fatalf("stored records = %#v", stored)
	}
}

func TestIngestorAssignsSequencesToDirectTraceRecords(t *testing.T) {
	store := collector.NewMemoryStore()
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{
		ProducerID: "nano-worker",
		Store:      store,
	})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}

	batch := validCollectorBatch(t)
	batch.ProtocolVersion = collector.DirectProtocolVersion
	batch.BatchID = "batch-direct"
	batch.Chunks[0].SequenceAuthority = collector.SequenceAuthorityCollector
	batch.Chunks[0].FirstSequence = 0
	for index := range batch.Chunks[0].Records {
		batch.Chunks[0].Records[index].Sequence = 0
	}

	result, err := ingestor.Ingest(context.Background(), batch)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if got := result.Chunks[0]; got.Status != collector.ChunkCommitted || got.CommittedThrough != 2 {
		t.Fatalf("direct result = %#v", got)
	}
	stored := store.Records("trace-1")
	if len(stored) != 2 || stored[0].Sequence != 1 || stored[1].Sequence != 2 {
		t.Fatalf("Collector-assigned records = %#v", stored)
	}
}

func TestIngestorReconcilesDirectRecordIdentityOnResend(t *testing.T) {
	store := collector.NewMemoryStore()
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	batch := directCollectorBatch(t)
	if _, err := ingestor.Ingest(context.Background(), batch); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}

	batch.BatchID = "batch-direct-resend"
	result, err := ingestor.Ingest(context.Background(), batch)
	if err != nil {
		t.Fatalf("resend Ingest: %v", err)
	}
	if got := result.Chunks[0]; got.Status != collector.ChunkCommitted || got.CommittedThrough != 2 {
		t.Fatalf("resend result = %#v", got)
	}
	if got := len(store.Records("trace-1")); got != 2 {
		t.Fatalf("records after resend = %d, want 2", got)
	}
}

func TestIngestorRejectsConflictingDirectRecordIdentity(t *testing.T) {
	store := collector.NewMemoryStore()
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	batch := directCollectorBatch(t)
	if _, err := ingestor.Ingest(context.Background(), batch); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}

	conflict := directCollectorBatch(t)
	conflict.BatchID = "batch-direct-conflict"
	conflict.Chunks[0].Records[1].Record.Name = "nano.run.changed"
	conflict.Chunks[0].Records[1] = collectorEnvelope(t, 0, conflict.Chunks[0].Records[1].Record)
	result, err := ingestor.Ingest(context.Background(), conflict)
	if err != nil {
		t.Fatalf("conflicting Ingest: %v", err)
	}
	if got := result.Chunks[0]; got.Status != collector.ChunkRejected || got.Code != collector.CodeIdentityConflict || got.CommittedThrough != 2 {
		t.Fatalf("conflict result = %#v", got)
	}
	stored := store.Records("trace-1")
	if len(stored) != 2 || stored[1].Record.Name != "nano.run.admitted" {
		t.Fatalf("stored records changed after conflict: %#v", stored)
	}
}

func TestIngestorAssignsOneSequenceAcrossDirectProducerInstances(t *testing.T) {
	store := collector.NewMemoryStore()
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{
		ProducerIDPrefix: "nano-",
		Store:            store,
	})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	first := directCollectorBatch(t)
	first.ProducerID = "nano-control-plane/one"
	if _, err := ingestor.Ingest(context.Background(), first); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}

	second := directCollectorBatch(t)
	second.BatchID = "batch-direct-worker"
	second.ProducerID = "nano-worker/two"
	continued := second.Chunks[0].Records[1].Record
	continued.IdentityKey = "run/run-1/worker-observed"
	continued.Name = "nano.worker.observed"
	second.Chunks[0].Records = []collector.SequencedRecord{collectorEnvelope(t, 0, continued)}
	result, err := ingestor.Ingest(context.Background(), second)
	if err != nil {
		t.Fatalf("second Ingest: %v", err)
	}
	if got := result.Chunks[0]; got.Status != collector.ChunkCommitted || got.CommittedThrough != 3 {
		t.Fatalf("second result = %#v", got)
	}
	stored := store.Records("trace-1")
	if len(stored) != 3 || stored[2].Sequence != 3 || stored[2].Record.IdentityKey != continued.IdentityKey {
		t.Fatalf("stored records = %#v", stored)
	}
}

func TestIngestorAcknowledgesAnIdenticalTraceChunkResend(t *testing.T) {
	store := collector.NewMemoryStore()
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	batch := validCollectorBatch(t)
	if _, err := ingestor.Ingest(context.Background(), batch); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}

	result, err := ingestor.Ingest(context.Background(), batch)
	if err != nil {
		t.Fatalf("resend Ingest: %v", err)
	}
	if got := result.Chunks[0]; got.Status != collector.ChunkCommitted || got.CommittedThrough != 2 {
		t.Fatalf("resend result = %#v", got)
	}
	if got := len(store.Records("trace-1")); got != 2 {
		t.Fatalf("record count after resend = %d, want 2", got)
	}
}

func TestIngestorCommitsCrossTraceLinkAfterItsTarget(t *testing.T) {
	store := collector.NewMemoryStore()
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatal(err)
	}
	source := collectorBatchFor(t, "link-source")
	if result, err := ingestor.Ingest(context.Background(), source); err != nil || result.Chunks[0].Status != collector.ChunkCommitted {
		t.Fatalf("source Ingest = %#v, %v", result, err)
	}
	retry := collectorBatchFor(t, "link-retry")
	retriedFrom := retry.Chunks[0].Records[1].Record
	retriedFrom.Kind = agentobs.RecordLink
	retriedFrom.Name = "retried_from"
	retriedFrom.TargetTraceID = source.Chunks[0].Trace.TraceID
	retriedFrom.TargetSpanID = source.Chunks[0].Trace.RootSpanID
	retry.Chunks[0].Records[1] = collectorEnvelope(t, 2, retriedFrom)

	result, err := ingestor.Ingest(context.Background(), retry)
	if err != nil {
		t.Fatalf("retry Ingest transport error: %v", err)
	}
	if got := result.Chunks[0]; got.Status != collector.ChunkCommitted || got.CommittedThrough != 2 {
		t.Fatalf("cross-Trace Link result = %#v", got)
	}
}

func TestIngestorRetriesCrossTraceLinkWithMissingTarget(t *testing.T) {
	store := collector.NewMemoryStore()
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatal(err)
	}
	retry := collectorBatchFor(t, "link-missing")
	retriedFrom := retry.Chunks[0].Records[1].Record
	retriedFrom.Kind = agentobs.RecordLink
	retriedFrom.Name = "retried_from"
	retriedFrom.TargetTraceID = "trace-not-committed"
	retriedFrom.TargetSpanID = "root-not-committed"
	retry.Chunks[0].Records[1] = collectorEnvelope(t, 2, retriedFrom)

	result, err := ingestor.Ingest(context.Background(), retry)
	if err != nil {
		t.Fatalf("missing dependency transport error: %v", err)
	}
	if got := result.Chunks[0]; got.Status != collector.ChunkRetryable || got.Code != collector.CodeDependencyMissing || got.CommittedThrough != 0 {
		t.Fatalf("missing cross-Trace target result = %#v", got)
	}
}

func TestIngestorRejectsReplayAttachmentMetadataMutationOnResend(t *testing.T) {
	store := collector.NewMemoryStore()
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	batch := collectorBatchWithReplay(t, bytes.Repeat([]byte{0xa5}, 256))
	if _, err := ingestor.Ingest(context.Background(), batch); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}

	conflict := collectorBatchWithReplay(t, bytes.Repeat([]byte{0xa5}, 256))
	conflict.BatchID = "batch-replay-conflict"
	conflict.Chunks[0].Attachments[0].PlaintextSHA256 = strings.Repeat("c", 64)
	result, err := ingestor.Ingest(context.Background(), conflict)
	if err != nil {
		t.Fatalf("conflicting Ingest transport error: %v", err)
	}
	if got := result.Chunks[0]; got.Status != collector.ChunkRejected || got.Code != collector.CodeIdentityConflict || got.CommittedThrough != 2 {
		t.Fatalf("Replay conflict result = %#v", got)
	}
}

func TestIngestorRejectsAConflictingTraceChunkWithoutChangingCommittedData(t *testing.T) {
	store := collector.NewMemoryStore()
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	batch := validCollectorBatch(t)
	if _, err := ingestor.Ingest(context.Background(), batch); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}

	conflict := validCollectorBatch(t)
	conflict.BatchID = "batch-conflict"
	conflict.Chunks[0].Records[1].Record.Name = "nano.run.changed"
	conflict.Chunks[0].Records[1] = collectorEnvelope(t, 2, conflict.Chunks[0].Records[1].Record)
	result, err := ingestor.Ingest(context.Background(), conflict)
	if err != nil {
		t.Fatalf("conflicting Ingest transport error: %v", err)
	}
	if got := result.Chunks[0]; got.Status != collector.ChunkRejected || got.Code != collector.CodeIdentityConflict || got.CommittedThrough != 2 {
		t.Fatalf("conflict result = %#v", got)
	}
	stored := store.Records("trace-1")
	if len(stored) != 2 || stored[1].Record.Name != "nano.run.admitted" {
		t.Fatalf("stored records changed after conflict: %#v", stored)
	}
}

func TestIngestorCommitsValidChunksWhenAnotherChunkHasInvalidLifecycle(t *testing.T) {
	store := collector.NewMemoryStore()
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	invalid := collectorBatchFor(t, "invalid")
	invalid.Chunks[0].Records[0].Record.Kind = agentobs.RecordEvent
	invalid.Chunks[0].Records[0] = collectorEnvelope(t, 1, invalid.Chunks[0].Records[0].Record)
	valid := collectorBatchFor(t, "valid")
	invalid.BatchID = "batch-mixed"
	invalid.Chunks = append(invalid.Chunks, valid.Chunks[0])

	result, err := ingestor.Ingest(context.Background(), invalid)
	if err != nil {
		t.Fatalf("mixed Ingest transport error: %v", err)
	}
	if len(result.Chunks) != 2 {
		t.Fatalf("chunk results = %d, want 2", len(result.Chunks))
	}
	if got := result.Chunks[0]; got.Status != collector.ChunkRejected || got.Code != "invalid_lifecycle" || got.CommittedThrough != 0 {
		t.Fatalf("invalid result = %#v", got)
	}
	if got := result.Chunks[1]; got.Status != collector.ChunkCommitted || got.CommittedThrough != 2 {
		t.Fatalf("valid result = %#v", got)
	}
	if got := len(store.Records("trace-invalid")); got != 0 {
		t.Fatalf("invalid Trace stored %d records", got)
	}
	if got := len(store.Records("trace-valid")); got != 2 {
		t.Fatalf("valid Trace stored %d records, want 2", got)
	}
}

func TestIngestorCommitsValidChunksWhenAnotherChunkHasInvalidEnvelope(t *testing.T) {
	store := collector.NewMemoryStore()
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	batch := collectorBatchFor(t, "invalid-envelope")
	batch.BatchID = "batch-mixed-envelope"
	batch.Chunks[0].Trace.RunID = ""
	valid := collectorBatchFor(t, "valid-envelope")
	batch.Chunks = append(batch.Chunks, valid.Chunks[0])

	result, err := ingestor.Ingest(context.Background(), batch)
	if err != nil {
		t.Fatalf("mixed Ingest transport error: %v", err)
	}
	if len(result.Chunks) != 2 {
		t.Fatalf("chunk results = %d, want 2", len(result.Chunks))
	}
	if got := result.Chunks[0]; got.Status != collector.ChunkRejected || got.Code != collector.CodeInvalidChunk || got.CommittedThrough != 0 {
		t.Fatalf("invalid envelope result = %#v", got)
	}
	if got := result.Chunks[1]; got.Status != collector.ChunkCommitted || got.CommittedThrough != 2 {
		t.Fatalf("valid result = %#v", got)
	}
	if got := len(store.Records("trace-valid-envelope")); got != 2 {
		t.Fatalf("valid Trace stored %d records, want 2", got)
	}
}

func TestIngestorRejectsOversizedTraceDescriptorWithoutRetryingStore(t *testing.T) {
	store := collector.NewMemoryStore()
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	batch := validCollectorBatch(t)
	batch.Chunks[0].Trace.RunID = strings.Repeat("r", 129)

	result, err := ingestor.Ingest(context.Background(), batch)
	if err != nil {
		t.Fatalf("Ingest transport error: %v", err)
	}
	if got := result.Chunks[0]; got.Status != collector.ChunkRejected || got.Code != collector.CodeInvalidChunk {
		t.Fatalf("oversized descriptor result = %#v", got)
	}
	if got := len(store.Records("trace-1")); got != 0 {
		t.Fatalf("oversized descriptor stored %d records", got)
	}
}

func TestIngestorReturnsRetryableSequenceGapWithCurrentCursor(t *testing.T) {
	store := collector.NewMemoryStore()
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	batch := validCollectorBatch(t)
	if _, err := ingestor.Ingest(context.Background(), batch); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}

	gapRecord := collectorRecord("trace-1", "root-1", "run/run-1/gap", agentobs.RecordEvent, "nano.gap")
	gap := validCollectorBatch(t)
	gap.BatchID = "batch-gap"
	gap.Chunks[0].FirstSequence = 4
	gap.Chunks[0].Records = []collector.SequencedRecord{collectorEnvelope(t, 4, gapRecord)}
	result, err := ingestor.Ingest(context.Background(), gap)
	if err != nil {
		t.Fatalf("gap Ingest transport error: %v", err)
	}
	if got := result.Chunks[0]; got.Status != collector.ChunkRetryable || got.Code != "sequence_gap" || got.CommittedThrough != 2 {
		t.Fatalf("gap result = %#v", got)
	}
	if got := len(store.Records("trace-1")); got != 2 {
		t.Fatalf("record count after gap = %d, want 2", got)
	}
}

func TestIngestorRejectsCanonicalHashMismatchPerTrace(t *testing.T) {
	store := collector.NewMemoryStore()
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	batch := validCollectorBatch(t)
	batch.Chunks[0].Records[0].CanonicalSHA256 = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"

	result, err := ingestor.Ingest(context.Background(), batch)
	if err != nil {
		t.Fatalf("hash mismatch Ingest transport error: %v", err)
	}
	if got := result.Chunks[0]; got.Status != collector.ChunkRejected || got.Code != "canonical_hash_mismatch" || got.CommittedThrough != 0 {
		t.Fatalf("hash mismatch result = %#v", got)
	}
	if got := len(store.Records("trace-1")); got != 0 {
		t.Fatalf("hash mismatch stored %d records", got)
	}
}

func TestIngestorRejectsUnsupportedRecordSchemaExplicitly(t *testing.T) {
	store := collector.NewMemoryStore()
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	batch := validCollectorBatch(t)
	batch.Chunks[0].Trace.SchemaVersion = 2
	for index := range batch.Chunks[0].Records {
		batch.Chunks[0].Records[index].Record.SchemaVersion = 2
		batch.Chunks[0].Records[index] = collectorEnvelope(t, index+1, batch.Chunks[0].Records[index].Record)
	}

	result, err := ingestor.Ingest(context.Background(), batch)
	if err != nil {
		t.Fatalf("unsupported schema Ingest transport error: %v", err)
	}
	if got := result.Chunks[0]; got.Status != collector.ChunkRejected || got.Code != "unsupported_schema" || got.CommittedThrough != 0 {
		t.Fatalf("unsupported schema result = %#v", got)
	}
	if got := len(store.Records("trace-1")); got != 0 {
		t.Fatalf("unsupported schema stored %d records", got)
	}
}

func validCollectorBatch(t *testing.T) collector.Batch {
	t.Helper()
	return collectorBatchFor(t, "1")
}

func directCollectorBatch(t *testing.T) collector.Batch {
	t.Helper()
	batch := validCollectorBatch(t)
	batch.ProtocolVersion = collector.DirectProtocolVersion
	batch.BatchID = "batch-direct"
	batch.Chunks[0].SequenceAuthority = collector.SequenceAuthorityCollector
	batch.Chunks[0].FirstSequence = 0
	for index := range batch.Chunks[0].Records {
		batch.Chunks[0].Records[index].Sequence = 0
	}
	return batch
}

func collectorBatchFor(t *testing.T, suffix string) collector.Batch {
	t.Helper()
	traceID := agentobs.TraceID("trace-" + suffix)
	rootSpanID := agentobs.SpanID("root-" + suffix)
	runID := "run-" + suffix
	root := collectorRecord(traceID, rootSpanID, "run/"+runID+"/root/start", agentobs.RecordSpanStarted, "agent.execution")
	event := collectorRecord(traceID, rootSpanID, "run/"+runID+"/admitted", agentobs.RecordEvent, "nano.run.admitted")
	return collector.Batch{
		ProtocolVersion: collector.ProtocolVersion,
		BatchID:         "batch-" + suffix,
		ProducerID:      "nano-worker",
		CreatedAt:       time.Unix(1_700_000_100, 0).UTC(),
		Chunks: []collector.TraceChunk{{
			Trace: collector.TraceDescriptor{
				TraceID:                   traceID,
				RunID:                     runID,
				ChatID:                    "chat-" + suffix,
				NotebookID:                "notebook-" + suffix,
				RootSpanID:                rootSpanID,
				AgentName:                 "nano-research-agent",
				SchemaVersion:             1,
				SemanticConventionVersion: 1,
			},
			FirstSequence: 1,
			Records: []collector.SequencedRecord{
				collectorEnvelope(t, 1, root),
				collectorEnvelope(t, 2, event),
			},
		}},
	}
}

func collectorEnvelope(t *testing.T, sequence int, record agentobs.Record) collector.SequencedRecord {
	t.Helper()
	hash, err := record.CanonicalHash()
	if err != nil {
		t.Fatalf("CanonicalHash: %v", err)
	}
	return collector.SequencedRecord{Sequence: sequence, Record: record, CanonicalSHA256: hex.EncodeToString(hash[:])}
}

func collectorRecord(traceID agentobs.TraceID, spanID agentobs.SpanID, identity string, kind agentobs.RecordKind, name string) agentobs.Record {
	return agentobs.Record{
		SchemaVersion:             1,
		SemanticConventionVersion: 1,
		IdentityKey:               identity,
		Kind:                      kind,
		TraceID:                   traceID,
		SpanID:                    spanID,
		Name:                      name,
		OccurredAt:                time.Unix(1_700_000_000, 0).UTC(),
		PayloadVersion:            1,
	}
}

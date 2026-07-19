package collector_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/semconv"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/replay"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestObservabilityDatabaseUsesDedicatedCollectorRole(t *testing.T) {
	ctx := context.Background()
	pool := openObservabilityTestPool(t, ctx)
	t.Cleanup(pool.Close)
	var currentUser string
	if err := pool.QueryRow(ctx, `select current_user`).Scan(&currentUser); err != nil {
		t.Fatalf("load current PostgreSQL user: %v", err)
	}
	if currentUser != "nano_observability" {
		t.Fatalf("Observability PostgreSQL user = %q, want nano_observability", currentUser)
	}
}

func TestPostgresStorePersistsCommittedTraceAcrossConnections(t *testing.T) {
	ctx := context.Background()
	pool := openObservabilityTestPool(t, ctx)
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
	})
	resetObservabilityTestSchema(t, ctx, pool)

	store := collector.NewPostgresStore(pool)
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	batch := validCollectorBatch(t)
	for index := range batch.Chunks[0].Records {
		batch.Chunks[0].Records[index].Record.OccurredAt = time.Unix(1_700_000_000, int64(123_456_789+index)).UTC()
		batch.Chunks[0].Records[index] = collectorEnvelope(t, index+1, batch.Chunks[0].Records[index].Record)
	}
	result, err := ingestor.Ingest(ctx, batch)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if got := result.Chunks[0]; got.Status != collector.ChunkCommitted || got.CommittedThrough != 2 {
		t.Fatalf("chunk result = %#v", got)
	}

	pool.Close()
	pool = nil
	reopened := openObservabilityTestPool(t, ctx)
	t.Cleanup(reopened.Close)
	stored, err := collector.NewPostgresStore(reopened).LoadTrace(ctx, "trace-1")
	if err != nil {
		t.Fatalf("LoadTrace after reopen: %v", err)
	}
	if stored.Trace != batch.Chunks[0].Trace {
		t.Fatalf("stored descriptor = %#v, want %#v", stored.Trace, batch.Chunks[0].Trace)
	}
	if stored.CommittedThrough != 2 || stored.ProjectedThrough != 0 || stored.Tombstoned {
		t.Fatalf("stored cursor state = %#v", stored)
	}
	if len(stored.Records) != 2 {
		t.Fatalf("stored records = %d, want 2", len(stored.Records))
	}
	for index, want := range batch.Chunks[0].Records {
		got := stored.Records[index]
		if got.Sequence != want.Sequence || got.CanonicalSHA256 != want.CanonicalSHA256 || got.Record.IdentityKey != want.Record.IdentityKey || !got.Record.OccurredAt.Equal(want.Record.OccurredAt) {
			t.Fatalf("stored record %d = %#v, want %#v", index, got, want)
		}
	}
	var projectionTarget int
	if err := reopened.QueryRow(ctx, `select target_sequence from obs_projection_queue where trace_id = $1`, "trace-1").Scan(&projectionTarget); err != nil {
		t.Fatalf("load projection target: %v", err)
	}
	if projectionTarget != 2 {
		t.Fatalf("projection target = %d, want 2", projectionTarget)
	}
}

func TestPostgresStoreReconcilesResendAndRejectsConflictAtomically(t *testing.T) {
	ctx := context.Background()
	pool := openObservabilityTestPool(t, ctx)
	t.Cleanup(pool.Close)
	resetObservabilityTestSchema(t, ctx, pool)
	store := collector.NewPostgresStore(pool)
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	batch := validCollectorBatch(t)
	if _, err := ingestor.Ingest(ctx, batch); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}

	resend, err := ingestor.Ingest(ctx, batch)
	if err != nil {
		t.Fatalf("resend Ingest: %v", err)
	}
	if got := resend.Chunks[0]; got.Status != collector.ChunkCommitted || got.CommittedThrough != 2 {
		t.Fatalf("resend result = %#v", got)
	}
	conflict := validCollectorBatch(t)
	conflict.BatchID = "batch-conflict-postgres"
	conflict.Chunks[0].Records[1].Record.Name = "nano.run.changed"
	conflict.Chunks[0].Records[1] = collectorEnvelope(t, 2, conflict.Chunks[0].Records[1].Record)
	rejected, err := ingestor.Ingest(ctx, conflict)
	if err != nil {
		t.Fatalf("conflict Ingest: %v", err)
	}
	if got := rejected.Chunks[0]; got.Status != collector.ChunkRejected || got.Code != collector.CodeIdentityConflict || got.CommittedThrough != 2 {
		t.Fatalf("conflict result = %#v", got)
	}

	stored, err := store.LoadTrace(ctx, "trace-1")
	if err != nil {
		t.Fatalf("LoadTrace: %v", err)
	}
	if len(stored.Records) != 2 || stored.Records[1].Record.Name != "nano.run.admitted" {
		t.Fatalf("stored Trace changed after resend/conflict: %#v", stored)
	}
}

func TestPostgresStorePersistsCrossTraceLinkAfterTarget(t *testing.T) {
	ctx := context.Background()
	pool := openObservabilityTestPool(t, ctx)
	t.Cleanup(pool.Close)
	resetObservabilityTestSchema(t, ctx, pool)
	store := collector.NewPostgresStore(pool)
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatal(err)
	}
	source := collectorBatchFor(t, "postgres-link-source")
	if result, err := ingestor.Ingest(ctx, source); err != nil || result.Chunks[0].Status != collector.ChunkCommitted {
		t.Fatalf("source Ingest = %#v, %v", result, err)
	}
	retry := collectorBatchWithCrossTraceLink(t, "postgres-link-retry", source.Chunks[0].Trace.TraceID, source.Chunks[0].Trace.RootSpanID)
	result, err := ingestor.Ingest(ctx, retry)
	if err != nil || result.Chunks[0].Status != collector.ChunkCommitted || result.Chunks[0].CommittedThrough != 2 {
		t.Fatalf("cross-Trace Link Ingest = %#v, %v", result, err)
	}
	stored, err := store.LoadTrace(ctx, retry.Chunks[0].Trace.TraceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Records) != 2 || stored.Records[1].Record.Kind != agentobs.RecordLink ||
		stored.Records[1].Record.TargetTraceID != source.Chunks[0].Trace.TraceID {
		t.Fatalf("stored Retry Trace = %#v", stored)
	}
}

func TestPostgresStoreRetriesMissingCrossTraceLinkUntilTargetCommits(t *testing.T) {
	ctx := context.Background()
	pool := openObservabilityTestPool(t, ctx)
	t.Cleanup(pool.Close)
	resetObservabilityTestSchema(t, ctx, pool)
	store := collector.NewPostgresStore(pool)
	ingestor, _ := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	source := collectorBatchFor(t, "postgres-link-late-source")
	retry := collectorBatchWithCrossTraceLink(t, "postgres-link-waits", source.Chunks[0].Trace.TraceID, source.Chunks[0].Trace.RootSpanID)

	missing, err := ingestor.Ingest(ctx, retry)
	if err != nil {
		t.Fatalf("missing dependency transport error: %v", err)
	}
	if got := missing.Chunks[0]; got.Status != collector.ChunkRetryable || got.Code != collector.CodeDependencyMissing || got.CommittedThrough != 0 {
		t.Fatalf("missing dependency result = %#v", got)
	}
	var retryRows int
	if err := pool.QueryRow(ctx, `select count(*) from obs_traces where trace_id = $1`, retry.Chunks[0].Trace.TraceID).Scan(&retryRows); err != nil {
		t.Fatal(err)
	}
	if retryRows != 0 {
		t.Fatalf("missing dependency persisted %d Retry Trace rows", retryRows)
	}
	if result, err := ingestor.Ingest(ctx, source); err != nil || result.Chunks[0].Status != collector.ChunkCommitted {
		t.Fatalf("late source Ingest = %#v, %v", result, err)
	}
	reconciled, err := ingestor.Ingest(ctx, retry)
	if err != nil || reconciled.Chunks[0].Status != collector.ChunkCommitted || reconciled.Chunks[0].CommittedThrough != 2 {
		t.Fatalf("dependency retry = %#v, %v", reconciled, err)
	}
}

func TestPostgresStoreSerializesConcurrentIdenticalTraceChunks(t *testing.T) {
	ctx := context.Background()
	pool := openObservabilityTestPool(t, ctx)
	t.Cleanup(pool.Close)
	resetObservabilityTestSchema(t, ctx, pool)
	store := collector.NewPostgresStore(pool)
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	batch := validCollectorBatch(t)
	start := make(chan struct{})
	results := make(chan collector.BatchResult, 2)
	errorsFound := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			result, ingestErr := ingestor.Ingest(ctx, batch)
			if ingestErr != nil {
				errorsFound <- ingestErr
				return
			}
			results <- result
		}()
	}
	close(start)
	workers.Wait()
	close(errorsFound)
	close(results)
	for ingestErr := range errorsFound {
		t.Errorf("concurrent Ingest: %v", ingestErr)
	}
	for result := range results {
		if got := result.Chunks[0]; got.Status != collector.ChunkCommitted || got.CommittedThrough != 2 {
			t.Errorf("concurrent result = %#v", got)
		}
	}
	if t.Failed() {
		return
	}
	stored, err := store.LoadTrace(ctx, "trace-1")
	if err != nil {
		t.Fatalf("LoadTrace: %v", err)
	}
	if stored.CommittedThrough != 2 || len(stored.Records) != 2 {
		t.Fatalf("stored concurrent Trace = %#v", stored)
	}
}

func TestPostgresStoreSerializesConcurrentDirectProducerRecords(t *testing.T) {
	ctx := context.Background()
	pool := openObservabilityTestPool(t, ctx)
	t.Cleanup(pool.Close)
	resetObservabilityTestSchema(t, ctx, pool)
	store := collector.NewPostgresStore(pool)
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerIDPrefix: "nano-", Store: store})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	root := directCollectorBatch(t)
	root.ProducerID = "nano-control-plane/one"
	if _, err := ingestor.Ingest(ctx, root); err != nil {
		t.Fatalf("root Ingest: %v", err)
	}

	start := make(chan struct{})
	results := make(chan collector.BatchResult, 2)
	errorsFound := make(chan error, 2)
	var workers sync.WaitGroup
	for index := range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			batch := directCollectorBatch(t)
			batch.BatchID = fmt.Sprintf("batch-direct-worker-%d", index)
			batch.ProducerID = fmt.Sprintf("nano-worker/%d", index)
			record := batch.Chunks[0].Records[1].Record
			record.IdentityKey = fmt.Sprintf("run/run-1/concurrent/%d", index)
			record.Name = fmt.Sprintf("nano.concurrent.%d", index)
			batch.Chunks[0].Records = []collector.SequencedRecord{collectorEnvelope(t, 0, record)}
			<-start
			result, ingestErr := ingestor.Ingest(ctx, batch)
			if ingestErr != nil {
				errorsFound <- ingestErr
				return
			}
			results <- result
		}()
	}
	close(start)
	workers.Wait()
	close(errorsFound)
	close(results)
	for ingestErr := range errorsFound {
		t.Errorf("concurrent Ingest: %v", ingestErr)
	}
	committed := map[int]bool{}
	for result := range results {
		if got := result.Chunks[0]; got.Status != collector.ChunkCommitted {
			t.Errorf("concurrent result = %#v", got)
		} else {
			committed[got.CommittedThrough] = true
		}
	}
	if t.Failed() {
		return
	}
	if !committed[3] || !committed[4] {
		t.Fatalf("concurrent high watermarks = %#v, want 3 and 4", committed)
	}
	stored, err := store.LoadTrace(ctx, "trace-1")
	if err != nil {
		t.Fatalf("LoadTrace: %v", err)
	}
	if stored.CommittedThrough != 4 || len(stored.Records) != 4 || stored.Records[2].Sequence != 3 || stored.Records[3].Sequence != 4 {
		t.Fatalf("stored direct Trace = %#v", stored)
	}
}

func TestPostgresStoreRejectsRawRecordMutation(t *testing.T) {
	ctx := context.Background()
	pool := openObservabilityTestPool(t, ctx)
	t.Cleanup(pool.Close)
	resetObservabilityTestSchema(t, ctx, pool)
	store := collector.NewPostgresStore(pool)
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	if _, err := ingestor.Ingest(ctx, validCollectorBatch(t)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	if _, err := pool.Exec(ctx, `update obs_trace_records set name = 'tampered' where trace_id = 'trace-1' and sequence = 2`); err == nil {
		t.Fatal("raw Collector record update succeeded, want immutable-store rejection")
	}
	stored, err := store.LoadTrace(ctx, "trace-1")
	if err != nil {
		t.Fatalf("LoadTrace: %v", err)
	}
	if stored.Records[1].Record.Name != "nano.run.admitted" {
		t.Fatalf("raw Collector record changed: %#v", stored.Records[1])
	}
}

func TestPostgresStoreTakesOpaqueReplayCustodyBeforeACKAndReconcilesWithoutStaging(t *testing.T) {
	ctx := context.Background()
	pool := openObservabilityTestPool(t, ctx)
	t.Cleanup(pool.Close)
	resetObservabilityTestSchema(t, ctx, pool)
	producerObjects := objectstore.NewMemoryStore()
	collectorObjects := objectstore.NewMemoryStore()
	ciphertext := bytes.Repeat([]byte{0xa5}, 256)
	if err := producerObjects.Put(ctx, "producer-staging/attachment-1", ciphertext); err != nil {
		t.Fatal(err)
	}
	store, err := collector.NewPostgresStoreWithReplay(pool, producerObjects, collectorObjects)
	if err != nil {
		t.Fatalf("NewPostgresStoreWithReplay: %v", err)
	}
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatal(err)
	}
	batch := collectorBatchWithReplay(t, ciphertext)
	result, err := ingestor.Ingest(ctx, batch)
	if err != nil || result.Chunks[0].Status != collector.ChunkCommitted || result.Chunks[0].CommittedThrough != 2 {
		t.Fatalf("Replay Ingest result=%#v err=%v", result, err)
	}
	var objectKey, ciphertextHash string
	var payloadRows, ciphertextColumns int
	if err := pool.QueryRow(ctx, `
		select object_key, ciphertext_sha256 from obs_payload_refs
		where attachment_id = $1
	`, batch.Chunks[0].Attachments[0].AttachmentID).Scan(&objectKey, &ciphertextHash); err != nil {
		t.Fatalf("load Replay ref: %v", err)
	}
	if err := pool.QueryRow(ctx, `select count(*) from obs_payload_refs where trace_id = 'trace-1'`).Scan(&payloadRows); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		select count(*) from information_schema.columns
		where table_schema = 'public' and table_name = 'obs_payload_refs'
			and column_name in ('ciphertext', 'plaintext', 'payload')
	`).Scan(&ciphertextColumns); err != nil {
		t.Fatal(err)
	}
	storedCiphertext, err := collectorObjects.Get(ctx, objectKey, replay.MaxCiphertextBytes)
	if err != nil {
		t.Fatalf("load Collector Replay object: %v", err)
	}
	if payloadRows != 1 || ciphertextColumns != 0 || ciphertextHash != batch.Chunks[0].Attachments[0].CiphertextSHA256 || !bytes.Equal(storedCiphertext, ciphertext) {
		t.Fatalf("Collector Replay custody rows=%d ciphertext_columns=%d hash=%s bytes=%d", payloadRows, ciphertextColumns, ciphertextHash, len(storedCiphertext))
	}
	if err := producerObjects.Delete(ctx, batch.Chunks[0].Attachments[0].StagingObjectKey); err != nil {
		t.Fatal(err)
	}
	resend, err := ingestor.Ingest(ctx, batch)
	if err != nil || resend.Chunks[0].Status != collector.ChunkCommitted || resend.Chunks[0].CommittedThrough != 2 {
		t.Fatalf("Replay resend without staging result=%#v err=%v", resend, err)
	}
}

func TestPostgresStoreTreatsMissingReplayStagingObjectAsRetryableDependency(t *testing.T) {
	ctx := context.Background()
	pool := openObservabilityTestPool(t, ctx)
	t.Cleanup(pool.Close)
	resetObservabilityTestSchema(t, ctx, pool)
	producerObjects := objectstore.NewMemoryStore()
	collectorObjects := objectstore.NewMemoryStore()
	store, err := collector.NewPostgresStoreWithReplay(pool, producerObjects, collectorObjects)
	if err != nil {
		t.Fatal(err)
	}
	ingestor, _ := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	batch := collectorBatchWithReplay(t, bytes.Repeat([]byte{0xa5}, 256))
	result, err := ingestor.Ingest(ctx, batch)
	if err != nil {
		t.Fatalf("Ingest transport error: %v", err)
	}
	if got := result.Chunks[0]; got.Status != collector.ChunkRetryable || got.Code != collector.CodeAttachmentUnavailable || got.CommittedThrough != 0 {
		t.Fatalf("missing staging result = %#v", got)
	}
	var traces, payloads int
	if err := pool.QueryRow(ctx, `select count(*) from obs_traces`).Scan(&traces); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select count(*) from obs_payload_refs`).Scan(&payloads); err != nil {
		t.Fatal(err)
	}
	if traces != 0 || payloads != 0 || collectorObjects.Len() != 0 {
		t.Fatalf("missing staging persisted traces/payloads/objects = %d/%d/%d", traces, payloads, collectorObjects.Len())
	}
}

func TestPostgresStoreRejectsOversizedReplayStagingObjectAsIntegrityFailure(t *testing.T) {
	ctx := context.Background()
	pool := openObservabilityTestPool(t, ctx)
	t.Cleanup(pool.Close)
	resetObservabilityTestSchema(t, ctx, pool)
	producerObjects := objectstore.NewMemoryStore()
	collectorObjects := objectstore.NewMemoryStore()
	expected := bytes.Repeat([]byte{0xa5}, 256)
	if err := producerObjects.Put(ctx, "producer-staging/attachment-1", append(expected, 0xff)); err != nil {
		t.Fatal(err)
	}
	store, err := collector.NewPostgresStoreWithReplay(pool, producerObjects, collectorObjects)
	if err != nil {
		t.Fatal(err)
	}
	ingestor, _ := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	result, err := ingestor.Ingest(ctx, collectorBatchWithReplay(t, expected))
	if err != nil {
		t.Fatalf("Ingest transport error: %v", err)
	}
	if got := result.Chunks[0]; got.Status != collector.ChunkRejected || got.Code != collector.CodeAttachmentIntegrity || got.CommittedThrough != 0 {
		t.Fatalf("oversized staging result = %#v", got)
	}
	if collectorObjects.Len() != 0 {
		t.Fatalf("oversized staging persisted %d Collector objects", collectorObjects.Len())
	}
}

func collectorBatchWithReplay(t *testing.T, ciphertext []byte) collector.Batch {
	t.Helper()
	batch := validCollectorBatch(t)
	const attachmentID = "019bf000-0000-7000-8000-000000000101"
	batch.Chunks[0].Records[1].Record.Attributes = append(
		batch.Chunks[0].Records[1].Record.Attributes,
		agentobs.String(replay.ModelRequestAttachmentKey, attachmentID),
	)
	batch.Chunks[0].Records[1] = collectorEnvelope(t, 2, batch.Chunks[0].Records[1].Record)
	ciphertextDigest := sha256.Sum256(ciphertext)
	batch.Chunks[0].Attachments = []collector.AttachmentDescriptor{{
		AttachmentID: attachmentID, RecordSequence: 2, Class: replay.ClassModelRequest,
		SchemaVersion: 1, PlaintextSHA256: strings.Repeat("b", 64),
		StagingObjectKey: "producer-staging/attachment-1", CiphertextBytes: len(ciphertext),
		CiphertextSHA256: hex.EncodeToString(ciphertextDigest[:]), Compression: replay.CompressionGZIP,
		Encryption: replay.EncryptionAES256GCM, KeyID: "dev-key-v1",
		WrappedKey: bytes.Repeat([]byte{0xc3}, 60), Nonce: bytes.Repeat([]byte{0xd4}, 12),
		ExpiresAt: time.Now().UTC().Add(7 * 24 * time.Hour),
	}}
	return batch
}

func collectorBatchWithCrossTraceLink(t *testing.T, suffix string, targetTraceID agentobs.TraceID, targetSpanID agentobs.SpanID) collector.Batch {
	t.Helper()
	batch := collectorBatchFor(t, suffix)
	link := batch.Chunks[0].Records[1].Record
	link.Kind = agentobs.RecordLink
	link.Name = semconv.LinkRetriedFrom
	link.TargetTraceID = targetTraceID
	link.TargetSpanID = targetSpanID
	batch.Chunks[0].Records[1] = collectorEnvelope(t, 2, link)
	return batch
}

func TestPostgresStorePersistsUnknownTraceTombstoneAndPurgeWork(t *testing.T) {
	ctx := context.Background()
	pool := openObservabilityTestPool(t, ctx)
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
	})
	resetObservabilityTestSchema(t, ctx, pool)
	store := collector.NewPostgresStore(pool)
	purger, err := collector.NewPurger(collector.PurgerConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewPurger: %v", err)
	}
	batch := collector.PurgeBatch{
		ProtocolVersion: collector.ProtocolVersion, BatchID: "purge-batch-pg", ProducerID: "nano-worker",
		CreatedAt: time.Unix(1_700_000_200, 0).UTC(),
		Commands: []collector.PurgeCommand{{
			CommandID: "purge-command-pg", CommandVersion: 1, Kind: collector.CommandPurgeTrace,
			TraceID: "trace-1", RunID: "run-1", RequestedAt: time.Unix(1_700_000_100, 0).UTC(),
		}},
	}
	if result, err := purger.Purge(ctx, batch); err != nil || result.Commands[0].Status != collector.PurgeAcknowledged {
		t.Fatalf("Purge result = %#v, error = %v", result, err)
	}

	pool.Close()
	pool = nil
	reopened := openObservabilityTestPool(t, ctx)
	t.Cleanup(reopened.Close)
	store = collector.NewPostgresStore(reopened)
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	ingestResult, err := ingestor.Ingest(ctx, validCollectorBatch(t))
	if err != nil {
		t.Fatalf("late Ingest: %v", err)
	}
	if got := ingestResult.Chunks[0]; got.Status != collector.ChunkRejected || got.Code != collector.CodeTombstoned {
		t.Fatalf("late Ingest result = %#v", got)
	}
	var tombstones, purgeWork, traces int
	if err := reopened.QueryRow(ctx, `select count(*) from obs_trace_tombstones where trace_id = 'trace-1'`).Scan(&tombstones); err != nil {
		t.Fatalf("count tombstones: %v", err)
	}
	if err := reopened.QueryRow(ctx, `select count(*) from obs_purge_queue where trace_id = 'trace-1'`).Scan(&purgeWork); err != nil {
		t.Fatalf("count purge work: %v", err)
	}
	if err := reopened.QueryRow(ctx, `select count(*) from obs_traces where trace_id = 'trace-1'`).Scan(&traces); err != nil {
		t.Fatalf("count traces: %v", err)
	}
	if tombstones != 1 || purgeWork != 1 || traces != 0 {
		t.Fatalf("tombstone/purge/trace counts = %d/%d/%d", tombstones, purgeWork, traces)
	}
}

func openObservabilityTestPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("NANO_TEST_OBSERVABILITY_DATABASE_URL")
	if dsn == "" {
		t.Fatal("NANO_TEST_OBSERVABILITY_DATABASE_URL is required for real Collector PostgreSQL integration tests")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open Observability PostgreSQL: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping Observability PostgreSQL: %v", err)
	}
	return pool
}

func resetObservabilityTestSchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `drop schema if exists public cascade; create schema public`); err != nil {
		t.Fatalf("reset Observability schema: %v", err)
	}
	if err := collector.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("run Collector migrations: %v", err)
	}
}

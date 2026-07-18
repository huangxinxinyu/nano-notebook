package collector_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/collector"
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

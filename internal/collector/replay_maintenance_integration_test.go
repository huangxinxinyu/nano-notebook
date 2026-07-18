package collector_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
)

func TestReplayMaintenanceExpiresOnlyObjectBytesAndKeepsTraceHistory(t *testing.T) {
	ctx := context.Background()
	pool := openObservabilityTestPool(t, ctx)
	t.Cleanup(pool.Close)
	resetObservabilityTestSchema(t, ctx, pool)
	producerObjects := objectstore.NewMemoryStore()
	replayObjects := objectstore.NewMemoryStore()
	ciphertext := bytes.Repeat([]byte{0xa5}, 256)
	_ = producerObjects.Put(ctx, "producer-staging/attachment-1", ciphertext)
	store, _ := collector.NewPostgresStoreWithReplay(pool, producerObjects, replayObjects)
	ingestor, _ := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	batch := collectorBatchWithReplay(t, ciphertext)
	batch.Chunks[0].Attachments[0].ExpiresAt = time.Now().UTC().Add(-time.Minute)
	if result, err := ingestor.Ingest(ctx, batch); err != nil || result.Chunks[0].Status != collector.ChunkCommitted {
		t.Fatalf("Ingest = %#v, %v", result, err)
	}
	maintenance, err := collector.NewReplayMaintenance(pool, replayObjects, collector.ReplayMaintenanceConfig{BatchSize: 16, OrphanGrace: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	result, err := maintenance.RunOnce(ctx)
	if err != nil || result.Expired != 1 {
		t.Fatalf("RunOnce = %#v, %v", result, err)
	}
	var state string
	var traces, records int
	if err := pool.QueryRow(ctx, `select state from obs_payload_refs where trace_id = 'trace-1'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	_ = pool.QueryRow(ctx, `select count(*) from obs_traces`).Scan(&traces)
	_ = pool.QueryRow(ctx, `select count(*) from obs_trace_records`).Scan(&records)
	if state != "expired" || traces != 1 || records != 2 || replayObjects.Len() != 0 {
		t.Fatalf("expired state=%s traces=%d records=%d objects=%d", state, traces, records, replayObjects.Len())
	}
}

func TestReplayMaintenancePurgesObjectsAndTraceContentBehindTombstone(t *testing.T) {
	ctx := context.Background()
	pool := openObservabilityTestPool(t, ctx)
	t.Cleanup(pool.Close)
	resetObservabilityTestSchema(t, ctx, pool)
	producerObjects := objectstore.NewMemoryStore()
	replayObjects := objectstore.NewMemoryStore()
	ciphertext := bytes.Repeat([]byte{0xa5}, 256)
	_ = producerObjects.Put(ctx, "producer-staging/attachment-1", ciphertext)
	store, _ := collector.NewPostgresStoreWithReplay(pool, producerObjects, replayObjects)
	ingestor, _ := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	batch := collectorBatchWithReplay(t, ciphertext)
	if _, err := ingestor.Ingest(ctx, batch); err != nil {
		t.Fatal(err)
	}
	purger, _ := collector.NewPurger(collector.PurgerConfig{ProducerID: "nano-worker", Store: store})
	purgeBatch := collector.PurgeBatch{
		ProtocolVersion: collector.ProtocolVersion, BatchID: "purge-replay", ProducerID: "nano-worker", CreatedAt: time.Now().UTC(),
		Commands: []collector.PurgeCommand{{
			CommandID: "purge-replay-command", CommandVersion: 1, Kind: collector.CommandPurgeTrace,
			TraceID: "trace-1", RunID: "run-1", RequestedAt: time.Now().UTC(),
		}},
	}
	if result, err := purger.Purge(ctx, purgeBatch); err != nil || result.Commands[0].Status != collector.PurgeAcknowledged {
		t.Fatalf("Purge = %#v, %v", result, err)
	}
	maintenance, _ := collector.NewReplayMaintenance(pool, replayObjects, collector.ReplayMaintenanceConfig{BatchSize: 16, OrphanGrace: time.Hour})
	for range 2 {
		if _, err := maintenance.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var traces, records, refs int
	var stage string
	_ = pool.QueryRow(ctx, `select count(*) from obs_traces`).Scan(&traces)
	_ = pool.QueryRow(ctx, `select count(*) from obs_trace_records`).Scan(&records)
	_ = pool.QueryRow(ctx, `select count(*) from obs_payload_refs`).Scan(&refs)
	if err := pool.QueryRow(ctx, `select stage from obs_purge_queue where trace_id = 'trace-1'`).Scan(&stage); err != nil {
		t.Fatal(err)
	}
	if traces != 0 || records != 0 || refs != 0 || replayObjects.Len() != 0 || stage != "content_removed" {
		t.Fatalf("purged traces=%d records=%d refs=%d objects=%d stage=%s", traces, records, refs, replayObjects.Len(), stage)
	}
	if result, err := ingestor.Ingest(ctx, batch); err != nil || result.Chunks[0].Code != collector.CodeTombstoned {
		t.Fatalf("late Ingest = %#v, %v", result, err)
	}
}

func TestReplayMaintenanceDeletesOnlyOldUnreferencedCollectorObjects(t *testing.T) {
	ctx := context.Background()
	pool := openObservabilityTestPool(t, ctx)
	t.Cleanup(pool.Close)
	resetObservabilityTestSchema(t, ctx, pool)
	replayObjects := objectstore.NewMemoryStore()
	if err := replayObjects.Put(ctx, "agent-replay/orphan", []byte("opaque")); err != nil {
		t.Fatal(err)
	}
	maintenance, _ := collector.NewReplayMaintenance(pool, replayObjects, collector.ReplayMaintenanceConfig{
		BatchSize: 16, OrphanGrace: time.Hour, Now: func() time.Time { return time.Now().UTC().Add(2 * time.Hour) },
	})
	result, err := maintenance.RunOnce(ctx)
	if err != nil || result.OrphansDeleted != 1 || replayObjects.Len() != 0 {
		t.Fatalf("RunOnce = %#v objects=%d err=%v", result, replayObjects.Len(), err)
	}
}

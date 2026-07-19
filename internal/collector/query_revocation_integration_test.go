package collector_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReplayQueryRejectsExpiredPayloadBeforeMaintenanceRuns(t *testing.T) {
	ctx := context.Background()
	pool := openObservabilityTestPool(t, ctx)
	t.Cleanup(pool.Close)
	resetObservabilityTestSchema(t, ctx, pool)
	producerObjects := objectstore.NewMemoryStore()
	replayObjects := objectstore.NewMemoryStore()
	ciphertext := bytes.Repeat([]byte{0xa5}, 256)
	if err := producerObjects.Put(ctx, "producer-staging/attachment-1", ciphertext); err != nil {
		t.Fatal(err)
	}
	store, err := collector.NewPostgresStoreWithReplay(pool, producerObjects, replayObjects)
	if err != nil {
		t.Fatal(err)
	}
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatal(err)
	}
	batch := collectorBatchWithReplay(t, ciphertext)
	batch.Chunks[0].Attachments[0].ExpiresAt = time.Now().UTC().Add(-time.Minute)
	if result, err := ingestor.Ingest(ctx, batch); err != nil || result.Chunks[0].Status != collector.ChunkCommitted {
		t.Fatalf("Ingest = %#v, %v", result, err)
	}
	queries, err := collector.NewTraceQueryStore(pool, replayObjects)
	if err != nil {
		t.Fatal(err)
	}
	attachment := batch.Chunks[0].Attachments[0]
	_, err = queries.Replay(ctx, batch.Chunks[0].Trace.TraceID, batch.Chunks[0].Records[1].Record.SpanID, attachment.AttachmentID)
	if !errors.Is(err, collector.ErrReplayExpired) {
		t.Fatalf("expired Replay query error = %v, want ErrReplayExpired", err)
	}
	if replayObjects.Len() != 1 {
		t.Fatalf("query mutated expired Replay custody objects = %d, want 1 before maintenance", replayObjects.Len())
	}
}

func TestReplayQueryAndTombstoneHaveLinearizableAccessBoundary(t *testing.T) {
	ctx := context.Background()
	pool := openObservabilityTestPool(t, ctx)
	t.Cleanup(pool.Close)
	resetObservabilityTestSchema(t, ctx, pool)
	producerObjects := objectstore.NewMemoryStore()
	replayObjects := objectstore.NewMemoryStore()
	blockingObjects := &blockingReplayGetStore{
		Store: replayObjects, entered: make(chan struct{}), release: make(chan struct{}),
	}
	ciphertext := bytes.Repeat([]byte{0xb6}, 256)
	if err := producerObjects.Put(ctx, "producer-staging/attachment-1", ciphertext); err != nil {
		t.Fatal(err)
	}
	store, err := collector.NewPostgresStoreWithReplay(pool, producerObjects, replayObjects)
	if err != nil {
		t.Fatal(err)
	}
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatal(err)
	}
	batch := collectorBatchWithReplay(t, ciphertext)
	if result, err := ingestor.Ingest(ctx, batch); err != nil || result.Chunks[0].Status != collector.ChunkCommitted {
		t.Fatalf("Ingest = %#v, %v", result, err)
	}
	queries, err := collector.NewTraceQueryStore(pool, blockingObjects)
	if err != nil {
		t.Fatal(err)
	}
	attachment := batch.Chunks[0].Attachments[0]
	type replayResult struct {
		payload collector.OpaqueReplay
		err     error
	}
	replayDone := make(chan replayResult, 1)
	go func() {
		payload, replayErr := queries.Replay(ctx, batch.Chunks[0].Trace.TraceID, batch.Chunks[0].Records[1].Record.SpanID, attachment.AttachmentID)
		replayDone <- replayResult{payload: payload, err: replayErr}
	}()
	select {
	case <-blockingObjects.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Replay query did not reach object custody read")
	}

	purger, err := collector.NewPurger(collector.PurgerConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatal(err)
	}
	purgeDone := make(chan error, 1)
	go func() {
		result, purgeErr := purger.Purge(ctx, collector.PurgeBatch{
			ProtocolVersion: collector.ProtocolVersion, BatchID: "purge-query-race", ProducerID: "nano-worker", CreatedAt: time.Now().UTC(),
			Commands: []collector.PurgeCommand{{
				CommandID: "purge-query-race-command", CommandVersion: 1, Kind: collector.CommandPurgeTrace,
				TraceID: batch.Chunks[0].Trace.TraceID, RunID: batch.Chunks[0].Trace.RunID, RequestedAt: time.Now().UTC(),
			}},
		})
		if purgeErr == nil && (len(result.Commands) != 1 || result.Commands[0].Status != collector.PurgeAcknowledged) {
			purgeErr = fmt.Errorf("unexpected purge result %#v", result)
		}
		purgeDone <- purgeErr
	}()
	waitForBlockedTombstone(t, ctx, pool, purgeDone)

	close(blockingObjects.release)
	select {
	case result := <-replayDone:
		if result.err != nil || !bytes.Equal(result.payload.Sealed.Ciphertext, ciphertext) {
			t.Fatalf("in-flight Replay = %#v, %v", result.payload, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight Replay did not finish after object read was released")
	}
	select {
	case err := <-purgeDone:
		if err != nil {
			t.Fatalf("Purge after Replay: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Purge did not finish after Replay query released its access boundary")
	}
	if _, err := queries.Replay(ctx, batch.Chunks[0].Trace.TraceID, batch.Chunks[0].Records[1].Record.SpanID, attachment.AttachmentID); !errors.Is(err, collector.ErrReplayNotFound) {
		t.Fatalf("Replay after tombstone error = %v, want ErrReplayNotFound", err)
	}
}

func TestTraceDetailAndTombstoneHaveLinearizableAccessBoundary(t *testing.T) {
	ctx := context.Background()
	pool := openObservabilityTestPool(t, ctx)
	t.Cleanup(pool.Close)
	resetObservabilityTestSchema(t, ctx, pool)
	stored := projectionStoredTrace(t, true, true)
	store := collector.NewPostgresStore(pool)
	if _, err := store.CommitTraceChunk(ctx, collector.TraceChunk{
		Trace: stored.Trace, FirstSequence: 1, Records: stored.Records,
	}); err != nil {
		t.Fatalf("commit Detail race Trace: %v", err)
	}
	projector, err := collector.NewProjector(pool, collector.ProjectorConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if projected, err := projector.RunOnce(ctx); err != nil || !projected {
		t.Fatalf("project Detail race Trace projected=%t err=%v", projected, err)
	}
	queries, err := collector.NewTraceQueryStore(pool, nil)
	if err != nil {
		t.Fatal(err)
	}

	spanBlock, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = spanBlock.Rollback(context.Background()) })
	if _, err := spanBlock.Exec(ctx, `lock table obs_spans in access exclusive mode`); err != nil {
		t.Fatalf("lock projected Spans for Detail race: %v", err)
	}
	type detailResult struct {
		detail collector.ProjectedTrace
		err    error
	}
	detailDone := make(chan detailResult, 1)
	go func() {
		detail, detailErr := queries.Detail(ctx, stored.Trace.TraceID)
		detailDone <- detailResult{detail: detail, err: detailErr}
	}()
	waitForBlockedDetailChildren(t, ctx, pool)

	purger, err := collector.NewPurger(collector.PurgerConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatal(err)
	}
	purgeDone := make(chan error, 1)
	go func() {
		result, purgeErr := purger.Purge(ctx, collector.PurgeBatch{
			ProtocolVersion: collector.ProtocolVersion, BatchID: "purge-detail-race", ProducerID: "nano-worker", CreatedAt: time.Now().UTC(),
			Commands: []collector.PurgeCommand{{
				CommandID: "purge-detail-race-command", CommandVersion: 1, Kind: collector.CommandPurgeTrace,
				TraceID: stored.Trace.TraceID, RunID: stored.Trace.RunID, RequestedAt: time.Now().UTC(),
			}},
		})
		if purgeErr == nil && (len(result.Commands) != 1 || result.Commands[0].Status != collector.PurgeAcknowledged) {
			purgeErr = fmt.Errorf("unexpected purge result %#v", result)
		}
		purgeDone <- purgeErr
	}()
	waitForBlockedTombstone(t, ctx, pool, purgeDone)
	if err := spanBlock.Commit(ctx); err != nil {
		t.Fatalf("release projected Span lock: %v", err)
	}
	select {
	case result := <-detailDone:
		if result.err != nil || result.detail.Projection.Summary.TraceID != stored.Trace.TraceID {
			t.Fatalf("in-flight Detail = %#v, %v", result.detail, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight Detail did not finish after child rows were released")
	}
	select {
	case err := <-purgeDone:
		if err != nil {
			t.Fatalf("Purge after Detail: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Purge did not finish after Detail released its access boundary")
	}
	if _, err := queries.Detail(ctx, stored.Trace.TraceID); !errors.Is(err, collector.ErrTraceNotFound) {
		t.Fatalf("Detail after tombstone error = %v, want ErrTraceNotFound", err)
	}
}

type blockingReplayGetStore struct {
	objectstore.Store
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingReplayGetStore) Get(ctx context.Context, key string, maxBytes int64) ([]byte, error) {
	s.once.Do(func() { close(s.entered) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.release:
		return s.Store.Get(ctx, key, maxBytes)
	}
}

func waitForBlockedTombstone(t *testing.T, ctx context.Context, pool *pgxpool.Pool, purgeDone <-chan error) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-purgeDone:
			t.Fatalf("tombstone completed while the query still held its access boundary: %v", err)
		case <-ticker.C:
			var blocked bool
			err := pool.QueryRow(ctx, `
				select exists(
					select 1 from pg_stat_activity
					where datname = current_database()
					  and wait_event_type = 'Lock'
					  and query like '%update obs_traces set tombstoned_at%'
				)
			`).Scan(&blocked)
			if err != nil {
				t.Fatalf("inspect blocked tombstone: %v", err)
			}
			if blocked {
				return
			}
		case <-deadline.C:
			t.Fatal("tombstone did not reach the Replay query access boundary")
		}
	}
}

func waitForBlockedDetailChildren(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			var blocked bool
			err := pool.QueryRow(ctx, `
				select exists(
					select 1 from pg_stat_activity
					where datname = current_database()
					  and wait_event_type = 'Lock'
					  and query like '%from obs_spans where trace_id%'
				)
			`).Scan(&blocked)
			if err != nil {
				t.Fatalf("inspect blocked Detail child query: %v", err)
			}
			if blocked {
				return
			}
		case <-deadline.C:
			t.Fatal("Detail query did not reach blocked projected children")
		}
	}
}

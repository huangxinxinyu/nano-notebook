package app_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentoutbox"
	"github.com/huangxinxinyu/nano-notebook/internal/app"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCollectorTraceMigrationVerifierMatchesLegacyAuthorityAfterOutboxCleanup(t *testing.T) {
	api, collectorPool, traceID := newTraceMigrationVerifierFixture(t)
	ctx := context.Background()
	if _, err := api.db.Pool().Exec(ctx, `delete from agentobs_outbox_records where trace_id = $1`, traceID); err != nil {
		t.Fatalf("remove delivered Outbox records: %v", err)
	}

	report, err := app.VerifyCollectorTraceMigration(ctx, api.db.Pool(), collectorPool)
	if err != nil {
		t.Fatalf("VerifyCollectorTraceMigration: %v", err)
	}
	if report.TraceCount != 1 || report.RecordCount != 2 {
		t.Fatalf("migration verification report = %#v, want 1 Trace and 2 records", report)
	}
}

func TestCollectorTraceMigrationVerifierRejectsCanonicalHashDrift(t *testing.T) {
	api, collectorPool, traceID := newTraceMigrationVerifierFixture(t)
	ctx := context.Background()
	if _, err := collectorPool.Exec(ctx, `alter table obs_trace_records disable trigger obs_trace_records_immutable_update`); err != nil {
		t.Fatalf("disable immutable trigger for drift fixture: %v", err)
	}
	t.Cleanup(func() {
		_, _ = collectorPool.Exec(context.Background(), `alter table obs_trace_records enable trigger obs_trace_records_immutable_update`)
	})
	if _, err := collectorPool.Exec(ctx, `
		update obs_trace_records set canonical_sha256 = repeat('0', 64)
		where trace_id = $1 and sequence = 1
	`, traceID); err != nil {
		t.Fatalf("corrupt Collector canonical hash: %v", err)
	}

	_, err := app.VerifyCollectorTraceMigration(ctx, api.db.Pool(), collectorPool)
	if err == nil || !strings.Contains(err.Error(), "canonical hash drift") {
		t.Fatalf("VerifyCollectorTraceMigration error = %v, want canonical hash drift", err)
	}
}

func TestCollectorTraceMigrationVerifierRejectsMissingCollectorRecord(t *testing.T) {
	api, collectorPool, traceID := newTraceMigrationVerifierFixture(t)
	ctx := context.Background()
	if _, err := collectorPool.Exec(ctx, `delete from obs_trace_records where trace_id = $1 and sequence = 2`, traceID); err != nil {
		t.Fatalf("remove Collector record: %v", err)
	}

	_, err := app.VerifyCollectorTraceMigration(ctx, api.db.Pool(), collectorPool)
	if err == nil || !strings.Contains(err.Error(), "record count drift") {
		t.Fatalf("VerifyCollectorTraceMigration error = %v, want record count drift", err)
	}
}

func newTraceMigrationVerifierFixture(t *testing.T) (*testAPI, *pgxpool.Pool, string) {
	t.Helper()
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-migration-verifier@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c490")
	ctx := context.Background()
	var traceID string
	if err := api.db.Pool().QueryRow(ctx, `select trace_id from agent_trace_refs where run_id = $1`, runID).Scan(&traceID); err != nil {
		t.Fatalf("load admitted Trace ID: %v", err)
	}
	if _, err := api.db.Pool().Exec(ctx, `
		insert into agent_traces(trace_id, run_id, root_span_id, schema_version, created_at)
		select trace_id, run_id, root_span_id, schema_version, created_at
		from agent_trace_refs where trace_id = $1
	`, traceID); err != nil {
		t.Fatalf("seed legacy Trace envelope: %v", err)
	}
	if _, err := api.db.Pool().Exec(ctx, `
		insert into agent_trace_records(
			trace_id, sequence_no, identity_key, record_kind, span_id, parent_span_id,
			name, target_trace_id, target_span_id, occurred_at, payload_version,
			payload, payload_sha256, created_at
		)
		select trace_id, sequence_no, identity_key, record_kind, span_id, parent_span_id,
			name, target_trace_id, target_span_id, occurred_at, payload_version,
			payload, payload_sha256, created_at
		from agentobs_outbox_records where trace_id = $1 order by sequence_no
	`, traceID); err != nil {
		t.Fatalf("seed legacy Trace records: %v", err)
	}

	collectorPool := openTraceMigrationCollectorPool(t, ctx)
	outboxStore, err := agentoutbox.NewPostgresStore(api.db.Pool(), agentoutbox.Config{
		ProducerID: "nano-worker", MaxRecords: 128, MaxEncodedBytes: 512 * 1024,
		MaxTraces: 16, LeaseDuration: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("configure migration Outbox Store: %v", err)
	}
	claimed, ok, err := outboxStore.ClaimBatch(ctx)
	if err != nil || !ok {
		t.Fatalf("claim migration Batch = %#v ok=%t err=%v", claimed, ok, err)
	}
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{
		ProducerID: "nano-worker", Store: collector.NewPostgresStore(collectorPool),
	})
	if err != nil {
		t.Fatalf("configure migration Collector: %v", err)
	}
	result, err := ingestor.Ingest(ctx, claimed.Batch)
	if err != nil || len(result.Chunks) != 1 || result.Chunks[0].Status != collector.ChunkCommitted {
		t.Fatalf("ingest migration Batch = %#v err=%v", result, err)
	}
	return api, collectorPool, traceID
}

func openTraceMigrationCollectorPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("NANO_TEST_MIGRATION_OBSERVABILITY_DATABASE_URL")
	if dsn == "" {
		t.Fatal("NANO_TEST_MIGRATION_OBSERVABILITY_DATABASE_URL is required for migration verifier integration tests")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open migration Collector database: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `drop schema if exists public cascade; create schema public`); err != nil {
		t.Fatalf("reset migration Collector schema: %v", err)
	}
	if err := collector.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("migrate migration Collector schema: %v", err)
	}
	return pool
}

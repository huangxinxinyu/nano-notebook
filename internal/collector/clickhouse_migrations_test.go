package collector

import (
	"context"
	"strings"
	"testing"
)

func TestClickHouseMigrationsDefineDurableRawRecordContract(t *testing.T) {
	sql := ClickHouseMigrationsSQL()
	for _, required := range []string{
		"create table if not exists obs_trace_records_raw",
		"canonical_sha256 FixedString(64)",
		"source_topic LowCardinality(String)",
		"source_partition Int32",
		"source_offset Int64",
		"ingest_version UInt64",
		"ENGINE = ReplacingMergeTree(ingest_version)",
		"PARTITION BY toYYYYMM(occurred_at)",
		"ORDER BY (notebook_id, trace_id, identity_key, sequence)",
		"TTL occurred_at + INTERVAL 90 DAY DELETE",
		"CODEC(ZSTD(3))",
		"create table if not exists obs_trace_summaries",
		"models Array(LowCardinality(String))",
		"providers Array(LowCardinality(String))",
		"cached_tokens Nullable(Int64)",
		"error_code LowCardinality(String)",
		"delegation_targets Array(LowCardinality(String))",
		"rag_degradations Array(LowCardinality(String))",
		"projected_sequence UInt32",
		"ENGINE = ReplacingMergeTree(ingest_version)",
		"ORDER BY trace_id",
		"create table if not exists obs_span_analytics",
		"tool_name LowCardinality(String)",
		"retryable Nullable(Bool)",
		"ORDER BY (notebook_id, trace_id, span_id)",
		"ADD COLUMN IF NOT EXISTS input_tokens Nullable(Int64) AFTER retryable",
		"ADD COLUMN IF NOT EXISTS tools_sha256 String DEFAULT '' AFTER system_sha256",
		"create table if not exists obs_replay_payload_refs",
		"metadata_sha256 FixedString(64)",
		"ORDER BY (attachment_id, metadata_sha256)",
		"create table if not exists obs_trace_tombstones",
		"ORDER BY (trace_id, command_sha256)",
		"create table if not exists obs_trace_purge_state",
		"ORDER BY (command_id, command_sha256)",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("ClickHouse migrations are missing %q", required)
		}
	}
}

func TestRunClickHouseMigrationsExecutesSchema(t *testing.T) {
	executor := &recordingClickHouseMigrationExecutor{}
	if err := RunClickHouseMigrations(context.Background(), executor); err != nil {
		t.Fatal(err)
	}
	if len(executor.queries) != len(clickHouseMigrationStatements) {
		t.Fatalf("ClickHouse migration queries=%d want=%d", len(executor.queries), len(clickHouseMigrationStatements))
	}
	for index, query := range executor.queries {
		if query != clickHouseMigrationStatements[index] {
			t.Fatalf("ClickHouse migration query %d changed", index)
		}
	}
}

type recordingClickHouseMigrationExecutor struct {
	queries []string
}

func (e *recordingClickHouseMigrationExecutor) Exec(_ context.Context, query string, _ ...any) error {
	e.queries = append(e.queries, query)
	return nil
}

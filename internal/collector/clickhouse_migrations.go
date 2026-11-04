package collector

import (
	"context"
	"errors"
)

type clickHouseMigrationExecutor interface {
	Exec(context.Context, string, ...any) error
}

func RunClickHouseMigrations(ctx context.Context, executor clickHouseMigrationExecutor) error {
	if executor == nil {
		return errors.New("nil Collector ClickHouse migration executor")
	}
	for _, statement := range clickHouseMigrationStatements {
		if err := executor.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// ClickHouseMigrationsSQL returns the idempotent schema for retained Durable
// Agent Trace facts. Raw rows repeat their Trace descriptor intentionally so a
// retained fact never depends on a mutable parent row.
func ClickHouseMigrationsSQL() string {
	return clickHouseRawMigrationSQL + "\n" + clickHouseSummaryMigrationSQL + "\n" + clickHouseAnalyticsColumnsMigrationSQL + "\n" + clickHouseSpanAnalyticsMigrationSQL + "\n" + clickHouseSpanPrefixColumnsMigrationSQL + "\n" + clickHouseReplayRefsMigrationSQL + "\n" + clickHouseTombstonesMigrationSQL + "\n" + clickHousePurgeStateMigrationSQL
}

var clickHouseMigrationStatements = []string{
	clickHouseRawMigrationSQL, clickHouseSummaryMigrationSQL, clickHouseAnalyticsColumnsMigrationSQL, clickHouseSpanAnalyticsMigrationSQL,
	clickHouseSpanPrefixColumnsMigrationSQL, clickHouseReplayRefsMigrationSQL,
	clickHouseTombstonesMigrationSQL, clickHousePurgeStateMigrationSQL,
}

const clickHouseRawMigrationSQL = `
create table if not exists obs_trace_records_raw (
	trace_id String CODEC(ZSTD(3)),
	workload_kind LowCardinality(String),
	workload_id String CODEC(ZSTD(3)),
	run_id String CODEC(ZSTD(3)),
	chat_id String CODEC(ZSTD(3)),
	notebook_id String CODEC(ZSTD(3)),
	root_span_id String CODEC(ZSTD(3)),
	agent_name LowCardinality(String),
	trace_schema_version UInt16,
	semantic_convention_version UInt16,
	sequence UInt32,
	identity_key String CODEC(ZSTD(3)),
	kind LowCardinality(String),
	span_id String CODEC(ZSTD(3)),
	parent_span_id String CODEC(ZSTD(3)),
	target_trace_id String CODEC(ZSTD(3)),
	target_span_id String CODEC(ZSTD(3)),
	name String CODEC(ZSTD(3)),
	status LowCardinality(String),
	occurred_at DateTime64(9, 'UTC'),
	occurred_at_unix_nano Int64,
	payload_version UInt16,
	canonical_payload String CODEC(ZSTD(3)),
	canonical_sha256 FixedString(64),
	source_topic LowCardinality(String),
	source_partition Int32,
	source_offset Int64,
	ingest_version UInt64,
	ingested_at DateTime64(9, 'UTC') DEFAULT now64(9)
)
ENGINE = ReplacingMergeTree(ingest_version)
PARTITION BY toYYYYMM(occurred_at)
ORDER BY (notebook_id, trace_id, identity_key, sequence)
TTL occurred_at + INTERVAL 90 DAY DELETE
SETTINGS index_granularity = 8192;
`

const clickHouseSummaryMigrationSQL = `
create table if not exists obs_trace_summaries (
	trace_id String CODEC(ZSTD(3)),
	workload_kind LowCardinality(String),
	workload_id String CODEC(ZSTD(3)),
	run_id String CODEC(ZSTD(3)),
	chat_id String CODEC(ZSTD(3)),
	notebook_id String CODEC(ZSTD(3)),
	root_span_id String CODEC(ZSTD(3)),
	agent_name LowCardinality(String),
	started_at DateTime64(9, 'UTC'),
	started_at_unix_nano Int64,
	last_observed_unix_nano Int64,
	ended_at_unix_nano Nullable(Int64),
	duration_nanoseconds Nullable(Int64),
	status LowCardinality(String),
	active Bool,
	models Array(LowCardinality(String)),
	input_tokens Nullable(Int64),
	output_tokens Nullable(Int64),
	total_tokens Nullable(Int64),
	cost_known Bool,
	cost_amount Nullable(Float64),
	cost_currency LowCardinality(String),
	cost_source LowCardinality(String),
	attempt_count UInt32,
	providers Array(LowCardinality(String)),
	cached_tokens Nullable(Int64),
	reasoning_tokens Nullable(Int64),
	error_code LowCardinality(String),
	stop_reason LowCardinality(String),
	agent_definition LowCardinality(String),
	prompt_version LowCardinality(String),
	configuration_version LowCardinality(String),
	delegation_targets Array(LowCardinality(String)),
	delegation_outcomes Array(LowCardinality(String)),
	rag_stages Array(LowCardinality(String)),
	rag_degradations Array(LowCardinality(String)),
	citation_outcomes Array(LowCardinality(String)),
	committed_sequence UInt32,
	projected_sequence UInt32,
	ingest_version UInt64,
	projected_at DateTime64(9, 'UTC') DEFAULT now64(9)
)
ENGINE = ReplacingMergeTree(ingest_version)
PARTITION BY toYYYYMM(started_at)
ORDER BY trace_id
TTL started_at + INTERVAL 90 DAY DELETE
SETTINGS index_granularity = 8192;
`

const clickHouseAnalyticsColumnsMigrationSQL = `
ALTER TABLE obs_trace_summaries
	ADD COLUMN IF NOT EXISTS providers Array(LowCardinality(String)) AFTER attempt_count,
	ADD COLUMN IF NOT EXISTS cached_tokens Nullable(Int64) AFTER providers,
	ADD COLUMN IF NOT EXISTS reasoning_tokens Nullable(Int64) AFTER cached_tokens,
	ADD COLUMN IF NOT EXISTS error_code LowCardinality(String) AFTER reasoning_tokens,
	ADD COLUMN IF NOT EXISTS stop_reason LowCardinality(String) AFTER error_code,
	ADD COLUMN IF NOT EXISTS agent_definition LowCardinality(String) AFTER stop_reason,
	ADD COLUMN IF NOT EXISTS prompt_version LowCardinality(String) AFTER agent_definition,
	ADD COLUMN IF NOT EXISTS configuration_version LowCardinality(String) AFTER prompt_version,
	ADD COLUMN IF NOT EXISTS delegation_targets Array(LowCardinality(String)) AFTER configuration_version,
	ADD COLUMN IF NOT EXISTS delegation_outcomes Array(LowCardinality(String)) AFTER delegation_targets,
	ADD COLUMN IF NOT EXISTS rag_stages Array(LowCardinality(String)) AFTER delegation_outcomes,
	ADD COLUMN IF NOT EXISTS rag_degradations Array(LowCardinality(String)) AFTER rag_stages,
	ADD COLUMN IF NOT EXISTS citation_outcomes Array(LowCardinality(String)) AFTER rag_degradations;
`

const clickHouseSpanAnalyticsMigrationSQL = `
create table if not exists obs_span_analytics (
	trace_id String CODEC(ZSTD(3)),
	notebook_id String CODEC(ZSTD(3)),
	agent_name LowCardinality(String),
	started_at DateTime64(9, 'UTC'),
	span_id String CODEC(ZSTD(3)),
	span_kind LowCardinality(String),
	name LowCardinality(String),
	tool_name LowCardinality(String),
	status LowCardinality(String),
	outcome LowCardinality(String),
	duration_nanoseconds Nullable(Int64),
	provider LowCardinality(String),
	requested_model LowCardinality(String),
	selected_model LowCardinality(String),
	cached_tokens Nullable(Int64),
	reasoning_tokens Nullable(Int64),
	error_code LowCardinality(String),
	retryable Nullable(Bool),
	ingest_version UInt64,
	projected_at DateTime64(9, 'UTC') DEFAULT now64(9)
)
ENGINE = ReplacingMergeTree(ingest_version)
PARTITION BY toYYYYMM(started_at)
ORDER BY (notebook_id, trace_id, span_id)
TTL started_at + INTERVAL 90 DAY DELETE
SETTINGS index_granularity = 8192;
`

// clickHouseSpanPrefixColumnsMigrationSQL adds the per-decision prefix-cache
// diagnostics to model span rows; rows written before it read as NULL or empty.
const clickHouseSpanPrefixColumnsMigrationSQL = `
ALTER TABLE obs_span_analytics
	ADD COLUMN IF NOT EXISTS input_tokens Nullable(Int64) AFTER retryable,
	ADD COLUMN IF NOT EXISTS decision_ordinal Nullable(Int64) AFTER input_tokens,
	ADD COLUMN IF NOT EXISTS span_started_unix_nano Int64 DEFAULT 0 AFTER decision_ordinal,
	ADD COLUMN IF NOT EXISTS system_sha256 String DEFAULT '' AFTER span_started_unix_nano,
	ADD COLUMN IF NOT EXISTS tools_sha256 String DEFAULT '' AFTER system_sha256;
`

const clickHouseReplayRefsMigrationSQL = `
create table if not exists obs_replay_payload_refs (
	attachment_id String CODEC(ZSTD(3)),
	metadata_sha256 FixedString(64),
	trace_id String CODEC(ZSTD(3)),
	record_sequence UInt32,
	class LowCardinality(String),
	schema_version UInt16,
	plaintext_sha256 FixedString(64),
	object_key String CODEC(ZSTD(3)),
	ciphertext_bytes UInt32,
	ciphertext_sha256 FixedString(64),
	compression LowCardinality(String),
	encryption LowCardinality(String),
	key_id LowCardinality(String),
	wrapped_key String,
	nonce String,
	expires_at DateTime64(9, 'UTC'),
	expires_at_unix_nano Int64,
	state LowCardinality(String),
	source_topic LowCardinality(String),
	source_partition Int32,
	source_offset Int64,
	ingest_version UInt64,
	stored_at DateTime64(9, 'UTC') DEFAULT now64(9)
)
ENGINE = ReplacingMergeTree(ingest_version)
ORDER BY (attachment_id, metadata_sha256)
SETTINGS index_granularity = 8192;
`

const clickHouseTombstonesMigrationSQL = `
create table if not exists obs_trace_tombstones (
	trace_id String CODEC(ZSTD(3)),
	command_sha256 FixedString(64),
	command_id String CODEC(ZSTD(3)),
	command_version UInt16,
	run_id String CODEC(ZSTD(3)),
	producer_id LowCardinality(String),
	requested_at DateTime64(9, 'UTC'),
	requested_at_unix_nano Int64,
	source_topic LowCardinality(String),
	source_partition Int32,
	source_offset Int64,
	tombstone_version UInt64,
	tombstoned_at DateTime64(9, 'UTC') DEFAULT now64(9)
)
ENGINE = ReplacingMergeTree(tombstone_version)
ORDER BY (trace_id, command_sha256)
SETTINGS index_granularity = 8192;
`

const clickHousePurgeStateMigrationSQL = `
create table if not exists obs_trace_purge_state (
	command_id String CODEC(ZSTD(3)),
	command_sha256 FixedString(64),
	trace_id String CODEC(ZSTD(3)),
	stage LowCardinality(String),
	tombstone_ack Bool,
	replay_objects_ack Bool,
	replay_refs_ack Bool,
	raw_records_ack Bool,
	summaries_ack Bool,
	span_analytics_ack Bool,
	source_topic LowCardinality(String),
	source_partition Int32,
	source_offset Int64,
	state_version UInt64,
	updated_at DateTime64(9, 'UTC') DEFAULT now64(9)
)
ENGINE = ReplacingMergeTree(state_version)
ORDER BY (command_id, command_sha256)
SETTINGS index_granularity = 8192;
`

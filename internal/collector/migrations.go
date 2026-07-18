package collector

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

func RunMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return errors.New("nil Collector database pool")
	}
	_, err := pool.Exec(ctx, collectorMigrationsSQL)
	return err
}

const collectorMigrationsSQL = `
create table if not exists obs_traces (
	trace_id text primary key check (char_length(trace_id) between 1 and 128),
	run_id text not null check (char_length(run_id) between 1 and 128),
	chat_id text not null check (char_length(chat_id) between 1 and 128),
	notebook_id text not null check (char_length(notebook_id) between 1 and 128),
	root_span_id text not null check (char_length(root_span_id) between 1 and 128),
	agent_name text not null check (char_length(agent_name) between 1 and 160),
	schema_version integer not null check (schema_version >= 1),
	semantic_convention_version integer not null check (semantic_convention_version >= 1),
	committed_sequence integer not null default 0 check (committed_sequence >= 0),
	projected_sequence integer not null default 0 check (projected_sequence between 0 and committed_sequence),
	tombstoned_at timestamptz,
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now()
);

create index if not exists obs_traces_run_idx on obs_traces(run_id);
create index if not exists obs_traces_chat_idx on obs_traces(chat_id);
create index if not exists obs_traces_notebook_idx on obs_traces(notebook_id);

create table if not exists obs_trace_records (
	trace_id text not null references obs_traces(trace_id) on delete cascade,
	sequence integer not null check (sequence >= 1),
	schema_version integer not null check (schema_version >= 1),
	identity_key text not null check (char_length(identity_key) between 1 and 256),
	kind text not null check (char_length(kind) between 1 and 64),
	span_id text not null check (char_length(span_id) between 1 and 128),
	parent_span_id text not null default '',
	target_trace_id text not null default '',
	target_span_id text not null default '',
	name text not null check (char_length(name) between 1 and 256),
	occurred_at timestamptz not null,
	occurred_at_unix_nano bigint not null,
	payload_version integer not null check (payload_version >= 1),
	canonical_payload jsonb not null check (jsonb_typeof(canonical_payload) = 'object'),
	canonical_sha256 text not null check (canonical_sha256 ~ '^[0-9a-f]{64}$'),
	created_at timestamptz not null default now(),
	primary key (trace_id, sequence),
	unique (trace_id, identity_key)
);

create index if not exists obs_trace_records_span_idx on obs_trace_records(trace_id, span_id, sequence);

create or replace function obs_reject_trace_record_update()
returns trigger
language plpgsql
as $$
begin
	raise exception 'Collector raw Trace records are immutable' using errcode = '55000';
end;
$$;

drop trigger if exists obs_trace_records_immutable_update on obs_trace_records;
create trigger obs_trace_records_immutable_update
	before update on obs_trace_records
	for each row execute function obs_reject_trace_record_update();

create table if not exists obs_projection_queue (
	trace_id text primary key references obs_traces(trace_id) on delete cascade,
	target_sequence integer not null check (target_sequence >= 1),
	available_at timestamptz not null default now(),
	attempt_count integer not null default 0 check (attempt_count >= 0),
	lease_token text,
	lease_expires_at timestamptz,
	last_error_code text,
	updated_at timestamptz not null default now()
);

create index if not exists obs_projection_queue_ready_idx
	on obs_projection_queue(available_at, trace_id)
	where lease_token is null;
`

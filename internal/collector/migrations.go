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
	workload_kind text not null check (workload_kind in ('agent_run', 'source_processing')),
	workload_id text not null check (char_length(workload_id) between 1 and 160),
	run_id text not null default '' check (char_length(run_id) between 0 and 128),
	chat_id text not null default '' check (char_length(chat_id) between 0 and 128),
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

alter table obs_traces add column if not exists workload_kind text;
alter table obs_traces add column if not exists workload_id text;
update obs_traces set workload_kind='agent_run' where workload_kind is null;
update obs_traces set workload_id=run_id where workload_id is null;
alter table obs_traces alter column workload_kind set not null;
alter table obs_traces alter column workload_id set not null;
alter table obs_traces alter column run_id set default '';
alter table obs_traces alter column chat_id set default '';
alter table obs_traces drop constraint if exists obs_traces_run_id_check;
alter table obs_traces drop constraint if exists obs_traces_chat_id_check;
alter table obs_traces drop constraint if exists obs_traces_workload_kind_check;
alter table obs_traces drop constraint if exists obs_traces_workload_id_check;
alter table obs_traces add constraint obs_traces_run_id_check check (char_length(run_id) between 0 and 128);
alter table obs_traces add constraint obs_traces_chat_id_check check (char_length(chat_id) between 0 and 128);
alter table obs_traces add constraint obs_traces_workload_kind_check check (workload_kind in ('agent_run', 'source_processing'));
alter table obs_traces add constraint obs_traces_workload_id_check check (char_length(workload_id) between 1 and 160);

create index if not exists obs_traces_run_idx on obs_traces(run_id);
create index if not exists obs_traces_chat_idx on obs_traces(chat_id);
create index if not exists obs_traces_notebook_idx on obs_traces(notebook_id);
create index if not exists obs_traces_workload_idx on obs_traces(workload_kind, workload_id);

create table if not exists obs_trace_tombstones (
	trace_id text primary key check (char_length(trace_id) between 1 and 128),
	run_id text not null check (char_length(run_id) between 1 and 128),
	tombstoned_at timestamptz not null default now()
);

create table if not exists obs_purge_commands (
	command_id text primary key check (char_length(command_id) between 1 and 160),
	trace_id text not null references obs_trace_tombstones(trace_id) on delete restrict,
	run_id text not null check (char_length(run_id) between 1 and 128),
	command_version integer not null check (command_version = 1),
	producer_id text not null check (char_length(producer_id) between 1 and 160),
	requested_at timestamptz not null,
	requested_at_unix_nano bigint not null,
	created_at timestamptz not null default now()
);

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

create table if not exists obs_payload_refs (
	attachment_id uuid primary key,
	trace_id text not null references obs_traces(trace_id) on delete cascade,
	record_sequence integer not null,
	class text not null check (class in ('model_request', 'model_decision', 'action_input', 'action_result')),
	schema_version integer not null check (schema_version = 1),
	plaintext_sha256 text not null check (plaintext_sha256 ~ '^[0-9a-f]{64}$'),
	object_key text not null unique check (char_length(object_key) between 1 and 512),
	ciphertext_bytes integer not null check (ciphertext_bytes between 1 and 2097152),
	ciphertext_sha256 text not null check (ciphertext_sha256 ~ '^[0-9a-f]{64}$'),
	compression text not null check (compression = 'gzip'),
	encryption text not null check (encryption = 'aes-256-gcm'),
	key_id text not null check (char_length(key_id) between 1 and 160),
	wrapped_key bytea not null check (octet_length(wrapped_key) between 1 and 1024),
	nonce bytea not null check (octet_length(nonce) between 1 and 64),
	state text not null default 'available' check (state in ('available', 'expired', 'purged')),
	expires_at timestamptz not null,
	expires_at_unix_nano bigint not null,
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now(),
	unique (trace_id, record_sequence, class),
	foreign key (trace_id, record_sequence)
		references obs_trace_records(trace_id, sequence) on delete restrict
);

create index if not exists obs_payload_refs_expiry_idx
	on obs_payload_refs(expires_at, attachment_id)
	where state = 'available';

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

create table if not exists obs_trace_summaries (
	trace_id text primary key references obs_traces(trace_id) on delete cascade,
	workload_kind text not null check (workload_kind in ('agent_run', 'source_processing')),
	workload_id text not null check (char_length(workload_id) between 1 and 160),
	run_id text not null default '',
	chat_id text not null default '',
	notebook_id text not null,
	root_span_id text not null,
	agent_name text not null,
	started_at_unix_nano bigint not null,
	last_observed_unix_nano bigint not null,
	ended_at_unix_nano bigint,
	duration_nanoseconds bigint check (duration_nanoseconds is null or duration_nanoseconds >= 0),
	status text not null default '',
	active boolean not null,
	models text[] not null default '{}',
	input_tokens bigint,
	output_tokens bigint,
	total_tokens bigint,
	cost_known boolean not null default false,
	cost_amount double precision,
	cost_currency text not null default '',
	cost_source text not null default '',
	attempt_count integer not null default 0 check (attempt_count >= 0),
	projected_sequence integer not null check (projected_sequence >= 1),
	updated_at timestamptz not null default now()
);

alter table obs_trace_summaries add column if not exists workload_kind text;
alter table obs_trace_summaries add column if not exists workload_id text;
update obs_trace_summaries s set workload_kind=t.workload_kind, workload_id=t.workload_id
	from obs_traces t where s.trace_id=t.trace_id and (s.workload_kind is null or s.workload_id is null);
alter table obs_trace_summaries alter column workload_kind set not null;
alter table obs_trace_summaries alter column workload_id set not null;
alter table obs_trace_summaries alter column run_id set default '';
alter table obs_trace_summaries alter column chat_id set default '';
alter table obs_trace_summaries drop constraint if exists obs_trace_summaries_workload_kind_check;
alter table obs_trace_summaries drop constraint if exists obs_trace_summaries_workload_id_check;
alter table obs_trace_summaries add constraint obs_trace_summaries_workload_kind_check check (workload_kind in ('agent_run', 'source_processing'));
alter table obs_trace_summaries add constraint obs_trace_summaries_workload_id_check check (char_length(workload_id) between 1 and 160);

create index if not exists obs_trace_summaries_cursor_idx
	on obs_trace_summaries(started_at_unix_nano desc, trace_id desc);
create index if not exists obs_trace_summaries_run_idx on obs_trace_summaries(run_id);
create index if not exists obs_trace_summaries_chat_idx on obs_trace_summaries(chat_id);
create index if not exists obs_trace_summaries_notebook_idx on obs_trace_summaries(notebook_id);
create index if not exists obs_trace_summaries_agent_idx on obs_trace_summaries(agent_name);
create index if not exists obs_trace_summaries_workload_idx on obs_trace_summaries(workload_kind, workload_id);
create index if not exists obs_trace_summaries_status_idx on obs_trace_summaries(status, active);
create index if not exists obs_trace_summaries_models_idx on obs_trace_summaries using gin(models);

create table if not exists obs_spans (
	trace_id text not null references obs_trace_summaries(trace_id) on delete cascade,
	span_id text not null,
	parent_span_id text not null default '',
	name text not null,
	start_sequence integer not null,
	end_sequence integer,
	started_at_unix_nano bigint not null,
	ended_at_unix_nano bigint,
	duration_nanoseconds bigint check (duration_nanoseconds is null or duration_nanoseconds >= 0),
	status text not null default '',
	start_attributes jsonb not null,
	end_attributes jsonb not null,
	replay_references jsonb not null,
	model_analysis jsonb,
	primary key (trace_id, span_id)
);

create index if not exists obs_spans_parent_idx on obs_spans(trace_id, parent_span_id, start_sequence);

create table if not exists obs_events (
	trace_id text not null references obs_trace_summaries(trace_id) on delete cascade,
	sequence integer not null,
	span_id text not null,
	name text not null,
	occurred_at_unix_nano bigint not null,
	attributes jsonb not null,
	primary key (trace_id, sequence)
);

create index if not exists obs_events_span_idx on obs_events(trace_id, span_id, sequence);

create table if not exists obs_links (
	trace_id text not null references obs_trace_summaries(trace_id) on delete cascade,
	sequence integer not null,
	span_id text not null,
	name text not null,
	target_trace_id text not null,
	target_span_id text not null,
	occurred_at_unix_nano bigint not null,
	attributes jsonb not null,
	primary key (trace_id, sequence)
);

create index if not exists obs_links_span_idx on obs_links(trace_id, span_id, sequence);
create index if not exists obs_links_target_idx on obs_links(target_trace_id, target_span_id);

create table if not exists obs_purge_queue (
	trace_id text primary key references obs_trace_tombstones(trace_id) on delete cascade,
	stage text not null default 'pending' check (stage in ('pending', 'objects_removed', 'content_removed')),
	available_at timestamptz not null default now(),
	attempt_count integer not null default 0 check (attempt_count >= 0),
	lease_token text,
	lease_expires_at timestamptz,
	last_error_code text,
	updated_at timestamptz not null default now()
);

create index if not exists obs_purge_queue_ready_idx
	on obs_purge_queue(available_at, trace_id)
	where stage != 'content_removed' and lease_token is null;
`

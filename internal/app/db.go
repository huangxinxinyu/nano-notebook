package app

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	pool *pgxpool.Pool
}

func OpenDB(ctx context.Context, dsn string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 8
	cfg.MinConns = 1
	cfg.MaxConnLifetime = time.Hour
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &DB{pool: pool}, nil
}

func (db *DB) Close() {
	if db != nil && db.pool != nil {
		db.pool.Close()
	}
}

func (db *DB) Pool() *pgxpool.Pool {
	return db.pool
}

func (db *DB) WithRequestPrincipal(ctx context.Context, principalID string, fn func(pgx.Tx) error) error {
	if db == nil || db.pool == nil {
		return errors.New("nil database")
	}
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `set local role nano_app`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `select set_config('app.principal_id', $1, true)`, principalID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func ResetForTests(ctx context.Context, db *DB) error {
	if db == nil || db.pool == nil {
		return errors.New("nil database")
	}
	_, err := db.pool.Exec(ctx, `drop schema if exists public cascade; create schema public;`)
	return err
}

func RunMigrations(ctx context.Context, db *DB) error {
	if db == nil || db.pool == nil {
		return errors.New("nil database")
	}
	if _, err := db.pool.Exec(ctx, migrationsSQL); err != nil {
		return err
	}
	return backfillLegacyAgentTraces(ctx, db.pool)
}

const migrationsSQL = `
do $$
begin
	if not exists (select 1 from pg_roles where rolname = 'nano_app') then
		create role nano_app;
	end if;
	if not exists (select 1 from pg_roles where rolname = 'nano_worker') then
		create role nano_worker;
	end if;
	execute format('grant nano_app to %I', current_user);
	execute format('grant nano_worker to %I', current_user);
end $$;

create table if not exists identity_users (
	id text primary key,
	canonical_email text not null unique,
	display_email text not null,
	created_at timestamptz not null default now()
);

create table if not exists identity_local_credentials (
	user_id text primary key references identity_users(id) on delete cascade,
	password_hash text not null,
	created_at timestamptz not null default now()
);

create table if not exists identity_sessions (
	id text primary key,
	user_id text not null references identity_users(id) on delete cascade,
	token_hash text not null unique,
	expires_at timestamptz not null,
	revoked_at timestamptz,
	created_at timestamptz not null default now()
);

create index if not exists identity_sessions_active_idx
	on identity_sessions(token_hash, expires_at)
	where revoked_at is null;

create table if not exists identity_auth_attempts (
	canonical_email text not null,
	attempted_at timestamptz not null default now(),
	succeeded boolean not null
);

create index if not exists identity_auth_attempts_recent_idx
	on identity_auth_attempts(canonical_email, attempted_at desc);

create table if not exists platform_capability_grants (
	user_id text not null references identity_users(id) on delete cascade,
	capability text not null check (capability in ('platform.trace.read', 'platform.trace.replay')),
	granted_by text,
	granted_at timestamptz not null default now(),
	primary key (user_id, capability)
);

create table if not exists platform_replay_access_audit (
	id text primary key,
	operator_user_id text not null references identity_users(id) on delete restrict,
	trace_id text not null check (char_length(trace_id) between 1 and 128),
	span_id text not null check (char_length(span_id) between 1 and 128),
	replay_id text not null check (char_length(replay_id) between 1 and 128),
	replay_class text not null default '' check (replay_class in ('', 'model_request', 'model_decision', 'action_input', 'action_result')),
	outcome text not null check (outcome in ('allowed', 'denied', 'failed')),
	failure_code text not null default '' check (char_length(failure_code) <= 64),
	requested_at timestamptz not null default now()
);

create index if not exists platform_replay_access_audit_operator_idx
	on platform_replay_access_audit(operator_user_id, requested_at desc);

create table if not exists notebook_notebooks (
	id text primary key,
	title text not null check (char_length(title) between 1 and 160),
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now(),
	recent_at timestamptz not null default now()
);

create table if not exists notebook_memberships (
	notebook_id text not null references notebook_notebooks(id) on delete cascade,
	user_id text not null references identity_users(id) on delete cascade,
	role text not null check (role = 'owner'),
	created_at timestamptz not null default now(),
	primary key (notebook_id, user_id)
);

create unique index if not exists notebook_single_owner_idx
	on notebook_memberships(notebook_id)
	where role = 'owner';

create index if not exists notebook_owned_recent_idx
	on notebook_memberships(user_id, role, notebook_id);

create table if not exists platform_idempotency_keys (
	principal_id text not null,
	action text not null,
	key text not null,
	request_hash text not null,
	status_code integer not null,
	response_json jsonb not null,
	created_at timestamptz not null default now(),
	primary key (principal_id, action, key)
);

create table if not exists chat_chats (
	id text primary key,
	notebook_id text not null references notebook_notebooks(id) on delete cascade,
	creator_user_id text not null references identity_users(id) on delete cascade,
	title text not null check (char_length(title) between 1 and 160),
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now()
);

create index if not exists chat_chats_private_recent_idx
	on chat_chats(creator_user_id, notebook_id, updated_at desc, id desc);

create table if not exists chat_messages (
	id text primary key,
	chat_id text not null references chat_chats(id) on delete cascade,
	role text not null check (role in ('user', 'assistant')),
	content text not null check (char_length(content) between 1 and 65536),
	created_at timestamptz not null default now()
);

create index if not exists chat_messages_order_idx
	on chat_messages(chat_id, created_at, id);

create table if not exists agent_runs (
	id text primary key,
	user_id text not null references identity_users(id) on delete cascade,
	chat_id text not null references chat_chats(id) on delete cascade,
	input_message_id text not null references chat_messages(id) on delete restrict,
	output_message_id text unique references chat_messages(id) on delete restrict,
	status text not null check (status in ('queued', 'running', 'completed', 'failed', 'cancelled')),
	model text not null,
	prompt_version text not null,
	time_zone text not null default 'UTC',
	deadline_at timestamptz not null default (now() + interval '10 minutes'),
	action_decision_limit integer not null default 4,
	final_decision_limit integer not null default 1,
	action_limit integer not null default 8,
	action_batch_limit integer not null default 4,
	action_result_byte_limit integer not null default 16384,
	action_results_byte_limit integer not null default 65536,
	error_code text,
	created_at timestamptz not null default now(),
	started_at timestamptz,
	finished_at timestamptz,
	updated_at timestamptz not null default now()
);

create unique index if not exists agent_runs_one_active_per_user_idx
	on agent_runs(user_id)
	where status in ('queued', 'running');

create unique index if not exists agent_runs_one_active_per_input_idx
	on agent_runs(input_message_id)
	where status in ('queued', 'running');

create unique index if not exists agent_runs_one_completed_per_input_idx
	on agent_runs(input_message_id)
	where status = 'completed';

create index if not exists agent_runs_chat_recent_idx
	on agent_runs(chat_id, created_at desc, id desc);

create table if not exists agent_run_checkpoints (
	run_id text not null references agent_runs(id) on delete cascade,
	sequence_no integer not null check (sequence_no >= 1),
	identity_key text not null check (char_length(identity_key) between 1 and 160),
	kind text not null check (kind in ('action_proposal', 'action_result', 'final_draft')),
	decision_no integer not null check (decision_no >= 1),
	action_index integer,
	action_id text,
	payload_version integer not null default 1 check (payload_version >= 1),
	payload jsonb not null check (jsonb_typeof(payload) = 'object'),
	payload_sha256 text not null check (payload_sha256 ~ '^[0-9a-f]{64}$'),
	created_at timestamptz not null default now(),
	primary key (run_id, sequence_no),
	unique (run_id, identity_key),
	constraint agent_run_checkpoints_kind_shape_check check (
		(kind = 'action_proposal' and action_index is null and action_id is null)
		or (kind = 'action_result' and action_index >= 0 and char_length(action_id) >= 1)
		or (kind = 'final_draft' and action_index is null and action_id is null)
	)
);

create table if not exists agent_traces (
	trace_id text primary key check (char_length(trace_id) between 1 and 160),
	run_id text not null unique references agent_runs(id) on delete cascade,
	root_span_id text not null unique check (char_length(root_span_id) between 1 and 160),
	schema_version integer not null check (schema_version >= 1),
	created_at timestamptz not null default now()
);

create table if not exists agent_trace_records (
	trace_id text not null references agent_traces(trace_id) on delete cascade,
	sequence_no integer not null check (sequence_no >= 1),
	identity_key text not null check (char_length(identity_key) between 1 and 200),
	record_kind text not null check (record_kind in ('span_started', 'span_ended', 'event', 'link')),
	span_id text not null check (char_length(span_id) between 1 and 160),
	parent_span_id text check (parent_span_id is null or char_length(parent_span_id) between 1 and 160),
	name text not null check (char_length(name) between 1 and 160),
	target_trace_id text check (target_trace_id is null or char_length(target_trace_id) between 1 and 160),
	target_span_id text check (target_span_id is null or char_length(target_span_id) between 1 and 160),
	occurred_at timestamptz not null,
	payload_version integer not null check (payload_version >= 1),
	payload jsonb not null check (jsonb_typeof(payload) = 'object' and octet_length(payload::text) <= 16384),
	payload_sha256 text not null check (payload_sha256 ~ '^[0-9a-f]{64}$'),
	created_at timestamptz not null default now(),
	primary key (trace_id, sequence_no),
	unique (trace_id, identity_key),
	constraint agent_trace_records_kind_shape_check check (
		(record_kind = 'span_started' and target_trace_id is null and target_span_id is null)
		or (record_kind in ('span_ended', 'event') and parent_span_id is null and target_trace_id is null and target_span_id is null)
		or (record_kind = 'link' and parent_span_id is null and target_trace_id is not null and target_span_id is not null)
	)
);

create unique index if not exists agent_trace_records_one_start_idx
	on agent_trace_records(trace_id, span_id)
	where record_kind = 'span_started';

create unique index if not exists agent_trace_records_one_terminal_idx
	on agent_trace_records(trace_id, span_id)
	where record_kind = 'span_ended';

create or replace function validate_agent_trace_record()
returns trigger
language plpgsql
security definer
set search_path = pg_catalog, public
as $$
declare
	envelope_root_span_id text;
	next_sequence integer;
	record_count integer;
	total_payload_bytes bigint;
	started_name text;
	link_count integer;
begin
	select root_span_id
	into envelope_root_span_id
	from agent_traces
	where trace_id = new.trace_id
	for update;
	if not found then
		raise exception using errcode = '23514', message = 'Trace envelope does not exist';
	end if;

	select coalesce(max(sequence_no), 0) + 1, count(*), coalesce(sum(octet_length(payload::text)), 0)
	into next_sequence, record_count, total_payload_bytes
	from agent_trace_records
	where trace_id = new.trace_id;
	if new.sequence_no <> next_sequence then
		raise exception using errcode = '23514', message = 'Trace sequence is not contiguous';
	end if;
	if record_count >= 256 then
		raise exception using errcode = '23514', message = 'Trace record limit exceeded';
	end if;
	if total_payload_bytes + octet_length(new.payload::text) > 1048576 then
		raise exception using errcode = '23514', message = 'Trace payload limit exceeded';
	end if;

	if new.sequence_no = 1 then
		if new.record_kind <> 'span_started' or new.span_id <> envelope_root_span_id or new.parent_span_id is not null then
			raise exception using errcode = '23514', message = 'First Trace record must start the envelope root Span';
		end if;
	elsif new.record_kind = 'span_started' then
		if new.parent_span_id is null then
			raise exception using errcode = '23514', message = 'Trace cannot contain a second root Span';
		end if;
		perform 1
		from agent_trace_records
		where trace_id = new.trace_id and span_id = new.parent_span_id and record_kind = 'span_started';
		if not found then
			raise exception using errcode = '23514', message = 'Span parent does not resolve';
		end if;
	else
		select name
		into started_name
		from agent_trace_records
		where trace_id = new.trace_id and span_id = new.span_id and record_kind = 'span_started';
		if not found then
			raise exception using errcode = '23514', message = 'Record source Span does not resolve';
		end if;
		if new.record_kind = 'span_ended' and new.name <> started_name then
			raise exception using errcode = '23514', message = 'Terminal Span name does not match its start';
		end if;
	end if;

	if new.record_kind = 'link' then
		perform 1
		from agent_trace_records
		where trace_id = new.target_trace_id and span_id = new.target_span_id and record_kind = 'span_started';
		if not found then
			raise exception using errcode = '23514', message = 'Link target does not resolve';
		end if;
		select count(*)
		into link_count
		from agent_trace_records
		where trace_id = new.trace_id and span_id = new.span_id and record_kind = 'link';
		if link_count >= 8 then
			raise exception using errcode = '23514', message = 'Span Link limit exceeded';
		end if;
	end if;
	return new;
end
$$;
revoke all on function validate_agent_trace_record() from public;

drop trigger if exists agent_trace_records_validate_before_insert on agent_trace_records;
create trigger agent_trace_records_validate_before_insert
	before insert on agent_trace_records
	for each row execute function validate_agent_trace_record();

create table if not exists agent_trace_refs (
	trace_id text primary key check (char_length(trace_id) between 1 and 160),
	run_id text not null unique references agent_runs(id) on delete cascade,
	chat_id text not null references chat_chats(id) on delete cascade,
	notebook_id text not null references notebook_notebooks(id) on delete cascade,
	root_span_id text not null unique check (char_length(root_span_id) between 1 and 160),
	agent_name text not null check (char_length(agent_name) between 1 and 160),
	schema_version integer not null check (schema_version >= 1),
	semantic_convention_version integer not null check (semantic_convention_version >= 1),
	next_sequence integer not null default 1 check (next_sequence >= 1),
	collector_cursor integer not null default 0 check (collector_cursor >= 0 and collector_cursor < next_sequence),
	terminal_sequence integer check (terminal_sequence is null or terminal_sequence >= 1),
	delivery_state text not null default 'ready' check (delivery_state in ('ready', 'leased', 'acknowledged', 'quarantined', 'purging')),
	lease_token uuid,
	lease_expires_at timestamptz,
	next_attempt_at timestamptz not null default now(),
	attempt_count integer not null default 0 check (attempt_count >= 0),
	last_error_code text,
	quarantined_at timestamptz,
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now(),
	constraint agent_trace_refs_delivery_shape_check check (
		(delivery_state = 'leased' and lease_token is not null and lease_expires_at is not null)
		or (delivery_state != 'leased' and lease_token is null and lease_expires_at is null)
	)
);

create index if not exists agent_trace_refs_sender_ready_idx
	on agent_trace_refs(next_attempt_at, created_at, trace_id)
	where delivery_state = 'ready';

create table if not exists agentobs_outbox_records (
	trace_id text not null references agent_trace_refs(trace_id) on delete cascade,
	sequence_no integer not null check (sequence_no >= 1),
	identity_key text not null check (char_length(identity_key) between 1 and 200),
	record_kind text not null check (record_kind in ('span_started', 'span_ended', 'event', 'link')),
	span_id text not null check (char_length(span_id) between 1 and 160),
	parent_span_id text,
	name text not null check (char_length(name) between 1 and 160),
	target_trace_id text,
	target_span_id text,
	occurred_at timestamptz not null,
	occurred_at_unix_nano bigint not null,
	payload_version integer not null check (payload_version >= 1),
	payload jsonb not null check (jsonb_typeof(payload) = 'object' and octet_length(payload::text) <= 16384),
	payload_sha256 text not null check (payload_sha256 ~ '^[0-9a-f]{64}$'),
	canonical_sha256 text not null check (canonical_sha256 ~ '^[0-9a-f]{64}$'),
	encoded_bytes integer not null check (encoded_bytes >= 1),
	created_at timestamptz not null default now(),
	primary key (trace_id, sequence_no),
	unique (trace_id, identity_key),
	constraint agentobs_outbox_records_kind_shape_check check (
		(record_kind = 'span_started' and target_trace_id is null and target_span_id is null)
		or (record_kind = 'span_ended' and parent_span_id is null and target_trace_id is null and target_span_id is null)
		or (record_kind = 'event' and parent_span_id is null and target_trace_id is null and target_span_id is null)
		or (record_kind = 'link' and parent_span_id is null and target_trace_id is not null and target_span_id is not null)
	)
);

create index if not exists agentobs_outbox_records_trace_ready_idx
	on agentobs_outbox_records(trace_id, sequence_no);

create unique index if not exists agentobs_outbox_records_one_start_idx
	on agentobs_outbox_records(trace_id, span_id)
	where record_kind = 'span_started';

create unique index if not exists agentobs_outbox_records_one_terminal_idx
	on agentobs_outbox_records(trace_id, span_id)
	where record_kind = 'span_ended';

create or replace function validate_agentobs_outbox_record()
returns trigger
language plpgsql
security definer
set search_path = pg_catalog, public
as $$
declare
	envelope_root_span_id text;
	expected_sequence integer;
	record_count integer;
	total_payload_bytes bigint;
	started_name text;
	link_count integer;
begin
	select root_span_id, next_sequence
	into envelope_root_span_id, expected_sequence
	from agent_trace_refs
	where trace_id = new.trace_id
	for update;
	if not found then
		raise exception using errcode = '23514', message = 'Trace reference does not exist';
	end if;
	if new.sequence_no <> expected_sequence then
		raise exception using errcode = '23514', message = 'Trace sequence is not contiguous';
	end if;

	select count(*), coalesce(sum(octet_length(payload::text)), 0)
	into record_count, total_payload_bytes
	from agentobs_outbox_records
	where trace_id = new.trace_id;
	if record_count >= 256 then
		raise exception using errcode = '23514', message = 'Trace record limit exceeded';
	end if;
	if total_payload_bytes + octet_length(new.payload::text) > 1048576 then
		raise exception using errcode = '23514', message = 'Trace payload limit exceeded';
	end if;

	if new.sequence_no = 1 then
		if new.record_kind <> 'span_started' or new.span_id <> envelope_root_span_id or new.parent_span_id is not null then
			raise exception using errcode = '23514', message = 'First Trace record must start the reference root Span';
		end if;
	elsif new.record_kind = 'span_started' then
		if new.parent_span_id is null then
			raise exception using errcode = '23514', message = 'Trace cannot contain a second root Span';
		end if;
		perform 1 from agentobs_outbox_records
		where trace_id = new.trace_id and span_id = new.parent_span_id and record_kind = 'span_started';
		if not found then
			raise exception using errcode = '23514', message = 'Span parent does not resolve';
		end if;
	else
		select name into started_name
		from agentobs_outbox_records
		where trace_id = new.trace_id and span_id = new.span_id and record_kind = 'span_started';
		if not found then
			raise exception using errcode = '23514', message = 'Record source Span does not resolve';
		end if;
		if new.record_kind = 'span_ended' and new.name <> started_name then
			raise exception using errcode = '23514', message = 'Terminal Span name does not match its start';
		end if;
	end if;

	if new.record_kind = 'link' then
		perform 1
		from agentobs_outbox_records
		where trace_id = new.target_trace_id and span_id = new.target_span_id and record_kind = 'span_started';
		if not found then
			perform 1 from agent_trace_refs
			where trace_id = new.target_trace_id and root_span_id = new.target_span_id;
		end if;
		if not found then
			raise exception using errcode = '23514', message = 'Link target does not resolve';
		end if;
		select count(*) into link_count
		from agentobs_outbox_records
		where trace_id = new.trace_id and span_id = new.span_id and record_kind = 'link';
		if link_count >= 8 then
			raise exception using errcode = '23514', message = 'Span Link limit exceeded';
		end if;
	end if;
	return new;
end
$$;
revoke all on function validate_agentobs_outbox_record() from public;

drop trigger if exists agentobs_outbox_records_validate_before_insert on agentobs_outbox_records;
create trigger agentobs_outbox_records_validate_before_insert
	before insert on agentobs_outbox_records
	for each row execute function validate_agentobs_outbox_record();

create table if not exists agentobs_outbox_capacity (
	singleton boolean primary key default true check (singleton),
	max_records integer not null default 100000 check (max_records >= 1),
	current_records integer not null default 0 check (current_records >= 0),
	max_staged_ciphertext_bytes bigint not null default 1073741824 check (max_staged_ciphertext_bytes >= 1),
	current_staged_ciphertext_bytes bigint not null default 0 check (current_staged_ciphertext_bytes >= 0),
	updated_at timestamptz not null default now()
);

insert into agentobs_outbox_capacity(singleton, current_records)
	select true, count(*)::integer from agentobs_outbox_records
	on conflict (singleton) do nothing;

create or replace function reserve_agentobs_outbox_record_capacity()
returns trigger
language plpgsql
security definer
set search_path = pg_catalog, public
as $$
begin
	update agentobs_outbox_capacity
	set current_records = current_records + 1, updated_at = now()
	where singleton and current_records < max_records;
	if not found then
		raise exception using errcode = '54000', message = 'Agent Trace Outbox record limit exceeded';
	end if;
	return new;
end
$$;
revoke all on function reserve_agentobs_outbox_record_capacity() from public;

create or replace function release_agentobs_outbox_record_capacity()
returns trigger
language plpgsql
security definer
set search_path = pg_catalog, public
as $$
begin
	update agentobs_outbox_capacity
	set current_records = greatest(current_records - 1, 0), updated_at = now()
	where singleton;
	return old;
end
$$;
revoke all on function release_agentobs_outbox_record_capacity() from public;

drop trigger if exists agentobs_outbox_records_reserve_capacity on agentobs_outbox_records;
create trigger agentobs_outbox_records_reserve_capacity
	before insert on agentobs_outbox_records
	for each row execute function reserve_agentobs_outbox_record_capacity();

drop trigger if exists agentobs_outbox_records_release_capacity on agentobs_outbox_records;
create trigger agentobs_outbox_records_release_capacity
	after delete on agentobs_outbox_records
	for each row execute function release_agentobs_outbox_record_capacity();

create table if not exists agentobs_replay_staging (
	attachment_id uuid primary key,
	trace_id text not null references agent_trace_refs(trace_id) on delete cascade,
	identity_key text not null check (char_length(identity_key) between 1 and 200),
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
	record_sequence integer,
	state text not null default 'staged' check (state in ('staged', 'attached')),
	expires_at timestamptz not null,
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now(),
	unique (trace_id, identity_key),
	foreign key (trace_id, record_sequence)
		references agentobs_outbox_records(trace_id, sequence_no) on delete restrict,
	constraint agentobs_replay_staging_attachment_shape_check check (
		(state = 'staged' and record_sequence is null)
		or (state = 'attached' and record_sequence is not null and record_sequence >= 1)
	)
);

create index if not exists agentobs_replay_staging_expiry_idx
	on agentobs_replay_staging(expires_at, attachment_id);

create unique index if not exists agentobs_replay_staging_record_class_idx
	on agentobs_replay_staging(trace_id, record_sequence, class)
	where state = 'attached';

create or replace function reserve_agentobs_replay_staging_capacity()
returns trigger
language plpgsql
security definer
set search_path = pg_catalog, public
as $$
declare
	trace_attachment_count integer;
	trace_ciphertext_bytes bigint;
begin
	perform 1 from agent_trace_refs where trace_id = new.trace_id for update;
	if not found then
		raise exception using errcode = '23514', message = 'Replay Trace reference does not exist';
	end if;
	select count(*), coalesce(sum(ciphertext_bytes), 0)
	into trace_attachment_count, trace_ciphertext_bytes
	from agentobs_replay_staging where trace_id = new.trace_id;
	if trace_attachment_count >= 32 then
		raise exception using errcode = '54000', message = 'Replay Attachment count limit exceeded';
	end if;
	if trace_ciphertext_bytes + new.ciphertext_bytes > 16777216 then
		raise exception using errcode = '54000', message = 'Replay Trace ciphertext limit exceeded';
	end if;
	update agentobs_outbox_capacity
	set current_staged_ciphertext_bytes = current_staged_ciphertext_bytes + new.ciphertext_bytes,
		updated_at = now()
	where singleton
	  and current_staged_ciphertext_bytes + new.ciphertext_bytes <= max_staged_ciphertext_bytes;
	if not found then
		raise exception using errcode = '54000', message = 'Replay staged ciphertext limit exceeded';
	end if;
	return new;
end
$$;
revoke all on function reserve_agentobs_replay_staging_capacity() from public;

create or replace function release_agentobs_replay_staging_capacity()
returns trigger
language plpgsql
security definer
set search_path = pg_catalog, public
as $$
begin
	update agentobs_outbox_capacity
	set current_staged_ciphertext_bytes = greatest(current_staged_ciphertext_bytes - old.ciphertext_bytes, 0),
		updated_at = now()
	where singleton;
	return old;
end
$$;
revoke all on function release_agentobs_replay_staging_capacity() from public;

drop trigger if exists agentobs_replay_staging_reserve_capacity on agentobs_replay_staging;
create trigger agentobs_replay_staging_reserve_capacity
	before insert on agentobs_replay_staging
	for each row execute function reserve_agentobs_replay_staging_capacity();

drop trigger if exists agentobs_replay_staging_release_capacity on agentobs_replay_staging;
create trigger agentobs_replay_staging_release_capacity
	after delete on agentobs_replay_staging
	for each row execute function release_agentobs_replay_staging_capacity();

create table if not exists agentobs_outbox_commands (
	command_id text primary key check (char_length(command_id) between 1 and 200),
	command_version integer not null check (command_version = 1),
	command_kind text not null check (command_kind = 'purge_trace'),
	trace_id text not null unique check (char_length(trace_id) between 1 and 160),
	run_id text not null check (char_length(run_id) between 1 and 160),
	requested_at timestamptz not null,
	requested_at_unix_nano bigint not null,
	delivery_state text not null default 'ready' check (delivery_state in ('ready', 'leased', 'acknowledged', 'quarantined')),
	lease_token uuid,
	lease_expires_at timestamptz,
	next_attempt_at timestamptz not null default now(),
	attempt_count integer not null default 0 check (attempt_count >= 0),
	last_error_code text,
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now(),
	constraint agentobs_outbox_commands_delivery_shape_check check (
		(delivery_state = 'leased' and lease_token is not null and lease_expires_at is not null)
		or (delivery_state != 'leased' and lease_token is null and lease_expires_at is null)
	)
);

create index if not exists agentobs_outbox_commands_ready_idx
	on agentobs_outbox_commands(next_attempt_at, created_at, command_id)
	where delivery_state = 'ready';

create table if not exists agentobs_outbox_command_objects (
	command_id text not null references agentobs_outbox_commands(command_id) on delete cascade,
	object_key text not null check (char_length(object_key) between 1 and 512),
	primary key (command_id, object_key)
);

create or replace function enqueue_agentobs_trace_purge(trace_identity text, run_identity text)
returns void
language plpgsql
security definer
set search_path = pg_catalog, public
as $$
declare
	command_identity text;
	requested timestamptz;
begin
	command_identity := 'purge/' || trace_identity;
	requested := clock_timestamp();
	insert into agentobs_outbox_commands(
		command_id, command_version, command_kind, trace_id, run_id,
		requested_at, requested_at_unix_nano
	) values (
		command_identity, 1, 'purge_trace', trace_identity, run_identity,
		requested, (extract(epoch from requested) * 1000000000)::bigint
	) on conflict (trace_id) do nothing;
	insert into agentobs_outbox_command_objects(command_id, object_key)
	select command_identity, object_key
	from agentobs_replay_staging
	where trace_id = trace_identity
	on conflict do nothing;
end
$$;
revoke all on function enqueue_agentobs_trace_purge(text, text) from public;

create or replace function enqueue_agentobs_run_purge()
returns trigger
language plpgsql
security definer
set search_path = pg_catalog, public
as $$
declare
	trace_ref record;
begin
	for trace_ref in select trace_id, run_id from agent_trace_refs where run_id = old.id loop
		perform enqueue_agentobs_trace_purge(trace_ref.trace_id, trace_ref.run_id);
	end loop;
	return old;
end
$$;
revoke all on function enqueue_agentobs_run_purge() from public;

create or replace function enqueue_agentobs_chat_purges()
returns trigger
language plpgsql
security definer
set search_path = pg_catalog, public
as $$
declare
	trace_ref record;
begin
	for trace_ref in select trace_id, run_id from agent_trace_refs where chat_id = old.id loop
		perform enqueue_agentobs_trace_purge(trace_ref.trace_id, trace_ref.run_id);
	end loop;
	return old;
end
$$;
revoke all on function enqueue_agentobs_chat_purges() from public;

create or replace function enqueue_agentobs_notebook_purges()
returns trigger
language plpgsql
security definer
set search_path = pg_catalog, public
as $$
declare
	trace_ref record;
begin
	for trace_ref in select trace_id, run_id from agent_trace_refs where notebook_id = old.id loop
		perform enqueue_agentobs_trace_purge(trace_ref.trace_id, trace_ref.run_id);
	end loop;
	return old;
end
$$;
revoke all on function enqueue_agentobs_notebook_purges() from public;

drop trigger if exists agent_runs_enqueue_observability_purge on agent_runs;
create trigger agent_runs_enqueue_observability_purge
	before delete on agent_runs
	for each row execute function enqueue_agentobs_run_purge();

drop trigger if exists chat_chats_enqueue_observability_purges on chat_chats;
create trigger chat_chats_enqueue_observability_purges
	before delete on chat_chats
	for each row execute function enqueue_agentobs_chat_purges();

drop trigger if exists notebook_notebooks_enqueue_observability_purges on notebook_notebooks;
create trigger notebook_notebooks_enqueue_observability_purges
	before delete on notebook_notebooks
	for each row execute function enqueue_agentobs_notebook_purges();

create table if not exists agent_jobs (
	id text primary key,
	kind text not null check (kind = 'agent_run'),
	run_id text not null unique references agent_runs(id) on delete cascade,
	status text not null check (status in ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
	attempt_no integer not null default 0,
	lease_token uuid,
	lease_expires_at timestamptz,
	created_at timestamptz not null default now(),
	started_at timestamptz,
	finished_at timestamptz,
	updated_at timestamptz not null default now(),
	constraint agent_jobs_execution_state_check check (
		(status = 'queued' and attempt_no = 0 and lease_token is null and lease_expires_at is null)
		or (status = 'running' and attempt_no between 1 and 3 and lease_token is not null and lease_expires_at is not null)
		or (status in ('succeeded', 'failed', 'cancelled') and attempt_no between 0 and 3 and lease_token is null and lease_expires_at is null)
	)
);

create index if not exists agent_jobs_queued_idx
	on agent_jobs(created_at, id)
	where status = 'queued';

-- Upgrade Sprint 2A databases in place. A process restart may leave an old
-- running row without a lease, so make that work claimable by lease-aware
-- workers.
alter table agent_runs drop constraint if exists agent_runs_input_message_id_key;
alter table agent_runs drop constraint if exists agent_runs_status_check;
alter table agent_runs add constraint agent_runs_status_check
	check (status in ('queued', 'running', 'completed', 'failed', 'cancelled'));
alter table agent_runs add column if not exists time_zone text not null default 'UTC';
alter table agent_runs add column if not exists deadline_at timestamptz not null default (now() + interval '10 minutes');
alter table agent_runs add column if not exists action_decision_limit integer not null default 4;
alter table agent_runs add column if not exists final_decision_limit integer not null default 1;
alter table agent_runs add column if not exists action_limit integer not null default 8;
alter table agent_runs add column if not exists action_batch_limit integer not null default 4;
alter table agent_runs add column if not exists action_result_byte_limit integer not null default 16384;
alter table agent_runs add column if not exists action_results_byte_limit integer not null default 65536;
alter table chat_messages drop column if exists answer_mode;
alter table agent_runs drop column if exists iteration_count;
alter table agent_runs drop column if exists finish_reason;
alter table agent_runs drop column if exists prompt_tokens;
alter table agent_runs drop column if exists completion_tokens;
alter table agent_runs drop column if exists total_tokens;

alter table agent_jobs add column if not exists attempt_no integer not null default 0;
alter table agent_jobs add column if not exists lease_token uuid;
alter table agent_jobs add column if not exists lease_expires_at timestamptz;
create index if not exists agent_jobs_expired_lease_idx
	on agent_jobs(lease_expires_at, created_at, id)
	where status = 'running';
alter table agent_jobs drop constraint if exists agent_jobs_status_check;
alter table agent_jobs drop constraint if exists agent_jobs_execution_state_check;
update agent_runs r
	set status = 'queued', started_at = null, updated_at = now()
	from agent_jobs j
	where j.run_id = r.id and j.status = 'running' and j.lease_token is null and r.status = 'running';
update agent_jobs
	set status = 'queued', attempt_no = 0, started_at = null, updated_at = now()
	where status = 'running' and lease_token is null;
alter table agent_jobs add constraint agent_jobs_status_check
	check (status in ('queued', 'running', 'succeeded', 'failed', 'cancelled'));
alter table agent_jobs add constraint agent_jobs_execution_state_check check (
	(status = 'queued' and attempt_no = 0 and lease_token is null and lease_expires_at is null)
	or (status = 'running' and attempt_no between 1 and 3 and lease_token is not null and lease_expires_at is not null)
	or (status in ('succeeded', 'failed', 'cancelled') and attempt_no between 0 and 3 and lease_token is null and lease_expires_at is null)
);

-- Adopt only active pre-Trace Runs. Terminal history remains untouched because
-- reconstructing unobserved execution would fabricate evidence. A running
-- legacy Job is returned to the queue so its first Sprint 4 claim can create a
-- real Attempt Span under the adopted root.
do $$
declare
	candidate record;
	identity_suffix text;
	trace_identity text;
	root_identity text;
	payload_text constant text := '{"semantic_convention_version":1,"attributes":[]}';
	payload_hash text;
	adopted_at timestamptz;
begin
	payload_hash := encode(sha256(convert_to(payload_text, 'UTF8')), 'hex');
	for candidate in
		select r.id, r.status
		from agent_runs r
		left join agent_trace_refs ref on ref.run_id = r.id
		left join agent_traces legacy on legacy.run_id = r.id
		where r.status in ('queued', 'running')
		  and ref.trace_id is null and legacy.trace_id is null
		order by r.created_at, r.id
	loop
		if candidate.status = 'running' then
			update agent_jobs
			set status = 'queued', attempt_no = 0, lease_token = null,
				lease_expires_at = null, started_at = null, finished_at = null,
				updated_at = now()
			where run_id = candidate.id;
			update agent_runs
			set status = 'queued', started_at = null, updated_at = now()
			where id = candidate.id;
		end if;

		identity_suffix := md5(candidate.id);
		trace_identity := 'migration-trace-' || identity_suffix;
		root_identity := 'migration-root-' || identity_suffix;
		adopted_at := clock_timestamp();
		insert into agent_traces(trace_id, run_id, root_span_id, schema_version, created_at)
		values(trace_identity, candidate.id, root_identity, 1, adopted_at);
		insert into agent_trace_records(
			trace_id, sequence_no, identity_key, record_kind, span_id,
			parent_span_id, name, target_trace_id, target_span_id,
			occurred_at, payload_version, payload, payload_sha256
		) values (
			trace_identity, 1, 'migration/' || identity_suffix || '/root/start',
			'span_started', root_identity, null, 'agent.execution', null, null,
			adopted_at, 1, payload_text::jsonb, payload_hash
		);
		insert into agent_trace_records(
			trace_id, sequence_no, identity_key, record_kind, span_id,
			parent_span_id, name, target_trace_id, target_span_id,
			occurred_at, payload_version, payload, payload_sha256
		) values (
			trace_identity, 2, 'migration/' || identity_suffix || '/adopted',
			'event', root_identity, null, 'nano.migration.adopted', null, null,
			adopted_at, 1, payload_text::jsonb, payload_hash
		);
	end loop;
end $$;

alter table identity_users enable row level security;
alter table identity_local_credentials enable row level security;
alter table identity_sessions enable row level security;
alter table identity_auth_attempts enable row level security;
alter table platform_capability_grants enable row level security;
alter table platform_replay_access_audit enable row level security;
alter table notebook_notebooks enable row level security;
alter table notebook_memberships enable row level security;
alter table platform_idempotency_keys enable row level security;
alter table chat_chats enable row level security;
alter table chat_messages enable row level security;
alter table agent_runs enable row level security;
alter table agent_run_checkpoints enable row level security;
alter table agent_traces enable row level security;
alter table agent_trace_records enable row level security;
alter table agent_trace_refs enable row level security;
alter table agentobs_outbox_records enable row level security;
alter table agentobs_replay_staging enable row level security;
alter table agentobs_outbox_commands enable row level security;
alter table agentobs_outbox_command_objects enable row level security;
alter table agent_jobs enable row level security;

grant usage on schema public to nano_app, nano_worker;
grant select, insert, update, delete on
	identity_users,
	identity_local_credentials,
	identity_sessions,
	identity_auth_attempts,
	platform_capability_grants,
	platform_replay_access_audit,
	notebook_notebooks,
	notebook_memberships,
	platform_idempotency_keys,
	chat_chats,
	chat_messages,
	agent_runs,
	agent_jobs
to nano_app;
revoke update, delete on platform_capability_grants, platform_replay_access_audit from nano_app;
revoke insert on platform_capability_grants from nano_app;
revoke select on platform_replay_access_audit from nano_app;
grant select on
	identity_users,
	identity_sessions,
	notebook_notebooks,
	notebook_memberships,
	chat_chats,
	chat_messages,
	agent_runs
to nano_worker;
grant select, insert, update, delete on agent_jobs to nano_worker;
grant insert, update on chat_messages, chat_chats, agent_runs to nano_worker;
revoke all on agent_run_checkpoints from nano_app, nano_worker;
grant select, insert on agent_run_checkpoints to nano_worker;
revoke all on agent_traces, agent_trace_records from nano_app, nano_worker;
grant insert on agent_traces, agent_trace_records to nano_app;
grant select, insert on agent_traces, agent_trace_records to nano_worker;
revoke all on agent_trace_refs, agentobs_outbox_records from nano_app, nano_worker;
grant insert on agent_trace_refs, agentobs_outbox_records to nano_app;
grant select, insert, update, delete on agent_trace_refs, agentobs_outbox_records to nano_worker;
revoke all on agentobs_replay_staging from nano_app, nano_worker;
grant select, insert, update, delete on agentobs_replay_staging to nano_worker;
revoke all on agentobs_outbox_commands, agentobs_outbox_command_objects from nano_app, nano_worker;
grant select, insert, update, delete on agentobs_outbox_commands, agentobs_outbox_command_objects to nano_worker;

drop policy if exists identity_users_owner on identity_users;
create policy identity_users_owner on identity_users
	for all to nano_app
	using (id = nullif(current_setting('app.principal_id', true), ''))
	with check (id = nullif(current_setting('app.principal_id', true), ''));

drop policy if exists identity_sessions_owner on identity_sessions;
create policy identity_sessions_owner on identity_sessions
	for all to nano_app
	using (user_id = nullif(current_setting('app.principal_id', true), ''))
	with check (user_id = nullif(current_setting('app.principal_id', true), ''));

drop policy if exists platform_capability_grants_owner on platform_capability_grants;
create policy platform_capability_grants_owner on platform_capability_grants
	for select to nano_app
	using (user_id = nullif(current_setting('app.principal_id', true), ''));

drop policy if exists platform_replay_access_audit_append on platform_replay_access_audit;
create policy platform_replay_access_audit_append on platform_replay_access_audit
	for insert to nano_app
	with check (operator_user_id = nullif(current_setting('app.principal_id', true), ''));

drop policy if exists notebook_memberships_owner on notebook_memberships;
create policy notebook_memberships_owner on notebook_memberships
	for all to nano_app
	using (user_id = nullif(current_setting('app.principal_id', true), ''))
	with check (user_id = nullif(current_setting('app.principal_id', true), ''));

drop policy if exists notebook_memberships_worker on notebook_memberships;
create policy notebook_memberships_worker on notebook_memberships
	for select to nano_worker
	using (true);

drop policy if exists notebook_notebooks_owner on notebook_notebooks;
create policy notebook_notebooks_owner on notebook_notebooks
	for all to nano_app
	using (
		exists (
			select 1
			from notebook_memberships m
			where m.notebook_id = notebook_notebooks.id
			  and m.user_id = nullif(current_setting('app.principal_id', true), '')
			  and m.role = 'owner'
		)
	)
	with check (true);

drop policy if exists notebook_notebooks_worker on notebook_notebooks;
create policy notebook_notebooks_worker on notebook_notebooks
	for select to nano_worker
	using (true);

drop policy if exists platform_idempotency_owner on platform_idempotency_keys;
create policy platform_idempotency_owner on platform_idempotency_keys
	for all to nano_app
	using (principal_id = nullif(current_setting('app.principal_id', true), ''))
	with check (principal_id = nullif(current_setting('app.principal_id', true), ''));

drop policy if exists chat_chats_private on chat_chats;
create policy chat_chats_private on chat_chats
	for all to nano_app
	using (
		creator_user_id = nullif(current_setting('app.principal_id', true), '')
		and exists (
			select 1 from notebook_memberships m
			where m.notebook_id = chat_chats.notebook_id
			  and m.user_id = nullif(current_setting('app.principal_id', true), '')
		)
	)
	with check (
		creator_user_id = nullif(current_setting('app.principal_id', true), '')
		and exists (
			select 1 from notebook_memberships m
			where m.notebook_id = chat_chats.notebook_id
			  and m.user_id = nullif(current_setting('app.principal_id', true), '')
		)
	);

drop policy if exists chat_chats_worker on chat_chats;
create policy chat_chats_worker on chat_chats
	for select to nano_worker
	using (true);

drop policy if exists chat_messages_private on chat_messages;
create policy chat_messages_private on chat_messages
	for all to nano_app
	using (
		exists (
			select 1 from chat_chats c
			where c.id = chat_messages.chat_id
			  and c.creator_user_id = nullif(current_setting('app.principal_id', true), '')
		)
	)
	with check (
		exists (
			select 1 from chat_chats c
			where c.id = chat_messages.chat_id
			  and c.creator_user_id = nullif(current_setting('app.principal_id', true), '')
		)
	);

drop policy if exists chat_messages_worker on chat_messages;
create policy chat_messages_worker on chat_messages
	for all to nano_worker
	using (true)
	with check (true);

drop policy if exists agent_runs_private on agent_runs;
create policy agent_runs_private on agent_runs
	for all to nano_app
	using (user_id = nullif(current_setting('app.principal_id', true), ''))
	with check (user_id = nullif(current_setting('app.principal_id', true), ''));

drop policy if exists agent_runs_worker on agent_runs;
create policy agent_runs_worker on agent_runs
	for all to nano_worker
	using (true)
	with check (true);

drop policy if exists agent_run_checkpoints_worker_read on agent_run_checkpoints;
create policy agent_run_checkpoints_worker_read on agent_run_checkpoints
	for select to nano_worker
	using (true);

drop policy if exists agent_run_checkpoints_worker_append on agent_run_checkpoints;
create policy agent_run_checkpoints_worker_append on agent_run_checkpoints
	for insert to nano_worker
	with check (true);

drop policy if exists agent_traces_app_insert on agent_traces;
create policy agent_traces_app_insert on agent_traces
	for insert to nano_app
	with check (
		exists (
			select 1 from agent_runs r
			where r.id = agent_traces.run_id
			  and r.user_id = nullif(current_setting('app.principal_id', true), '')
		)
	);

drop policy if exists agent_traces_worker_read on agent_traces;
create policy agent_traces_worker_read on agent_traces
	for select to nano_worker
	using (true);

drop policy if exists agent_traces_worker_insert on agent_traces;
create policy agent_traces_worker_insert on agent_traces
	for insert to nano_worker
	with check (true);

create or replace function nano_trace_owned(candidate_trace_id text)
returns boolean
language sql
stable
security definer
set search_path = pg_catalog, public
as $$
	select exists (
		select 1
		from public.agent_traces t
		join public.agent_runs r on r.id = t.run_id
		where t.trace_id = candidate_trace_id
		  and r.user_id = nullif(current_setting('app.principal_id', true), '')
	)
$$;
revoke all on function nano_trace_owned(text) from public;
grant execute on function nano_trace_owned(text) to nano_app;

create or replace function nano_trace_ref_owned(candidate_trace_id text)
returns boolean
language sql
stable
security definer
set search_path = pg_catalog, public
as $$
	select exists (
		select 1
		from public.agent_trace_refs t
		join public.agent_runs r on r.id = t.run_id
		where t.trace_id = candidate_trace_id
		  and r.user_id = nullif(current_setting('app.principal_id', true), '')
	)
$$;
revoke all on function nano_trace_ref_owned(text) from public;
grant execute on function nano_trace_ref_owned(text) to nano_app;

create or replace function nano_advance_agent_trace_ref(
	candidate_trace_id text,
	candidate_sequence integer,
	candidate_kind text,
	candidate_span_id text
)
returns void
language plpgsql
security definer
set search_path = pg_catalog, public
as $$
declare
	principal text;
	invoker_role text;
begin
	principal := nullif(current_setting('app.principal_id', true), '');
	invoker_role := current_setting('role', true);
	if (invoker_role = 'nano_app' or session_user = 'nano_app')
		and (principal is null or not nano_trace_ref_owned(candidate_trace_id)) then
		raise exception using errcode = '42501', message = 'Trace ref is not owned by request principal';
	end if;
	update agent_trace_refs
	set next_sequence = candidate_sequence + 1,
		terminal_sequence = case
			when candidate_kind = 'span_ended' and root_span_id = candidate_span_id then candidate_sequence
			else terminal_sequence
		end,
		delivery_state = case
			when delivery_state in ('leased', 'quarantined', 'purging') then delivery_state
			else 'ready'
		end,
		updated_at = now()
	where trace_id = candidate_trace_id and next_sequence = candidate_sequence;
	if not found then
		raise exception using errcode = '23514', message = 'Trace ref sequence is not contiguous';
	end if;
end
$$;
revoke all on function nano_advance_agent_trace_ref(text, integer, text, text) from public;
grant execute on function nano_advance_agent_trace_ref(text, integer, text, text) to nano_app, nano_worker;

create or replace function nano_owned_run_trace_state(candidate_run_id text)
returns table(trace_id text, root_span_id text, schema_version integer, sequence_no integer)
language sql
stable
security definer
set search_path = pg_catalog, public
as $$
	select t.trace_id, t.root_span_id, t.schema_version, (t.next_sequence - 1)::integer
	from public.agent_trace_refs t
	join public.agent_runs r on r.id = t.run_id
	where t.run_id = candidate_run_id
	  and r.user_id = nullif(current_setting('app.principal_id', true), '')
$$;
revoke all on function nano_owned_run_trace_state(text) from public;
grant execute on function nano_owned_run_trace_state(text) to nano_app;

create or replace function nano_owned_trace_span(candidate_run_id text, candidate_identity_key text)
returns table(trace_id text, span_id text)
language sql
stable
security definer
set search_path = pg_catalog, public
as $$
	select t.trace_id, rec.span_id
	from public.agent_trace_refs t
	join public.agent_runs r on r.id = t.run_id
	join public.agentobs_outbox_records rec on rec.trace_id = t.trace_id
	where t.run_id = candidate_run_id
	  and r.user_id = nullif(current_setting('app.principal_id', true), '')
	  and rec.identity_key = candidate_identity_key
	  and rec.record_kind = 'span_started'
$$;
revoke all on function nano_owned_trace_span(text, text) from public;
grant execute on function nano_owned_trace_span(text, text) to nano_app;

drop policy if exists agent_trace_records_app_insert on agent_trace_records;
create policy agent_trace_records_app_insert on agent_trace_records
	for insert to nano_app
	with check (nano_trace_owned(trace_id));

drop policy if exists agent_trace_records_worker_read on agent_trace_records;
create policy agent_trace_records_worker_read on agent_trace_records
	for select to nano_worker
	using (true);

drop policy if exists agent_trace_records_worker_insert on agent_trace_records;
create policy agent_trace_records_worker_insert on agent_trace_records
	for insert to nano_worker
	with check (true);

drop policy if exists agent_trace_refs_app_insert on agent_trace_refs;
create policy agent_trace_refs_app_insert on agent_trace_refs
	for insert to nano_app
	with check (
		exists (
			select 1 from agent_runs r
			where r.id = agent_trace_refs.run_id
			  and r.user_id = nullif(current_setting('app.principal_id', true), '')
		)
	);

drop policy if exists agent_trace_refs_worker on agent_trace_refs;
create policy agent_trace_refs_worker on agent_trace_refs
	for all to nano_worker using (true) with check (true);

drop policy if exists agentobs_outbox_records_app_insert on agentobs_outbox_records;
create policy agentobs_outbox_records_app_insert on agentobs_outbox_records
	for insert to nano_app with check (nano_trace_ref_owned(trace_id));

drop policy if exists agentobs_outbox_records_worker on agentobs_outbox_records;
create policy agentobs_outbox_records_worker on agentobs_outbox_records
	for all to nano_worker using (true) with check (true);

drop policy if exists agentobs_replay_staging_worker on agentobs_replay_staging;
create policy agentobs_replay_staging_worker on agentobs_replay_staging
	for all to nano_worker using (true) with check (true);

drop policy if exists agentobs_outbox_commands_worker on agentobs_outbox_commands;
create policy agentobs_outbox_commands_worker on agentobs_outbox_commands
	for all to nano_worker using (true) with check (true);

drop policy if exists agentobs_outbox_command_objects_worker on agentobs_outbox_command_objects;
create policy agentobs_outbox_command_objects_worker on agentobs_outbox_command_objects
	for all to nano_worker using (true) with check (true);

drop policy if exists agent_jobs_private on agent_jobs;
create policy agent_jobs_private on agent_jobs
	for all to nano_app
	using (
		exists (
			select 1 from agent_runs r
			where r.id = agent_jobs.run_id
			  and r.user_id = nullif(current_setting('app.principal_id', true), '')
		)
	)
	with check (
		exists (
			select 1 from agent_runs r
			where r.id = agent_jobs.run_id
			  and r.user_id = nullif(current_setting('app.principal_id', true), '')
		)
	);

drop policy if exists agent_jobs_worker on agent_jobs;
create policy agent_jobs_worker on agent_jobs
	for all to nano_worker
	using (true)
	with check (true);
`

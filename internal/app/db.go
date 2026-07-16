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
	_, err := db.pool.Exec(ctx, migrationsSQL)
	return err
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
	answer_mode text check (
		(role = 'user' and answer_mode is null)
		or (role = 'assistant' and answer_mode = 'model_knowledge')
	),
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
	iteration_count integer not null default 0 check (iteration_count between 0 and 1),
	finish_reason text,
	prompt_tokens integer check (prompt_tokens is null or prompt_tokens >= 0),
	completion_tokens integer check (completion_tokens is null or completion_tokens >= 0),
	total_tokens integer check (total_tokens is null or total_tokens >= 0),
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

alter table identity_users enable row level security;
alter table identity_local_credentials enable row level security;
alter table identity_sessions enable row level security;
alter table identity_auth_attempts enable row level security;
alter table notebook_notebooks enable row level security;
alter table notebook_memberships enable row level security;
alter table platform_idempotency_keys enable row level security;
alter table chat_chats enable row level security;
alter table chat_messages enable row level security;
alter table agent_runs enable row level security;
alter table agent_jobs enable row level security;

grant usage on schema public to nano_app, nano_worker;
grant select, insert, update, delete on
	identity_users,
	identity_local_credentials,
	identity_sessions,
	identity_auth_attempts,
	notebook_notebooks,
	notebook_memberships,
	platform_idempotency_keys,
	chat_chats,
	chat_messages,
	agent_runs,
	agent_jobs
to nano_app;
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

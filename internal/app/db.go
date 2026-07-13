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

alter table identity_users enable row level security;
alter table identity_local_credentials enable row level security;
alter table identity_sessions enable row level security;
alter table identity_auth_attempts enable row level security;
alter table notebook_notebooks enable row level security;
alter table notebook_memberships enable row level security;
alter table platform_idempotency_keys enable row level security;

grant usage on schema public to nano_app, nano_worker;
grant select, insert, update, delete on
	identity_users,
	identity_local_credentials,
	identity_sessions,
	identity_auth_attempts,
	notebook_notebooks,
	notebook_memberships,
	platform_idempotency_keys
to nano_app;
grant select on
	identity_users,
	identity_sessions,
	notebook_notebooks,
	notebook_memberships
to nano_worker;

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

drop policy if exists platform_idempotency_owner on platform_idempotency_keys;
create policy platform_idempotency_owner on platform_idempotency_keys
	for all to nano_app
	using (principal_id = nullif(current_setting('app.principal_id', true), ''))
	with check (principal_id = nullif(current_setting('app.principal_id', true), ''));
`

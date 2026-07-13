package app

import (
	"context"
	"errors"
	"time"

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
`

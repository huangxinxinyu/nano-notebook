package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
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
	traceScope, hasTraceScope := agent.TraceScopeFromContext(ctx)
	if hasTraceScope {
		defer traceScope.Rollback()
	}
	if _, err := tx.Exec(ctx, `set local role nano_app`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `select set_config('app.principal_id', $1, true)`, principalID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if hasTraceScope {
		_ = traceScope.PublishAfterCommit(ctx)
	}
	return nil
}

// ResetForTests drops and recreates the public schema, so it must never run
// against a database a human could be using for local development. It
// refuses to run unless the connected database name ends in "_test" (the
// convention scripts/test-go and every NANO_TEST_DATABASE_URL default in
// this repo already follow) — this is the guard that was missing when a
// hand-typed NANO_TEST_DATABASE_URL pointed at the dev database "nano" and
// wiped it.
func ResetForTests(ctx context.Context, db *DB) error {
	if db == nil || db.pool == nil {
		return errors.New("nil database")
	}
	var databaseName string
	if err := db.pool.QueryRow(ctx, `select current_database()`).Scan(&databaseName); err != nil {
		return err
	}
	if !strings.HasSuffix(databaseName, "_test") {
		return fmt.Errorf("refusing to reset database %q: ResetForTests only runs against databases whose name ends in \"_test\"", databaseName)
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
	if err := registerEmbeddedPromptCatalog(ctx, db); err != nil {
		return err
	}
	return registerEmbeddedAgentCatalog(ctx, db)
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
	capability text not null check (capability in ('platform.trace.read', 'platform.trace.replay', 'platform.trace.analytics')),
	granted_by text,
	granted_at timestamptz not null default now(),
	primary key (user_id, capability)
);

alter table platform_capability_grants drop constraint if exists platform_capability_grants_capability_check;
alter table platform_capability_grants add constraint platform_capability_grants_capability_check
	check (capability in ('platform.trace.read', 'platform.trace.replay', 'platform.trace.analytics'));

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
	role text not null constraint notebook_memberships_role_check check (role in ('viewer', 'editor', 'owner')),
	created_at timestamptz not null default now(),
	primary key (notebook_id, user_id)
);

alter table notebook_memberships drop constraint if exists notebook_memberships_role_check;
alter table notebook_memberships add constraint notebook_memberships_role_check
	check (role in ('viewer', 'editor', 'owner'));

create unique index if not exists notebook_single_owner_idx
	on notebook_memberships(notebook_id)
	where role = 'owner';

create or replace function nano_enforce_notebook_owner()
returns trigger
language plpgsql
security definer
set search_path = pg_catalog, public
as $$
declare
	candidate_notebook_id text;
begin
	if tg_table_name = 'notebook_notebooks' then
		candidate_notebook_id := coalesce(new.id, old.id);
	else
		candidate_notebook_id := coalesce(new.notebook_id, old.notebook_id);
	end if;
	if exists(select 1 from public.notebook_notebooks where id=candidate_notebook_id)
		and (select count(*) from public.notebook_memberships where notebook_id=candidate_notebook_id and role='owner') <> 1 then
		raise exception 'notebook % must have exactly one owner', candidate_notebook_id using errcode='23514';
	end if;
	return null;
end
$$;

drop trigger if exists notebook_owner_membership_guard on notebook_memberships;
create constraint trigger notebook_owner_membership_guard
	after insert or update or delete on notebook_memberships
	deferrable initially deferred for each row execute function nano_enforce_notebook_owner();

drop trigger if exists notebook_owner_notebook_guard on notebook_notebooks;
create constraint trigger notebook_owner_notebook_guard
	after insert on notebook_notebooks
	deferrable initially deferred for each row execute function nano_enforce_notebook_owner();

create index if not exists notebook_owned_recent_idx
	on notebook_memberships(user_id, role, notebook_id);

create table if not exists notebook_invitations (
	id text primary key,
	notebook_id text not null references notebook_notebooks(id) on delete cascade,
	canonical_email text not null check (char_length(canonical_email) between 3 and 320),
	display_email text not null check (char_length(display_email) between 3 and 320),
	role text not null check (role in ('viewer', 'editor')),
	token_hash text not null unique check (token_hash ~ '^[0-9a-f]{64}$'),
	token_generation integer not null default 1 check (token_generation > 0),
	state text not null check (state in ('pending', 'accepted', 'revoked', 'expired')),
	invited_by_user_id text not null references identity_users(id) on delete restrict,
	accepted_by_user_id text references identity_users(id) on delete restrict,
	expires_at timestamptz not null,
	accepted_at timestamptz,
	revoked_at timestamptz,
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now(),
	constraint notebook_invitations_lifecycle_check check (
		(state = 'pending' and accepted_by_user_id is null and accepted_at is null and revoked_at is null)
		or (state = 'accepted' and accepted_by_user_id is not null and accepted_at is not null and revoked_at is null)
		or (state = 'revoked' and accepted_by_user_id is null and accepted_at is null and revoked_at is not null)
		or (state = 'expired' and accepted_by_user_id is null and accepted_at is null and revoked_at is null)
	)
);

create unique index if not exists notebook_invitations_pending_email_idx
	on notebook_invitations(notebook_id, canonical_email)
	where state = 'pending';

create index if not exists notebook_invitations_management_idx
	on notebook_invitations(notebook_id, state, created_at desc, id);

create index if not exists notebook_invitations_expiry_idx
	on notebook_invitations(expires_at, id)
	where state = 'pending';

create table if not exists platform_mail_outbox (
	id text primary key,
	kind text not null check (kind in ('notebook_invitation', 'notebook_deleted')),
	invitation_id text,
	actor_user_id text not null references identity_users(id) on delete restrict,
	recipient_email text not null check (char_length(recipient_email) between 3 and 320),
	locale text not null check (locale in ('en', 'zh-CN')),
	payload jsonb not null check (jsonb_typeof(payload) = 'object'),
	state text not null check (state in ('pending', 'leased', 'sent', 'failed')),
	attempt_no integer not null default 0 check (attempt_no between 0 and 10),
	available_at timestamptz not null,
	lease_token uuid,
	lease_expires_at timestamptz,
	last_error_code text,
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now(),
	sent_at timestamptz,
	constraint platform_mail_outbox_lease_check check (
		(state = 'pending' and lease_token is null and lease_expires_at is null and sent_at is null)
		or (state = 'leased' and lease_token is not null and lease_expires_at is not null and sent_at is null)
		or (state = 'sent' and lease_token is null and lease_expires_at is null and sent_at is not null)
		or (state = 'failed' and lease_token is null and lease_expires_at is null and sent_at is null)
	)
);

create index if not exists platform_mail_outbox_claim_idx
	on platform_mail_outbox(available_at, created_at, id)
	where state = 'pending';

create or replace function nano_notebook_reserved_member_slots(candidate_notebook_id text, observed_at timestamptz)
returns integer
language sql
stable
security definer
set search_path = pg_catalog, public
as $$
	select case when exists (
		select 1 from public.notebook_memberships owner_membership
		where owner_membership.notebook_id = candidate_notebook_id
		  and owner_membership.user_id = nullif(current_setting('app.principal_id', true), '')
		  and owner_membership.role = 'owner'
	) then (
		select
			(select count(*) from public.notebook_memberships m where m.notebook_id=candidate_notebook_id and m.role <> 'owner') +
			(select count(*) from public.notebook_invitations i where i.notebook_id=candidate_notebook_id and i.state='pending' and i.expires_at > observed_at)
	)::integer else -1 end
$$;
revoke all on function nano_notebook_reserved_member_slots(text, timestamptz) from public;
grant execute on function nano_notebook_reserved_member_slots(text, timestamptz) to nano_app;

create or replace function nano_notebook_email_is_member(candidate_notebook_id text, candidate_email text)
returns boolean
language sql
stable
security definer
set search_path = pg_catalog, public
as $$
	select exists (
		select 1
		from public.notebook_memberships owner_membership
		where owner_membership.notebook_id = candidate_notebook_id
		  and owner_membership.user_id = nullif(current_setting('app.principal_id', true), '')
		  and owner_membership.role = 'owner'
	) and exists (
		select 1
		from public.notebook_memberships m
		join public.identity_users u on u.id=m.user_id
		where m.notebook_id=candidate_notebook_id and u.canonical_email=candidate_email
	)
$$;
revoke all on function nano_notebook_email_is_member(text, text) from public;
grant execute on function nano_notebook_email_is_member(text, text) to nano_app;

create or replace function nano_resolve_notebook_invitation(candidate_token_hash text)
returns table(notebook_title text, invited_role text, canonical_email text, expires_at timestamptz)
language sql
stable
security definer
set search_path = pg_catalog, public
as $$
	select n.title,i.role,i.canonical_email,i.expires_at
	from public.notebook_invitations i join public.notebook_notebooks n on n.id=i.notebook_id
	where i.token_hash=candidate_token_hash and i.state='pending' and i.expires_at > now()
$$;
revoke all on function nano_resolve_notebook_invitation(text) from public;
grant execute on function nano_resolve_notebook_invitation(text) to nano_app;

create table if not exists source_sources (
	id text primary key,
	notebook_id text not null references notebook_notebooks(id) on delete cascade,
	input_kind text not null check (input_kind in ('file', 'url')),
	format text not null check (format in ('txt')),
	title text not null check (char_length(title) between 1 and 255),
	media_type text not null check (char_length(media_type) between 1 and 255),
	byte_size bigint not null check (byte_size between 1 and 104857600),
	content_sha256 text not null check (content_sha256 ~ '^[0-9a-f]{64}$'),
	original_object_key text not null check (char_length(original_object_key) between 1 and 1024),
	state text not null check (state in ('uploaded')),
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now()
);

alter table source_sources add column if not exists origin_url text;
alter table source_sources add column if not exists final_url text;
alter table source_sources add column if not exists origin_url_identity text;
alter table source_sources add column if not exists final_url_identity text;
alter table source_sources drop constraint if exists source_sources_input_metadata_check;
alter table source_sources add constraint source_sources_input_metadata_check check (
	(input_kind = 'file' and origin_url is null and final_url is null)
	or (input_kind = 'url' and char_length(origin_url) between 1 and 4096 and char_length(final_url) between 1 and 4096)
);

alter table source_sources drop constraint if exists source_sources_state_check;
alter table source_sources add constraint source_sources_state_check check (
	state in ('uploaded', 'validating', 'normalizing', 'segmenting', 'qualifying', 'indexing', 'verifying', 'ready', 'failed')
);
alter table source_sources drop constraint if exists source_sources_format_check;
alter table source_sources add constraint source_sources_format_check check (
	format in ('txt', 'markdown', 'pdf', 'docx', 'pptx', 'mp3', 'wav', 'm4a', 'png', 'jpeg', 'webp', 'html', 'youtube')
);

create unique index if not exists source_sources_notebook_file_hash_idx
	on source_sources(notebook_id, content_sha256)
	where input_kind = 'file';

create index if not exists source_sources_notebook_created_idx
	on source_sources(notebook_id, created_at, id);

create index if not exists source_sources_notebook_origin_url_identity_idx
	on source_sources(notebook_id, origin_url_identity)
	where input_kind='url' and origin_url_identity is not null;

create unique index if not exists source_sources_notebook_final_url_identity_idx
	on source_sources(notebook_id, final_url_identity)
	where input_kind='url' and final_url_identity is not null;

create unique index if not exists source_sources_notebook_url_pdf_hash_idx
	on source_sources(notebook_id, content_sha256)
	where input_kind='url' and format='pdf'
		and original_object_key like 'sources/notebooks/%/content/%/original.pdf';

create table if not exists source_upload_intents (
	id text primary key,
	source_id text not null unique,
	notebook_id text not null references notebook_notebooks(id) on delete cascade,
	created_by_user_id text not null references identity_users(id) on delete cascade,
	idempotency_key text not null check (char_length(idempotency_key) between 1 and 255),
	request_hash text not null check (request_hash ~ '^[0-9a-f]{64}$'),
	title text not null check (char_length(title) between 1 and 255),
	format text not null check (format in ('txt')),
	media_type text not null check (char_length(media_type) between 1 and 255),
	byte_size bigint not null check (byte_size between 1 and 104857600),
	content_sha256 text not null check (content_sha256 ~ '^[0-9a-f]{64}$'),
	object_key text not null unique check (char_length(object_key) between 1 and 1024),
	state text not null check (state in ('pending', 'finalized', 'expired')),
	expires_at timestamptz not null,
	created_at timestamptz not null default now(),
	finalized_at timestamptz,
	constraint source_upload_intents_expiry_check check (expires_at > created_at),
	constraint source_upload_intents_finalized_check check (
		(state = 'finalized' and finalized_at is not null)
		or (state in ('pending', 'expired') and finalized_at is null)
	),
	unique (created_by_user_id, idempotency_key)
);

alter table source_upload_intents drop constraint if exists source_upload_intents_format_check;
alter table source_upload_intents add constraint source_upload_intents_format_check check (
	format in ('txt', 'markdown', 'pdf', 'docx', 'pptx', 'mp3', 'wav', 'm4a', 'png', 'jpeg', 'webp')
);

create index if not exists source_upload_intents_expiry_idx
	on source_upload_intents(expires_at, id)
	where state = 'pending';

create table if not exists source_url_admissions (
	id text primary key,
	source_id text not null unique,
	notebook_id text not null references notebook_notebooks(id) on delete cascade,
	created_by_user_id text not null references identity_users(id) on delete cascade,
	idempotency_key text not null check (char_length(idempotency_key) between 1 and 255),
	request_hash text not null check (request_hash ~ '^[0-9a-f]{64}$'),
	request_url text not null check (char_length(request_url) between 1 and 4096),
	state text not null check (state in ('pending', 'completed', 'failed')),
	error_code text,
	created_at timestamptz not null default now(),
	completed_at timestamptz,
	constraint source_url_admissions_completion_check check (
		(state = 'pending' and error_code is null and completed_at is null)
		or (state = 'completed' and error_code is null and completed_at is not null)
		or (state = 'failed' and error_code is not null and completed_at is not null)
	),
	unique (created_by_user_id, idempotency_key)
);

alter table source_url_admissions drop constraint if exists source_url_admissions_source_id_key;

create table if not exists source_processing_jobs (
	id text primary key,
	source_id text not null unique references source_sources(id) on delete cascade,
	notebook_id text not null references notebook_notebooks(id) on delete cascade,
	status text not null check (status in ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
	attempt_no integer not null default 0 check (attempt_no between 0 and 3),
	available_at timestamptz not null default now(),
	lease_token uuid,
	lease_expires_at timestamptz,
	last_error_code text,
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now(),
	constraint source_processing_jobs_lease_check check (
		(status = 'queued' and lease_token is null and lease_expires_at is null)
		or (status = 'running' and lease_token is not null and lease_expires_at is not null)
		or (status in ('succeeded', 'failed', 'cancelled') and lease_token is null and lease_expires_at is null)
	)
);

create index if not exists source_processing_jobs_claim_idx
	on source_processing_jobs(available_at, created_at, id)
	where status = 'queued';

create table if not exists source_purge_jobs (
	id text primary key,
	source_id text not null,
	notebook_id text not null,
	created_by_user_id text not null,
	original_object_key text not null check (char_length(original_object_key) between 1 and 1024),
	object_keys jsonb not null default '[]'::jsonb check (jsonb_typeof(object_keys)='array'),
	projection_scopes jsonb not null default '[]'::jsonb check (jsonb_typeof(projection_scopes)='array'),
	state text not null check (state in ('pending', 'running', 'succeeded', 'failed')),
	attempt_no integer not null default 0 check (attempt_no between 0 and 10),
	lease_token uuid,
	lease_expires_at timestamptz,
	last_error_code text,
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now(),
	constraint source_purge_jobs_lease_check check (
		(state = 'pending' and lease_token is null and lease_expires_at is null)
		or (state = 'running' and lease_token is not null and lease_expires_at is not null)
		or (state in ('succeeded', 'failed') and lease_token is null and lease_expires_at is null)
	)
);

alter table source_purge_jobs add column if not exists object_keys jsonb not null default '[]'::jsonb;
alter table source_purge_jobs add column if not exists projection_scopes jsonb not null default '[]'::jsonb;
update source_purge_jobs set object_keys=jsonb_build_array(original_object_key) where object_keys='[]'::jsonb;

alter table source_purge_jobs drop constraint if exists source_purge_jobs_notebook_id_fkey;
alter table source_purge_jobs drop constraint if exists source_purge_jobs_created_by_user_id_fkey;

create index if not exists source_purge_jobs_claim_idx
	on source_purge_jobs(created_at, id)
	where state = 'pending';

create table if not exists source_evidence_revisions (
	id text primary key,
	source_id text not null references source_sources(id) on delete cascade,
	notebook_id text not null references notebook_notebooks(id) on delete cascade,
	revision_no integer not null check (revision_no > 0),
	extraction_config_id text not null check (char_length(extraction_config_id) between 1 and 255),
	artifact_schema_version text not null check (char_length(artifact_schema_version) between 1 and 255),
	artifact_object_key text not null unique check (char_length(artifact_object_key) between 1 and 1024),
	artifact_sha256 text not null check (artifact_sha256 ~ '^[0-9a-f]{64}$'),
	status text not null check (status in ('building', 'active', 'superseded')),
	created_at timestamptz not null default now(),
	activated_at timestamptz,
	constraint source_evidence_revisions_activation_check check (
		(status = 'building' and activated_at is null)
		or (status in ('active', 'superseded') and activated_at is not null)
	),
	unique (source_id, revision_no)
);

create unique index if not exists source_evidence_revisions_one_active_idx
	on source_evidence_revisions(source_id) where status='active';

create table if not exists source_admission_reports (
	id text primary key check (id ~ '^sar_[0-9a-f]{32}$'),
	source_id text not null references source_sources(id) on delete cascade,
	notebook_id text not null references notebook_notebooks(id) on delete cascade,
	revision_id text not null references source_evidence_revisions(id) on delete cascade,
	content_sha256 text not null check (content_sha256 ~ '^[0-9a-f]{64}$'),
	artifact_sha256 text not null check (artifact_sha256 ~ '^[0-9a-f]{64}$'),
	policy_id text not null check (char_length(policy_id) between 1 and 255),
	policy_sha256 text not null check (policy_sha256 ~ '^[0-9a-f]{64}$'),
	runtime_mode text not null check (runtime_mode in ('shadow','enforcement')),
	status text not null check (status in ('passed','review_required','not_applicable')),
	score double precision check (score between 0 and 1),
	signal_coverage double precision not null check (signal_coverage between 0 and 1),
	exact_identity_match boolean not null,
	assessment_json jsonb not null check (jsonb_typeof(assessment_json)='object'),
	created_at timestamptz not null default now(),
	unique (revision_id, policy_sha256),
	constraint source_admission_reports_status_score_check check (
		(status='not_applicable' and score is null)
		or (status in ('passed','review_required') and score is not null)
	)
);

create index if not exists source_admission_reports_source_created_idx
	on source_admission_reports(source_id, created_at desc, id);

create table if not exists source_admission_reviews (
	id text primary key check (id ~ '^sarv_[0-9a-f]{32}$'),
	report_id text not null unique references source_admission_reports(id) on delete cascade,
	source_id text not null references source_sources(id) on delete cascade,
	notebook_id text not null references notebook_notebooks(id) on delete cascade,
	revision_id text not null references source_evidence_revisions(id) on delete cascade,
	reviewer_user_id text not null references identity_users(id) on delete restrict,
	decision text not null check (decision in ('approve','reject')),
	note text not null default '' check (char_length(note) <= 500),
	created_at timestamptz not null default now()
);

create index if not exists source_admission_reviews_source_created_idx
	on source_admission_reviews(source_id, created_at desc, id);

create table if not exists source_evidence_coverage (
	revision_id text primary key references source_evidence_revisions(id) on delete cascade,
	status text not null check (status in ('complete', 'partial')),
	total_runes integer not null check (total_runes > 0)
);

create table if not exists source_evidence_coverage_gaps (
	revision_id text not null references source_evidence_revisions(id) on delete cascade,
	ordinal integer not null check (ordinal >= 0),
	start_rune integer check (start_rune >= 0),
	end_rune integer check (end_rune > start_rune),
	reason text not null check (char_length(reason) between 1 and 255),
	impact text not null check (impact='non_primary'),
	coordinate_json jsonb,
	constraint source_evidence_coverage_gaps_bound_check check (
		(start_rune is not null and end_rune is not null and coordinate_json is null)
		or (start_rune is null and end_rune is null and jsonb_typeof(coordinate_json)='object')
	),
	primary key (revision_id, ordinal)
);

alter table source_evidence_coverage_gaps add column if not exists impact text;
update source_evidence_coverage_gaps set impact='non_primary' where impact is null;
alter table source_evidence_coverage_gaps alter column impact set not null;
alter table source_evidence_coverage_gaps add column if not exists coordinate_json jsonb;
alter table source_evidence_coverage_gaps alter column start_rune drop not null;
alter table source_evidence_coverage_gaps alter column end_rune drop not null;

do $$
begin
	if not exists (
		select 1 from pg_constraint where conname='source_evidence_coverage_gaps_impact_check'
	) then
		alter table source_evidence_coverage_gaps add constraint source_evidence_coverage_gaps_impact_check check (impact='non_primary');
	end if;
	if not exists (
		select 1 from pg_constraint where conname='source_evidence_coverage_gaps_bound_check'
	) then
		alter table source_evidence_coverage_gaps add constraint source_evidence_coverage_gaps_bound_check check (
			(start_rune is not null and end_rune is not null and coordinate_json is null)
			or (start_rune is null and end_rune is null and jsonb_typeof(coordinate_json)='object')
		);
	end if;
end $$;

create table if not exists source_evidence_units (
	id text primary key,
	revision_id text not null references source_evidence_revisions(id) on delete cascade,
	source_id text not null references source_sources(id) on delete cascade,
	notebook_id text not null references notebook_notebooks(id) on delete cascade,
	ordinal integer not null check (ordinal >= 0),
	kind text not null check (kind in ('heading', 'paragraph', 'list', 'code', 'table')),
	text_content text not null check (char_length(text_content) > 0),
	start_rune integer not null check (start_rune >= 0),
	end_rune integer not null check (end_rune > start_rune),
	heading_level integer check (heading_level between 1 and 6),
	coordinate_json jsonb,
	created_at timestamptz not null default now(),
	constraint source_evidence_units_coordinate_check check (
		coordinate_json is null or (
			jsonb_typeof(coordinate_json)='object'
			and coordinate_json->>'kind' in (
				'pdf_region','slide_region','image_region','document_block','html_block','time_interval'
			)
		)
	),
	unique (revision_id, ordinal)
);

create table if not exists source_viewer_artifacts (
	revision_id text not null references source_evidence_revisions(id) on delete cascade,
	source_id text not null references source_sources(id) on delete cascade,
	notebook_id text not null references notebook_notebooks(id) on delete cascade,
	ordinal integer not null check (ordinal > 0 and ordinal <= 500),
	width integer not null check (width > 0),
	height integer not null check (height > 0),
	media_type text not null check (media_type='image/png'),
	byte_size bigint not null check (byte_size > 0),
	content_sha256 text not null check (content_sha256 ~ '^[0-9a-f]{64}$'),
	filename text not null check (filename ~ '^(page|slide)-[0-9]{6}\.png$'),
	object_key text not null unique check (char_length(object_key) between 1 and 1024),
	render_config_id text not null check (char_length(render_config_id) between 1 and 255),
	created_at timestamptz not null default now(),
	primary key (revision_id, ordinal),
	unique (revision_id, filename)
);

create table if not exists source_maps (
	id text primary key check (id ~ '^smap_[0-9a-f]{32}$'),
	source_id text not null references source_sources(id) on delete cascade,
	notebook_id text not null references notebook_notebooks(id) on delete cascade,
	revision_id text not null references source_evidence_revisions(id) on delete cascade,
	original_sha256 text not null check (original_sha256 ~ '^[0-9a-f]{64}$'),
	artifact_object_key text not null unique check (char_length(artifact_object_key) between 1 and 1024),
	artifact_sha256 text not null check (artifact_sha256 ~ '^[0-9a-f]{64}$'),
	artifact_bytes integer not null check (artifact_bytes between 1 and 16777216),
	parser_identity text not null check (char_length(parser_identity) between 1 and 128),
	parser_version text not null check (char_length(parser_version) between 1 and 64),
	parser_policy_id text not null check (char_length(parser_policy_id) between 1 and 160),
	navigation_kind text not null check (navigation_kind in ('embedded_outline','inferred_sections','page_samples')),
	confidence text not null check (confidence in ('high','medium','low')),
	page_count integer not null check (page_count between 1 and 500),
	entry_count integer not null check (entry_count between 1 and 4096),
	created_at timestamptz not null default now(),
	unique (revision_id,parser_policy_id)
);

create or replace function nano_reject_source_map_mutation()
returns trigger
language plpgsql
as $$
begin
	raise exception 'Source Map artifacts are immutable';
end
$$;

drop trigger if exists source_maps_immutable on source_maps;
-- Source Maps are append-only during normal operation. Source lifecycle purge
-- is the sole deletion path and relies on the parent foreign-key cascade.
create trigger source_maps_immutable before update on source_maps
	for each row execute function nano_reject_source_map_mutation();

alter table source_viewer_artifacts drop constraint if exists source_viewer_artifacts_filename_check;
alter table source_viewer_artifacts add constraint source_viewer_artifacts_filename_check
	check (filename ~ '^(page|slide)-[0-9]{6}\.png$');

alter table source_evidence_units add column if not exists coordinate_json jsonb;
alter table source_evidence_units drop constraint if exists source_evidence_units_coordinate_check;
alter table source_evidence_units add constraint source_evidence_units_coordinate_check check (
	coordinate_json is null or (
		jsonb_typeof(coordinate_json)='object'
		and coordinate_json->>'kind' in (
			'pdf_region','slide_region','image_region','document_block','html_block','time_interval'
		)
	)
);

create table if not exists retrieval_index_versions (
	id text primary key,
	config_json jsonb not null,
	config_sha256 text not null unique check (config_sha256 ~ '^[0-9a-f]{64}$'),
	status text not null check (status in ('candidate', 'active', 'retired')),
	promoted_by_eval_run_id text,
	created_at timestamptz not null default now(),
	promoted_at timestamptz,
	constraint retrieval_index_versions_promotion_check check (
		(status = 'candidate' and promoted_by_eval_run_id is null and promoted_at is null)
		or (status in ('active', 'retired') and promoted_by_eval_run_id is not null and promoted_at is not null)
	)
);

create unique index if not exists retrieval_index_versions_one_active_idx
	on retrieval_index_versions((status)) where status='active';

create table if not exists retrieval_eval_runs (
	id text primary key,
	index_version_id text not null references retrieval_index_versions(id) on delete cascade,
	fixture_suite_sha256 text not null check (fixture_suite_sha256 ~ '^[0-9a-f]{64}$'),
	status text not null check (status in ('passed', 'failed')),
	metrics_json jsonb not null,
	created_at timestamptz not null default now()
);

create table if not exists retrieval_source_index_builds (
	revision_id text not null references source_evidence_revisions(id) on delete cascade,
	index_version_id text not null references retrieval_index_versions(id) on delete cascade,
	source_id text not null references source_sources(id) on delete cascade,
	notebook_id text not null references notebook_notebooks(id) on delete cascade,
	expected_points integer not null check (expected_points > 0),
	projection_sha256 text not null check (projection_sha256 ~ '^[0-9a-f]{64}$'),
	status text not null check (status in ('building', 'verified')),
	created_at timestamptz not null default now(),
	verified_at timestamptz,
	constraint retrieval_source_index_builds_verification_check check (
		(status='building' and verified_at is null) or (status='verified' and verified_at is not null)
	),
	primary key (revision_id, index_version_id)
);

create index if not exists retrieval_source_index_builds_source_idx
	on retrieval_source_index_builds(source_id, index_version_id, status);

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

create table if not exists chat_source_selections (
	chat_id text not null references chat_chats(id) on delete cascade,
	source_id text not null references source_sources(id) on delete cascade,
	selected boolean not null,
	explicit boolean not null default false,
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now(),
	primary key(chat_id,source_id)
);

create index if not exists chat_source_selections_selected_idx
	on chat_source_selections(chat_id,source_id) where selected=true;

create table if not exists chat_messages (
	id text primary key,
	chat_id text not null references chat_chats(id) on delete cascade,
	role text not null check (role in ('user', 'assistant')),
	content text not null check (char_length(content) between 1 and 65536),
	created_at timestamptz not null default now()
);

create index if not exists chat_messages_order_idx
	on chat_messages(chat_id, created_at, id);

create table if not exists source_discovery_sessions (
	id text primary key,
	notebook_id text not null references notebook_notebooks(id) on delete cascade,
	user_id text not null references identity_users(id) on delete cascade,
	origin_chat_id text references chat_chats(id) on delete cascade,
	origin text not null check (origin in ('manual','research_agent')),
	query text not null check (char_length(query) between 1 and 500),
	summary text check (char_length(summary) <= 2000),
	status text not null check (status in ('searching','ready','failed')),
	research_run_id text,
	error_code text check (char_length(error_code) <= 64),
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now(),
	completed_at timestamptz,
	constraint source_discovery_sessions_completion_check check (
		(status='searching' and error_code is null and completed_at is null)
		or (status='ready' and error_code is null and completed_at is not null)
		or (status='failed' and error_code is not null and completed_at is not null)
	)
);

create index if not exists source_discovery_sessions_latest_idx
	on source_discovery_sessions(user_id,notebook_id,created_at desc,id desc);

create table if not exists source_discovery_candidates (
	id text primary key,
	session_id text not null references source_discovery_sessions(id) on delete cascade,
	ordinal integer not null check (ordinal between 0 and 19),
	title text not null check (char_length(title) between 1 and 300),
	canonical_url text not null check (char_length(canonical_url) between 1 and 4096),
	display_url text not null check (char_length(display_url) between 1 and 4096),
	snippet text not null default '' check (char_length(snippet) <= 1000),
	favicon_ref text check (char_length(favicon_ref) <= 4096),
	selected boolean not null default true,
	status text not null default 'discovered' check (status in ('discovered','importing','imported','import_failed')),
	source_id text references source_sources(id) on delete set null,
	import_error_code text check (char_length(import_error_code) <= 64),
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now(),
	unique(session_id,ordinal),
	unique(session_id,canonical_url)
);

create table if not exists source_discovery_jobs (
	id text primary key,
	session_id text not null unique references source_discovery_sessions(id) on delete cascade,
	status text not null check (status in ('queued','running','succeeded','failed','cancelled')),
	attempt_no integer not null default 0 check (attempt_no between 0 and 3),
	available_at timestamptz not null default now(),
	lease_token uuid,
	lease_expires_at timestamptz,
	last_error_code text check (char_length(last_error_code) <= 64),
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now(),
	constraint source_discovery_jobs_lease_check check (
		(status='queued' and lease_token is null and lease_expires_at is null)
		or (status='running' and lease_token is not null and lease_expires_at is not null)
		or (status in ('succeeded','failed','cancelled') and lease_token is null and lease_expires_at is null)
	)
);

create index if not exists source_discovery_jobs_claim_idx
	on source_discovery_jobs(available_at,created_at,id) where status='queued';

create table if not exists agent_prompt_versions (
	prompt_identity text not null check (char_length(prompt_identity) between 3 and 255),
	prompt_version integer not null check (prompt_version > 0),
	output_contract text not null check (char_length(output_contract) between 3 and 255),
	canonical_sha256 text not null check (canonical_sha256 ~ '^[0-9a-f]{64}$'),
	content text not null check (char_length(content) > 0),
	source_path text not null check (char_length(source_path) between 1 and 500),
	registered_at timestamptz not null default now(),
	primary key (prompt_identity,prompt_version)
);

create or replace function nano_reject_agent_prompt_version_mutation()
returns trigger
language plpgsql
as $$
begin
	raise exception 'agent_prompt_versions is immutable';
end
$$;

drop trigger if exists agent_prompt_versions_immutable on agent_prompt_versions;
create trigger agent_prompt_versions_immutable
	before update or delete on agent_prompt_versions
	for each row execute function nano_reject_agent_prompt_version_mutation();

revoke all on agent_prompt_versions from nano_app, nano_worker;
grant select on agent_prompt_versions to nano_worker;

create table if not exists agent_skill_versions (
	skill_identity text not null check (char_length(skill_identity) between 3 and 255),
	skill_version integer not null check (skill_version > 0),
	canonical_sha256 text not null check (canonical_sha256 ~ '^[0-9a-f]{64}$'),
	name text not null check (char_length(name) between 1 and 100),
	description text not null check (char_length(description) between 1 and 500),
	body text not null check (char_length(body) > 0),
	canonical_payload jsonb not null check (jsonb_typeof(canonical_payload)='object'),
	source_path text not null check (char_length(source_path) between 1 and 500),
	registered_at timestamptz not null default now(),
	primary key (skill_identity,skill_version)
);

create table if not exists agent_contract_versions (
	contract_identity text not null check (char_length(contract_identity) between 3 and 255),
	contract_version integer not null check (contract_version > 0),
	canonical_sha256 text not null check (canonical_sha256 ~ '^[0-9a-f]{64}$'),
	json_schema jsonb not null check (jsonb_typeof(json_schema)='object'),
	canonical_payload jsonb not null check (jsonb_typeof(canonical_payload)='object'),
	source_path text not null check (char_length(source_path) between 1 and 500),
	registered_at timestamptz not null default now(),
	primary key (contract_identity,contract_version)
);

create table if not exists agent_model_policy_versions (
	policy_identity text not null check (char_length(policy_identity) between 3 and 255),
	policy_version integer not null check (policy_version > 0),
	canonical_sha256 text not null check (canonical_sha256 ~ '^[0-9a-f]{64}$'),
	provider_model text not null check (char_length(provider_model) between 1 and 255),
	temperature double precision not null check (temperature >= 0),
	max_output_tokens integer not null check (max_output_tokens > 0),
	timeout_ms integer not null check (timeout_ms > 0),
	enable_thinking boolean not null default false,
	canonical_payload jsonb not null check (jsonb_typeof(canonical_payload)='object'),
	source_path text not null check (char_length(source_path) between 1 and 500),
	registered_at timestamptz not null default now(),
	primary key (policy_identity,policy_version)
);

alter table agent_model_policy_versions
	add column if not exists enable_thinking boolean not null default false;

create table if not exists provider_model_capability_versions (
	capability_identity text not null check (char_length(capability_identity) between 3 and 255),
	capability_version integer not null check (capability_version > 0),
	canonical_sha256 text not null check (canonical_sha256 ~ '^[0-9a-f]{64}$'),
	provider_model text not null check (char_length(provider_model) between 1 and 255),
	resolved_model text not null check (char_length(resolved_model) between 1 and 255),
	context_window_tokens integer not null check (context_window_tokens > 0),
	max_input_tokens integer not null check (max_input_tokens > 0 and max_input_tokens <= context_window_tokens),
	max_output_tokens integer not null check (max_output_tokens > 0),
	tokenizer_identity text not null check (char_length(tokenizer_identity) between 1 and 255),
	tokenizer_version text not null check (char_length(tokenizer_version) between 1 and 255),
	invocation_mode text not null check (char_length(invocation_mode) between 1 and 64),
	canonical_payload jsonb not null check (jsonb_typeof(canonical_payload)='object'),
	source_path text not null check (char_length(source_path) between 1 and 500),
	registered_at timestamptz not null default now(),
	primary key (capability_identity,capability_version)
);

create table if not exists agent_model_context_policy_versions (
	context_policy_identity text not null check (char_length(context_policy_identity) between 3 and 255),
	context_policy_version integer not null check (context_policy_version > 0),
	canonical_sha256 text not null check (canonical_sha256 ~ '^[0-9a-f]{64}$'),
	model_policy_identity text not null,
	model_policy_version integer not null,
	capability_identity text not null,
	capability_version integer not null,
	pinned_max_output_tokens integer not null check (pinned_max_output_tokens > 0),
	soft_input_limit_tokens integer not null check (soft_input_limit_tokens > 0),
	estimation_safety_tokens integer not null check (estimation_safety_tokens > 0),
	keep_recent_tokens integer not null check (keep_recent_tokens > 0),
	summary_max_output_tokens integer not null check (summary_max_output_tokens > 0),
	overflow_retry_limit integer not null check (overflow_retry_limit > 0),
	canonical_payload jsonb not null check (jsonb_typeof(canonical_payload)='object'),
	source_path text not null check (char_length(source_path) between 1 and 500),
	registered_at timestamptz not null default now(),
	primary key (context_policy_identity,context_policy_version),
	unique (model_policy_identity,model_policy_version),
	foreign key (model_policy_identity,model_policy_version)
		references agent_model_policy_versions(policy_identity,policy_version),
	foreign key (capability_identity,capability_version)
		references provider_model_capability_versions(capability_identity,capability_version)
);

create table if not exists agent_definition_versions (
	definition_identity text not null check (char_length(definition_identity) between 3 and 255),
	definition_version integer not null check (definition_version > 0),
	canonical_sha256 text not null check (canonical_sha256 ~ '^[0-9a-f]{64}$'),
	executor text not null check (char_length(executor) between 1 and 255),
	model_policy_identity text not null,
	model_policy_version integer not null,
	prompt_bindings jsonb not null check (jsonb_typeof(prompt_bindings)='object'),
	input_contract_identity text not null,
	input_contract_version integer not null,
	result_contract_identity text not null,
	result_contract_version integer not null,
	skill_allowlist jsonb not null default '[]'::jsonb check (jsonb_typeof(skill_allowlist)='array'),
	tool_allowlist jsonb not null check (jsonb_typeof(tool_allowlist)='array'),
	children jsonb not null check (jsonb_typeof(children)='array'),
	limits jsonb not null check (jsonb_typeof(limits)='object'),
	delegation jsonb check (delegation is null or jsonb_typeof(delegation)='object'),
	canonical_payload jsonb not null check (jsonb_typeof(canonical_payload)='object'),
	source_path text not null check (char_length(source_path) between 1 and 500),
	registered_at timestamptz not null default now(),
	primary key (definition_identity,definition_version),
	foreign key (model_policy_identity,model_policy_version)
		references agent_model_policy_versions(policy_identity,policy_version),
	foreign key (input_contract_identity,input_contract_version)
		references agent_contract_versions(contract_identity,contract_version),
	foreign key (result_contract_identity,result_contract_version)
		references agent_contract_versions(contract_identity,contract_version)
);

alter table agent_definition_versions
	add column if not exists skill_allowlist jsonb not null default '[]'::jsonb check (jsonb_typeof(skill_allowlist)='array');

create table if not exists agent_release_manifests (
	release_identity text not null check (char_length(release_identity) between 3 and 255),
	release_version integer not null check (release_version > 0),
	canonical_sha256 text not null check (canonical_sha256 ~ '^[0-9a-f]{64}$'),
	roots jsonb not null check (jsonb_typeof(roots)='object'),
	canonical_payload jsonb not null check (jsonb_typeof(canonical_payload)='object'),
	source_path text not null check (char_length(source_path) between 1 and 500),
	registered_at timestamptz not null default now(),
	primary key (release_identity,release_version)
);

create or replace function nano_reject_agent_catalog_mutation()
returns trigger
language plpgsql
as $$
begin
	raise exception 'Agent catalog records are immutable';
end
$$;

drop trigger if exists agent_contract_versions_immutable on agent_contract_versions;
create trigger agent_contract_versions_immutable before update or delete on agent_contract_versions
	for each row execute function nano_reject_agent_catalog_mutation();
drop trigger if exists agent_skill_versions_immutable on agent_skill_versions;
create trigger agent_skill_versions_immutable before update or delete on agent_skill_versions
	for each row execute function nano_reject_agent_catalog_mutation();
drop trigger if exists agent_model_policy_versions_immutable on agent_model_policy_versions;
create trigger agent_model_policy_versions_immutable before update or delete on agent_model_policy_versions
	for each row execute function nano_reject_agent_catalog_mutation();
drop trigger if exists provider_model_capability_versions_immutable on provider_model_capability_versions;
create trigger provider_model_capability_versions_immutable before update or delete on provider_model_capability_versions
	for each row execute function nano_reject_agent_catalog_mutation();
drop trigger if exists agent_model_context_policy_versions_immutable on agent_model_context_policy_versions;
create trigger agent_model_context_policy_versions_immutable before update or delete on agent_model_context_policy_versions
	for each row execute function nano_reject_agent_catalog_mutation();
drop trigger if exists agent_definition_versions_immutable on agent_definition_versions;
create trigger agent_definition_versions_immutable before update or delete on agent_definition_versions
	for each row execute function nano_reject_agent_catalog_mutation();
drop trigger if exists agent_release_manifests_immutable on agent_release_manifests;
create trigger agent_release_manifests_immutable before update or delete on agent_release_manifests
	for each row execute function nano_reject_agent_catalog_mutation();

revoke all on agent_skill_versions,agent_contract_versions,agent_model_policy_versions,provider_model_capability_versions,agent_model_context_policy_versions,agent_definition_versions,agent_release_manifests from nano_app,nano_worker;
grant select on agent_skill_versions,agent_contract_versions,agent_model_policy_versions,provider_model_capability_versions,agent_model_context_policy_versions,agent_definition_versions,agent_release_manifests to nano_worker;

create table if not exists agent_prompt_sets (
	id text primary key check (char_length(id) between 3 and 255),
	canonical_sha256 text not null check (canonical_sha256 ~ '^[0-9a-f]{64}$'),
	bindings jsonb not null check (jsonb_typeof(bindings)='object'),
	registered_at timestamptz not null default now()
);

create table if not exists agent_configuration_sets (
	id text primary key check (char_length(id) between 3 and 255),
	canonical_sha256 text not null check (canonical_sha256 ~ '^[0-9a-f]{64}$'),
	prompt_set_id text not null references agent_prompt_sets(id),
	prompt_set_sha256 text not null check (prompt_set_sha256 ~ '^[0-9a-f]{64}$'),
	registered_at timestamptz not null default now()
);

create table if not exists agent_role_profiles (
	configuration_set_id text not null references agent_configuration_sets(id),
	role text not null check (role in ('leader','research')),
	canonical_sha256 text not null check (canonical_sha256 ~ '^[0-9a-f]{64}$'),
	executor_version text not null check (char_length(executor_version) between 3 and 255),
	model text not null check (char_length(model) between 1 and 255),
	prompt_purposes jsonb not null check (jsonb_typeof(prompt_purposes)='array'),
	tool_allowlist jsonb not null check (jsonb_typeof(tool_allowlist)='array'),
	run_config jsonb not null check (jsonb_typeof(run_config)='object'),
	max_attempts integer not null check (max_attempts between 1 and 10),
	registered_at timestamptz not null default now(),
	primary key (configuration_set_id,role)
);

create or replace function nano_reject_agent_configuration_mutation()
returns trigger
language plpgsql
as $$
begin
	raise exception 'Agent configuration records are immutable';
end
$$;

drop trigger if exists agent_prompt_sets_immutable on agent_prompt_sets;
create trigger agent_prompt_sets_immutable before update or delete on agent_prompt_sets
	for each row execute function nano_reject_agent_configuration_mutation();
drop trigger if exists agent_configuration_sets_immutable on agent_configuration_sets;
create trigger agent_configuration_sets_immutable before update or delete on agent_configuration_sets
	for each row execute function nano_reject_agent_configuration_mutation();
drop trigger if exists agent_role_profiles_immutable on agent_role_profiles;
create trigger agent_role_profiles_immutable before update or delete on agent_role_profiles
	for each row execute function nano_reject_agent_configuration_mutation();

revoke all on agent_prompt_sets,agent_configuration_sets,agent_role_profiles from nano_app,nano_worker;
grant select on agent_prompt_sets,agent_configuration_sets,agent_role_profiles to nano_worker;

create table if not exists agent_runs (
	id text primary key,
	user_id text not null references identity_users(id) on delete cascade,
	chat_id text not null references chat_chats(id) on delete cascade,
	input_message_id text not null references chat_messages(id) on delete restrict,
	output_message_id text unique references chat_messages(id) on delete restrict,
	status text not null check (status in ('queued', 'running', 'completed', 'failed', 'cancelled')),
	model text not null,
	prompt_version text not null,
	agent_config_id text not null default 'nano-interactive-v1' check (char_length(agent_config_id) between 1 and 255),
	executor_version text not null default 'leader-executor-v1' check (char_length(executor_version) between 3 and 255),
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

alter table agent_runs add column if not exists agent_config_id text not null default 'nano-interactive-v1';
alter table agent_runs drop constraint if exists agent_runs_agent_config_id_check;
alter table agent_runs add constraint agent_runs_agent_config_id_check check (char_length(agent_config_id) between 1 and 255);
alter table agent_runs add column if not exists executor_version text not null default 'leader-executor-v1';
alter table agent_runs drop constraint if exists agent_runs_executor_version_check;
alter table agent_runs add constraint agent_runs_executor_version_check check (char_length(executor_version) between 3 and 255);

alter table agent_runs add column if not exists selected_source_count integer not null default 0
	check (selected_source_count between 0 and 50);

alter table agent_runs add column if not exists agent_role text not null default 'leader';
alter table agent_runs add column if not exists parent_run_id text references agent_runs(id) on delete cascade;
alter table agent_runs add column if not exists discovery_session_id text references source_discovery_sessions(id) on delete set null;
alter table agent_runs drop constraint if exists agent_runs_agent_role_check;
alter table agent_runs add constraint agent_runs_agent_role_check check (agent_role in ('leader','research'));
alter table agent_runs drop constraint if exists agent_runs_role_shape_check;
alter table agent_runs add constraint agent_runs_role_shape_check check (
	(agent_role='leader')
	or (agent_role='research' and output_message_id is null)
);

create table if not exists agent_trees (
	id text primary key check (char_length(id) between 3 and 255),
	root_agent_run_id text unique references agent_runs(id) on delete cascade,
	absolute_deadline timestamptz not null,
	model_call_limit integer not null check (model_call_limit > 0),
	action_limit integer not null check (action_limit > 0),
	context_byte_limit integer not null check (context_byte_limit > 0),
	result_byte_limit integer not null check (result_byte_limit > 0),
	model_calls_consumed integer not null default 0 check (model_calls_consumed between 0 and model_call_limit),
	actions_consumed integer not null default 0 check (actions_consumed between 0 and action_limit),
	context_bytes_consumed bigint not null default 0 check (context_bytes_consumed between 0 and context_byte_limit),
	result_bytes_consumed bigint not null default 0 check (result_bytes_consumed between 0 and result_byte_limit),
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now()
);

alter table agent_runs add column if not exists runtime_kind text not null default 'legacy_role';
alter table agent_runs drop constraint if exists agent_runs_runtime_kind_check;
alter table agent_runs add constraint agent_runs_runtime_kind_check check (runtime_kind in ('legacy_role','configured'));
alter table agent_runs add column if not exists tree_id text references agent_trees(id) on delete cascade;
alter table agent_runs add column if not exists definition_identity text;
alter table agent_runs add column if not exists definition_version integer;
alter table agent_runs add column if not exists definition_sha256 text;
alter table agent_runs add column if not exists executor_identity text;
alter table agent_runs add column if not exists model_policy_identity text;
alter table agent_runs add column if not exists model_policy_version integer;
alter table agent_runs add column if not exists model_policy_sha256 text;
alter table agent_runs add column if not exists provider_model text;
alter table agent_runs add column if not exists provider_capability_identity text;
alter table agent_runs add column if not exists provider_capability_version integer;
alter table agent_runs add column if not exists provider_capability_sha256 text;
alter table agent_runs add column if not exists model_context_policy_identity text;
alter table agent_runs add column if not exists model_context_policy_version integer;
alter table agent_runs add column if not exists model_context_policy_sha256 text;
alter table agent_runs add column if not exists parent_context_manifest jsonb;

alter table agent_runs add column if not exists time_zone text not null default 'UTC';
alter table agent_runs add column if not exists deadline_at timestamptz not null default (now() + interval '10 minutes');
alter table agent_runs add column if not exists action_decision_limit integer not null default 4;
alter table agent_runs add column if not exists final_decision_limit integer not null default 1;
alter table agent_runs add column if not exists action_limit integer not null default 8;
alter table agent_runs add column if not exists action_batch_limit integer not null default 4;
alter table agent_runs add column if not exists action_result_byte_limit integer not null default 16384;
alter table agent_runs add column if not exists action_results_byte_limit integer not null default 65536;

alter table agent_runs alter column user_id drop not null;
alter table agent_runs alter column chat_id drop not null;
alter table agent_runs alter column input_message_id drop not null;
alter table agent_runs alter column model drop not null;
alter table agent_runs alter column prompt_version drop not null;
alter table agent_runs alter column agent_config_id drop not null;
alter table agent_runs alter column executor_version drop not null;
alter table agent_runs alter column agent_role drop not null;
alter table agent_runs alter column time_zone drop not null;
alter table agent_runs alter column deadline_at drop not null;
alter table agent_runs alter column action_decision_limit drop not null;
alter table agent_runs alter column final_decision_limit drop not null;
alter table agent_runs alter column action_limit drop not null;
alter table agent_runs alter column action_batch_limit drop not null;
alter table agent_runs alter column action_result_byte_limit drop not null;
alter table agent_runs alter column action_results_byte_limit drop not null;

create or replace function nano_normalize_configured_agent_run()
returns trigger
language plpgsql
as $$
begin
	if new.runtime_kind='configured' then
		new.agent_role = null;
		new.agent_config_id = null;
		new.executor_version = null;
		new.model = null;
		new.prompt_version = null;
		new.time_zone = null;
		new.deadline_at = null;
		new.action_decision_limit = null;
		new.final_decision_limit = null;
		new.action_limit = null;
		new.action_batch_limit = null;
		new.action_result_byte_limit = null;
		new.action_results_byte_limit = null;
	end if;
	return new;
end
$$;

drop trigger if exists agent_runs_normalize_configured on agent_runs;
create trigger agent_runs_normalize_configured before insert or update on agent_runs
	for each row execute function nano_normalize_configured_agent_run();

create or replace function nano_require_configured_agent_run_neutrality()
returns trigger
language plpgsql
as $$
begin
	if exists(select 1 from agent_runs where id=new.id and runtime_kind='configured' and user_id is not null) then
		raise exception 'configured Agent Run retained product ownership';
	end if;
	return null;
end
$$;

drop trigger if exists agent_runs_configured_neutrality on agent_runs;
create constraint trigger agent_runs_configured_neutrality
	after insert or update of user_id on agent_runs
	deferrable initially deferred
	for each row execute function nano_require_configured_agent_run_neutrality();

alter table agent_runs drop constraint if exists agent_runs_configured_shape_check;
alter table agent_runs add constraint agent_runs_configured_shape_check check (
	runtime_kind='legacy_role'
	or (
		runtime_kind='configured' and tree_id is not null
		and definition_identity is not null and definition_version > 0 and definition_sha256 ~ '^[0-9a-f]{64}$'
		and executor_identity is not null
		and model_policy_identity is not null and model_policy_version > 0 and model_policy_sha256 ~ '^[0-9a-f]{64}$'
		and provider_model is not null
		and agent_role is null and agent_config_id is null and executor_version is null
	)
);
alter table agent_runs drop constraint if exists agent_runs_definition_reference_fkey;
alter table agent_runs add constraint agent_runs_definition_reference_fkey
	foreign key (definition_identity,definition_version)
	references agent_definition_versions(definition_identity,definition_version);
alter table agent_runs drop constraint if exists agent_runs_model_policy_reference_fkey;
alter table agent_runs add constraint agent_runs_model_policy_reference_fkey
	foreign key (model_policy_identity,model_policy_version)
	references agent_model_policy_versions(policy_identity,policy_version);
alter table agent_runs drop constraint if exists agent_runs_provider_capability_reference_fkey;
alter table agent_runs add constraint agent_runs_provider_capability_reference_fkey
	foreign key (provider_capability_identity,provider_capability_version)
	references provider_model_capability_versions(capability_identity,capability_version);
alter table agent_runs drop constraint if exists agent_runs_model_context_policy_reference_fkey;
alter table agent_runs add constraint agent_runs_model_context_policy_reference_fkey
	foreign key (model_context_policy_identity,model_context_policy_version)
	references agent_model_context_policy_versions(context_policy_identity,context_policy_version);

create table if not exists chat_runs (
	id text primary key check (char_length(id) between 3 and 255),
	user_id text not null references identity_users(id) on delete cascade,
	chat_id text not null references chat_chats(id) on delete cascade,
	input_message_id text not null references chat_messages(id) on delete restrict,
	output_message_id text unique references chat_messages(id) on delete restrict,
	root_agent_run_id text not null unique references agent_runs(id) on delete cascade,
	status text not null check (status in ('queued','running','completed','failed','cancelled')),
	error_code text check (char_length(error_code) between 1 and 64),
	created_at timestamptz not null default now(),
	started_at timestamptz,
	finished_at timestamptz,
	updated_at timestamptz not null default now()
);

create or replace function nano_sync_chat_run_projection()
returns trigger
language plpgsql
as $$
begin
	update chat_runs
	set output_message_id=new.output_message_id,status=new.status,error_code=new.error_code,
		started_at=new.started_at,finished_at=new.finished_at,updated_at=new.updated_at
	where root_agent_run_id=new.id;
	return new;
end
$$;

drop trigger if exists agent_runs_sync_chat_run on agent_runs;
create trigger agent_runs_sync_chat_run
	after update of output_message_id,status,error_code,started_at,finished_at,updated_at on agent_runs
	for each row execute function nano_sync_chat_run_projection();

create table if not exists research_sessions (
	id text primary key check (char_length(id) between 3 and 255),
	user_id text not null references identity_users(id) on delete cascade,
	chat_id text not null references chat_chats(id) on delete cascade,
	input_message_id text not null unique references chat_messages(id) on delete restrict,
	status text not null check (status in ('planning','awaiting_confirmation','queued','running','publishing','completed','failed','cancelled')),
	planning_run_id text unique references agent_runs(id) on delete set null,
	accepted_plan_version integer,
	execution_run_id text unique references agent_runs(id) on delete set null,
	current_report_version integer,
	error_code text check (error_code is null or char_length(error_code) between 1 and 64),
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now(),
	completed_at timestamptz,
	check (status not in ('queued','running','publishing','completed') or accepted_plan_version is not null),
	check (status not in ('queued','running','publishing','completed') or execution_run_id is not null),
	check (status <> 'completed' or current_report_version is not null)
);

create table if not exists research_plan_versions (
	session_id text not null references research_sessions(id) on delete cascade,
	version integer not null check (version > 0),
	plan_json jsonb not null check (jsonb_typeof(plan_json)='object'),
	producer_run_id text references agent_runs(id) on delete set null,
	created_by text not null check (created_by in ('model','member')),
	created_at timestamptz not null default now(),
	primary key (session_id,version)
);

create table if not exists research_report_versions (
	session_id text not null references research_sessions(id) on delete cascade,
	version integer not null check (version > 0),
	producer_run_id text not null unique references agent_runs(id) on delete restrict,
	content_markdown text not null check (char_length(content_markdown) > 0),
	evidence_stats jsonb not null default '{}'::jsonb check (jsonb_typeof(evidence_stats)='object'),
	created_at timestamptz not null default now(),
	primary key (session_id,version)
);

create table if not exists research_evidence_ledger (
	session_id text not null references research_sessions(id) on delete cascade,
	url text not null check (char_length(url) between 1 and 4096),
	final_url text check (final_url is null or char_length(final_url) between 1 and 4096),
	title text not null default '' check (char_length(title) <= 2048),
	status text not null check (status in ('discovered','read','failed')),
	read_run_id text references agent_runs(id) on delete set null,
	read_action_id text,
	content_sha256 text check (content_sha256 is null or content_sha256 ~ '^[0-9a-f]{64}$'),
	word_count integer check (word_count is null or word_count >= 0),
	engine text,
	first_seen_at timestamptz not null default now(),
	last_seen_at timestamptz not null default now(),
	primary key (session_id,url)
);

create table if not exists research_source_imports (
	session_id text not null references research_sessions(id) on delete cascade,
	run_id text not null references agent_runs(id) on delete cascade,
	action_id text not null check (char_length(action_id) between 1 and 255),
	requested_url text not null check (char_length(requested_url) between 1 and 4096),
	final_url_identity text check (final_url_identity is null or char_length(final_url_identity) between 1 and 4096),
	source_id text references source_sources(id) on delete set null,
	processing_job_id text references source_processing_jobs(id) on delete set null,
	barrier_observed_attempt_no integer check (barrier_observed_attempt_no is null or barrier_observed_attempt_no > 0),
	retrieval_error_code text check (retrieval_error_code is null or retrieval_error_code ~ '^[a-z][a-z0-9_]{2,63}$'),
	created_at timestamptz not null default now(),
	unique (run_id, action_id)
);

alter table research_source_imports add column if not exists barrier_observed_attempt_no integer
	check (barrier_observed_attempt_no is null or barrier_observed_attempt_no > 0);
alter table research_source_imports add column if not exists retrieval_error_code text
	check (retrieval_error_code is null or retrieval_error_code ~ '^[a-z][a-z0-9_]{2,63}$');

create index if not exists research_source_imports_session_created_idx
	on research_source_imports(session_id,created_at,action_id);

create index if not exists research_source_imports_source_idx
	on research_source_imports(source_id)
	where source_id is not null;

alter table research_evidence_ledger add column if not exists media_type text
	check (media_type is null or media_type in ('text/html','application/pdf'));
alter table research_evidence_ledger add column if not exists page_count integer
	check (page_count is null or page_count > 0);
alter table research_evidence_ledger add column if not exists document_handle text
	check (document_handle is null or document_handle ~ '^rdoc_[0-9a-f]{32}$');
alter table research_evidence_ledger add column if not exists failure_reason text
	check (failure_reason is null or char_length(failure_reason) between 1 and 64);

create table if not exists research_step_capsules (
	session_id text not null references research_sessions(id) on delete cascade,
	run_id text not null references agent_runs(id) on delete cascade,
	decision_no integer not null check (decision_no > 0),
	start_checkpoint_seq integer not null check (start_checkpoint_seq > 0),
	end_checkpoint_seq integer not null check (end_checkpoint_seq >= start_checkpoint_seq),
	content_markdown text not null check (char_length(content_markdown) > 0),
	source_checkpoint_sha256 text not null check (source_checkpoint_sha256 ~ '^[0-9a-f]{64}$'),
	created_at timestamptz not null default now(),
	primary key (session_id,run_id,decision_no),
	unique (run_id,decision_no)
);

create table if not exists research_rollups (
	session_id text not null references research_sessions(id) on delete cascade,
	version integer not null check (version > 0),
	first_decision_no integer not null check (first_decision_no > 0),
	last_decision_no integer not null check (last_decision_no >= first_decision_no),
	content_markdown text not null check (char_length(content_markdown) > 0),
	source_capsules_sha256 text not null check (source_capsules_sha256 ~ '^[0-9a-f]{64}$'),
	created_at timestamptz not null default now(),
	primary key (session_id,version)
);

create table if not exists research_archival_capsules (
	id text primary key check (id ~ '^rcap_[0-9a-f]{64}$'),
	session_id text not null references research_sessions(id) on delete cascade,
	run_id text not null references agent_runs(id) on delete cascade,
	decision_no integer not null check (decision_no > 0),
	start_checkpoint_seq integer not null check (start_checkpoint_seq > 0),
	end_checkpoint_seq integer not null check (end_checkpoint_seq >= start_checkpoint_seq),
	source_checkpoint_sha256 text not null check (source_checkpoint_sha256 ~ '^[0-9a-f]{64}$'),
	capsule_json jsonb not null check (jsonb_typeof(capsule_json)='object'),
	capsule_sha256 text not null check (capsule_sha256 ~ '^[0-9a-f]{64}$'),
	capsule_bytes integer not null check (capsule_bytes between 1 and 8192),
	summarizer_model text not null check (char_length(summarizer_model) between 1 and 255),
	prompt_version text not null check (char_length(prompt_version) between 1 and 255),
	model_context_policy_identity text not null,
	model_context_policy_version integer not null check (model_context_policy_version > 0),
	model_context_policy_sha256 text not null check (model_context_policy_sha256 ~ '^[0-9a-f]{64}$'),
	created_at timestamptz not null default now(),
	unique (run_id,decision_no)
);

create table if not exists research_task_memories (
	id text primary key check (id ~ '^rmem_[0-9a-f]{64}$'),
	session_id text not null references research_sessions(id) on delete cascade,
	run_id text not null references agent_runs(id) on delete cascade,
	first_decision_no integer not null check (first_decision_no > 0),
	last_decision_no integer not null check (last_decision_no >= first_decision_no),
	start_checkpoint_seq integer not null check (start_checkpoint_seq > 0),
	end_checkpoint_seq integer not null check (end_checkpoint_seq >= start_checkpoint_seq),
	source_capsules_sha256 text not null check (source_capsules_sha256 ~ '^[0-9a-f]{64}$'),
	memory_json jsonb not null check (jsonb_typeof(memory_json)='object'),
	memory_sha256 text not null check (memory_sha256 ~ '^[0-9a-f]{64}$'),
	memory_bytes integer not null check (memory_bytes between 1 and 32768),
	summarizer_model text not null check (char_length(summarizer_model) between 1 and 255),
	prompt_version text not null check (char_length(prompt_version) between 1 and 255),
	model_context_policy_identity text not null,
	model_context_policy_version integer not null check (model_context_policy_version > 0),
	model_context_policy_sha256 text not null check (model_context_policy_sha256 ~ '^[0-9a-f]{64}$'),
	created_at timestamptz not null default now(),
	unique (run_id,start_checkpoint_seq,end_checkpoint_seq,model_context_policy_identity,model_context_policy_version)
);

create table if not exists research_compaction_failures (
	id text primary key check (id ~ '^rcfail_[0-9a-f]{64}$'),
	session_id text not null references research_sessions(id) on delete cascade,
	run_id text not null references agent_runs(id) on delete cascade,
	attempt_no integer not null check (attempt_no > 0),
	layer text not null check (layer in ('archival','task_memory')),
	reason_code text not null check (reason_code ~ '^[a-z][a-z0-9_]{2,63}$'),
	start_checkpoint_seq integer not null check (start_checkpoint_seq >= 0),
	end_checkpoint_seq integer not null check (end_checkpoint_seq >= start_checkpoint_seq),
	before_tokens integer not null check (before_tokens > 0),
	after_tokens integer not null check (after_tokens >= 0),
	model_context_policy_identity text not null,
	model_context_policy_version integer not null check (model_context_policy_version > 0),
	model_context_policy_sha256 text not null check (model_context_policy_sha256 ~ '^[0-9a-f]{64}$'),
	created_at timestamptz not null default now()
);

create or replace function nano_reject_research_artifact_mutation()
returns trigger
language plpgsql
as $$
begin
	raise exception 'Research artifact versions are immutable';
end
$$;

drop trigger if exists research_plan_versions_immutable on research_plan_versions;
create trigger research_plan_versions_immutable before update or delete on research_plan_versions
	for each row execute function nano_reject_research_artifact_mutation();
drop trigger if exists research_report_versions_immutable on research_report_versions;
create trigger research_report_versions_immutable before update or delete on research_report_versions
	for each row execute function nano_reject_research_artifact_mutation();
drop trigger if exists research_step_capsules_immutable on research_step_capsules;
create trigger research_step_capsules_immutable before update or delete on research_step_capsules
	for each row execute function nano_reject_research_artifact_mutation();
drop trigger if exists research_rollups_immutable on research_rollups;
create trigger research_rollups_immutable before update or delete on research_rollups
	for each row execute function nano_reject_research_artifact_mutation();
drop trigger if exists research_archival_capsules_immutable on research_archival_capsules;
create trigger research_archival_capsules_immutable before update on research_archival_capsules
	for each row execute function nano_reject_research_artifact_mutation();
drop trigger if exists research_task_memories_immutable on research_task_memories;
create trigger research_task_memories_immutable before update on research_task_memories
	for each row execute function nano_reject_research_artifact_mutation();

create table if not exists agent_run_results (
	id text primary key check (char_length(id) between 3 and 255),
	producer_run_id text not null unique references agent_runs(id) on delete cascade,
	contract_identity text not null,
	contract_version integer not null,
	contract_sha256 text not null check (contract_sha256 ~ '^[0-9a-f]{64}$'),
	payload jsonb not null check (jsonb_typeof(payload)='object'),
	payload_sha256 text not null check (payload_sha256 ~ '^[0-9a-f]{64}$'),
	payload_bytes integer not null check (payload_bytes > 0 and payload_bytes <= 1048576),
	created_at timestamptz not null default now(),
	foreign key (contract_identity,contract_version)
		references agent_contract_versions(contract_identity,contract_version)
);

create or replace function nano_reject_agent_run_result_mutation()
returns trigger
language plpgsql
as $$
begin
	raise exception 'agent_run_results is immutable';
end
$$;

drop trigger if exists agent_run_results_immutable on agent_run_results;
create trigger agent_run_results_immutable before update or delete on agent_run_results
	for each row execute function nano_reject_agent_run_result_mutation();

create table if not exists studio_outputs (
	id text primary key check (char_length(id) between 3 and 255),
	notebook_id text not null references notebook_notebooks(id) on delete cascade,
	created_by_user_id text references identity_users(id) on delete set null,
	kind text not null check (kind in ('report','flashcards','mind_map','data_table')),
	locale text not null check (char_length(locale) between 2 and 16),
	title text check (title is null or char_length(title) between 1 and 200),
	root_agent_run_id text not null unique references agent_runs(id) on delete cascade,
	result_id text unique references agent_run_results(id) on delete restrict,
	selected_source_count integer not null check (selected_source_count between 1 and 50),
	status text not null check (status in ('queued','running','completed','failed','cancelled')),
	error_code text check (error_code is null or error_code ~ '^[a-z][a-z0-9_]{1,63}$'),
	artifact jsonb check (artifact is null or jsonb_typeof(artifact)='object'),
	idempotency_key text not null check (char_length(idempotency_key) between 1 and 255),
	request_hash text not null check (request_hash ~ '^[0-9a-f]{64}$'),
	created_at timestamptz not null default now(),
	started_at timestamptz,
	finished_at timestamptz,
	updated_at timestamptz not null default now(),
	unique (created_by_user_id,idempotency_key),
	constraint studio_outputs_publication_shape check (
		(status='completed' and artifact is not null and title is not null and result_id is not null and error_code is null)
		or (status<>'completed' and artifact is null and result_id is null and title is null)
	)
);

create index if not exists studio_outputs_notebook_recent_idx
	on studio_outputs(notebook_id,created_at desc,id desc);

create or replace function nano_reject_completed_studio_artifact_mutation()
returns trigger
language plpgsql
as $$
begin
	if old.status='completed' then
		raise exception 'completed Studio Output artifact is immutable';
	end if;
	return new;
end
$$;

drop trigger if exists studio_outputs_completed_artifact_immutable on studio_outputs;
create trigger studio_outputs_completed_artifact_immutable
	before update of title,artifact,result_id on studio_outputs
	for each row execute function nano_reject_completed_studio_artifact_mutation();

create or replace function nano_sync_studio_output_projection()
returns trigger
language plpgsql
as $$
begin
	if not exists(select 1 from studio_outputs where root_agent_run_id=new.id) then
		return new;
	elsif new.status='completed' then
		if not exists(select 1 from studio_outputs where root_agent_run_id=new.id and artifact is not null and result_id is not null) then
			raise exception 'Studio Output must publish artifact before completing Agent Run';
		end if;
		update studio_outputs set status='completed',error_code=null,started_at=new.started_at,
			finished_at=new.finished_at,updated_at=new.updated_at where root_agent_run_id=new.id;
	else
		update studio_outputs set status=new.status,error_code=new.error_code,started_at=new.started_at,
			finished_at=new.finished_at,updated_at=new.updated_at where root_agent_run_id=new.id;
	end if;
	return new;
end
$$;

drop trigger if exists agent_runs_sync_studio_output on agent_runs;
create trigger agent_runs_sync_studio_output
	after update of status,error_code,started_at,finished_at,updated_at on agent_runs
	for each row execute function nano_sync_studio_output_projection();

create unique index if not exists agent_runs_one_research_child_idx
	on agent_runs(parent_run_id) where agent_role='research';

drop table if exists agent_run_routes;

create table if not exists agent_research_delegations (
	parent_run_id text primary key references agent_runs(id) on delete cascade,
	child_run_id text not null unique references agent_runs(id) on delete cascade,
	state text not null check (state in ('waiting','ready','failed','consumed')),
	discovery_session_id text references source_discovery_sessions(id) on delete set null,
	expanded_queries jsonb not null default '[]'::jsonb check (jsonb_typeof(expanded_queries)='array'),
	error_code text check (char_length(error_code) between 1 and 64),
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now(),
	completed_at timestamptz,
	constraint agent_research_delegations_completion_check check (
		(state='waiting' and discovery_session_id is null and error_code is null and completed_at is null)
		or (state='ready' and discovery_session_id is not null and error_code is null and completed_at is not null)
		or (state='failed' and error_code is not null and completed_at is not null)
		or (state='consumed' and completed_at is not null)
	)
);

create table if not exists agent_run_delegations (
	id text primary key check (char_length(id) between 3 and 255),
	parent_run_id text not null unique references agent_runs(id) on delete cascade,
	child_run_id text not null unique references agent_runs(id) on delete cascade,
	parent_role text not null check (parent_role='leader'),
	child_role text not null check (child_role='research'),
	child_ordinal integer not null check (child_ordinal=0),
	depth integer not null check (depth=1),
	state text not null check (state in ('waiting','succeeded','failed','cancelled')),
	error_code text check (char_length(error_code) between 1 and 64),
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now(),
	completed_at timestamptz,
	consumed_at timestamptz,
	constraint agent_run_delegations_terminal_shape check (
		(state='waiting' and error_code is null and completed_at is null and consumed_at is null)
		or (state='succeeded' and error_code is null and completed_at is not null)
		or (state in ('failed','cancelled') and error_code is not null and completed_at is not null)
	),
	unique(parent_run_id,child_role,child_ordinal)
);

alter table agent_run_delegations add column if not exists action_id text;
alter table agent_run_delegations add column if not exists result_id text references agent_run_results(id) on delete restrict;
alter table agent_run_delegations alter column parent_role drop not null;
alter table agent_run_delegations alter column child_role drop not null;
create unique index if not exists agent_run_delegations_parent_action_idx
	on agent_run_delegations(parent_run_id,action_id) where action_id is not null;

create table if not exists agent_research_outcomes (
	delegation_id text primary key references agent_run_delegations(id) on delete cascade,
	discovery_session_id text not null unique references source_discovery_sessions(id) on delete restrict,
	created_at timestamptz not null default now()
);

create table if not exists agent_role_checkpoints (
	run_id text not null references agent_runs(id) on delete cascade,
	agent_role text not null check (agent_role='research'),
	step_key text not null check (step_key in ('query_plan','search_result')),
	ordinal integer not null check (ordinal between 0 and 2),
	identity_key text not null check (char_length(identity_key) between 3 and 200),
	payload jsonb not null check (jsonb_typeof(payload)='object' and octet_length(payload::text) <= 262144),
	payload_sha256 text not null check (payload_sha256 ~ '^[0-9a-f]{64}$'),
	created_at timestamptz not null default now(),
	primary key (run_id,agent_role,step_key,ordinal),
	unique(run_id,identity_key),
	constraint agent_role_checkpoints_shape check (
		(step_key='query_plan' and ordinal=0)
		or (step_key='search_result' and ordinal between 0 and 2)
	)
);

insert into agent_run_delegations(
	id,parent_run_id,child_run_id,parent_role,child_role,child_ordinal,depth,state,error_code,
	created_at,updated_at,completed_at,consumed_at
)
select 'dlg_legacy_' || md5(legacy.parent_run_id || ':' || legacy.child_run_id),
	legacy.parent_run_id,legacy.child_run_id,'leader','research',0,1,
	case
		when child.status='completed' then 'succeeded'
		when child.status='cancelled' then 'cancelled'
		when child.status='failed' then 'failed'
		else 'waiting'
	end,
	case when child.status in ('failed','cancelled') then coalesce(child.error_code,legacy.error_code,'research_failed') end,
	legacy.created_at,legacy.updated_at,
	case when child.status in ('completed','failed','cancelled') then coalesce(legacy.completed_at,child.finished_at,legacy.updated_at) end,
	case when legacy.state='consumed' then legacy.updated_at end
from agent_research_delegations legacy
join agent_runs child on child.id=legacy.child_run_id
on conflict(parent_run_id) do nothing;

insert into agent_research_outcomes(delegation_id,discovery_session_id,created_at)
select delegation.id,legacy.discovery_session_id,coalesce(legacy.completed_at,legacy.updated_at)
from agent_research_delegations legacy
join agent_run_delegations delegation on delegation.parent_run_id=legacy.parent_run_id
where legacy.discovery_session_id is not null and delegation.state='succeeded'
on conflict(delegation_id) do nothing;

insert into agent_trees(
	id,root_agent_run_id,absolute_deadline,model_call_limit,action_limit,context_byte_limit,result_byte_limit,created_at,updated_at
)
select 'tree_' || root.id,root.id,root.deadline_at,
	greatest(1,coalesce(root.action_decision_limit,4)+coalesce(root.final_decision_limit,1)),
	greatest(1,coalesce(root.action_limit,8)),65536,greatest(1,coalesce(root.action_results_byte_limit,65536)),
	root.created_at,root.updated_at
from agent_runs root
where root.runtime_kind='legacy_role' and root.agent_role='leader'
on conflict(id) do nothing;

update agent_runs run
set tree_id='tree_' || run.id
where run.runtime_kind='legacy_role' and run.agent_role='leader' and run.tree_id is null;

update agent_runs child
set tree_id=parent.tree_id
from agent_run_delegations delegation
join agent_runs parent on parent.id=delegation.parent_run_id
where child.id=delegation.child_run_id and child.tree_id is null;

insert into chat_runs(
	id,user_id,chat_id,input_message_id,output_message_id,root_agent_run_id,status,error_code,
	created_at,started_at,finished_at,updated_at
)
select root.id,root.user_id,root.chat_id,root.input_message_id,root.output_message_id,root.id,root.status,root.error_code,
	root.created_at,root.started_at,root.finished_at,root.updated_at
from agent_runs root
where root.runtime_kind='legacy_role' and root.agent_role='leader'
on conflict(id) do nothing;

insert into agent_role_checkpoints(
	run_id,agent_role,step_key,ordinal,identity_key,payload,payload_sha256,created_at
)
select legacy.child_run_id,'research','query_plan',0,'research/query_plan/0',payload.document,
	encode(sha256(convert_to(payload.document::text,'UTF8')),'hex'),legacy.updated_at
from agent_research_delegations legacy
cross join lateral (select jsonb_build_object('queries',legacy.expanded_queries) as document) payload
where jsonb_array_length(legacy.expanded_queries)>0
on conflict(run_id,agent_role,step_key,ordinal) do nothing;

drop table agent_research_delegations;
drop index if exists agent_runs_one_research_child_idx;
alter table agent_runs drop constraint if exists agent_runs_role_shape_check;
alter table agent_runs add constraint agent_runs_role_shape_check check (
	(agent_role='leader')
	or (agent_role='research' and output_message_id is null)
);
alter table agent_runs drop column parent_run_id;

create unique index if not exists source_discovery_sessions_research_run_idx
	on source_discovery_sessions(research_run_id) where research_run_id is not null;

alter table source_discovery_sessions add column if not exists agent_run_id text references agent_runs(id) on delete cascade;
alter table source_discovery_sessions add column if not exists action_id text;
alter table source_discovery_sessions drop constraint if exists source_discovery_sessions_origin_check;
alter table source_discovery_sessions add constraint source_discovery_sessions_origin_check
	check (origin in ('manual','research_agent','chat_agent'));
alter table source_discovery_sessions drop constraint if exists source_discovery_sessions_chat_action_shape_check;
alter table source_discovery_sessions add constraint source_discovery_sessions_chat_action_shape_check check (
	(origin='chat_agent' and agent_run_id is not null and action_id is not null and research_run_id is null)
	or (origin<>'chat_agent' and agent_run_id is null and action_id is null)
);
create unique index if not exists source_discovery_sessions_chat_action_idx
	on source_discovery_sessions(agent_run_id,action_id) where agent_run_id is not null;

-- A Run pins the exact authoritative Evidence and Retrieval projection that
-- existed at admission. Source identities intentionally are not foreign keys:
-- deletion must invalidate publication without erasing what the Run selected.
create table if not exists agent_run_evidence_set (
	run_id text not null references agent_runs(id) on delete cascade,
	ordinal integer not null check (ordinal between 0 and 49),
	notebook_id text not null check (char_length(notebook_id) between 1 and 255),
	source_id text not null check (char_length(source_id) between 1 and 255),
	evidence_revision_id text not null check (char_length(evidence_revision_id) between 1 and 255),
	index_version_id text not null check (char_length(index_version_id) between 1 and 255),
	created_at timestamptz not null default now(),
	primary key (run_id, ordinal),
	unique (run_id, source_id)
);

create index if not exists agent_run_evidence_set_scope_idx
	on agent_run_evidence_set(notebook_id, source_id, evidence_revision_id, index_version_id);

create table if not exists agent_run_grounding_plans (
	run_id text primary key references agent_runs(id) on delete cascade,
	draft_sha256 text not null check (draft_sha256 ~ '^[0-9a-f]{64}$'),
	outcome text not null check (outcome in ('source_less','source_unsearched','source_free','source_cited','supported','insufficient_evidence','zero_support')),
	research_performed boolean not null default false,
	research_complete boolean not null,
	retrieval_degraded boolean not null,
	verifier_model text not null default '',
	verifier_prompt_version text not null default '',
	created_at timestamptz not null default now(),
	constraint agent_run_grounding_plans_shape_check check (
		(outcome='source_less' and research_performed=false and research_complete=false and retrieval_degraded=false and verifier_model='' and verifier_prompt_version='')
		or (outcome='source_unsearched' and research_performed=false and research_complete=false and retrieval_degraded=false and verifier_model='' and verifier_prompt_version='')
		or (outcome='source_free' and verifier_model='' and verifier_prompt_version='')
		or (outcome='source_cited' and research_performed=true and verifier_model='' and verifier_prompt_version='')
		or (outcome='supported' and verifier_model<>'' and verifier_prompt_version<>'')
		or (outcome='insufficient_evidence' and verifier_model<>'' and verifier_prompt_version<>'')
		or (outcome='zero_support' and research_complete=true and retrieval_degraded=false and verifier_model='' and verifier_prompt_version='')
	)
);

alter table agent_run_grounding_plans add column if not exists research_performed boolean not null default false;
alter table agent_run_grounding_plans alter column verifier_model set default '';
alter table agent_run_grounding_plans alter column verifier_prompt_version set default '';
alter table agent_run_grounding_plans drop constraint if exists agent_run_grounding_plans_outcome_check;
alter table agent_run_grounding_plans add constraint agent_run_grounding_plans_outcome_check
	check (outcome in ('source_less','source_unsearched','source_free','source_cited','supported','insufficient_evidence','zero_support'));
alter table agent_run_grounding_plans drop constraint if exists agent_run_grounding_plans_shape_check;
alter table agent_run_grounding_plans add constraint agent_run_grounding_plans_shape_check check (
	(outcome='source_less' and research_performed=false and research_complete=false and retrieval_degraded=false and verifier_model='' and verifier_prompt_version='')
	or (outcome='source_unsearched' and research_performed=false and research_complete=false and retrieval_degraded=false and verifier_model='' and verifier_prompt_version='')
	or (outcome='source_free' and verifier_model='' and verifier_prompt_version='')
	or (outcome='source_cited' and research_performed=true and verifier_model='' and verifier_prompt_version='')
	or (outcome='supported' and verifier_model<>'' and verifier_prompt_version<>'')
	or (outcome='insufficient_evidence' and verifier_model<>'' and verifier_prompt_version<>'')
	or (outcome='zero_support' and research_complete=true and retrieval_degraded=false and verifier_model='' and verifier_prompt_version='')
);

create table if not exists agent_claim_support_records (
	run_id text not null references agent_run_grounding_plans(run_id) on delete cascade,
	claim_ordinal integer not null check (claim_ordinal between 0 and 63),
	claim_text text not null check (char_length(claim_text) between 1 and 4000),
	verdict text not null check (verdict='supported'),
	primary key (run_id, claim_ordinal)
);

create table if not exists agent_draft_citations (
	run_id text not null,
	claim_ordinal integer not null,
	citation_ordinal integer not null check (citation_ordinal between 0 and 7),
	citation_id text not null unique check (char_length(citation_id) between 1 and 80),
	notebook_id text not null,
	source_id text not null,
	evidence_revision_id text not null,
	unit_id text not null,
	start_rune integer not null check (start_rune >= 0),
	end_rune integer not null check (end_rune > start_rune),
	primary key (run_id, claim_ordinal, citation_ordinal),
	foreign key (run_id, claim_ordinal) references agent_claim_support_records(run_id, claim_ordinal) on delete cascade
);

create table if not exists agent_draft_source_references (
	run_id text not null references agent_run_grounding_plans(run_id) on delete cascade,
	reference_ordinal integer not null check (reference_ordinal between 0 and 63),
	citation_id text not null unique check (char_length(citation_id) between 1 and 80),
	notebook_id text not null check (char_length(notebook_id) between 1 and 255),
	source_id text not null check (char_length(source_id) between 1 and 255),
	created_at timestamptz not null default now(),
	primary key (run_id, reference_ordinal),
	unique (run_id, source_id)
);

create table if not exists chat_citations (
	message_id text not null references chat_messages(id) on delete cascade,
	citation_id text not null,
	run_id text not null references agent_runs(id) on delete cascade,
	claim_ordinal integer not null,
	citation_ordinal integer not null,
	claim_text text not null,
	notebook_id text not null,
	source_id text not null,
	evidence_revision_id text not null,
	unit_id text not null,
	start_rune integer not null,
	end_rune integer not null,
	created_at timestamptz not null default now(),
	primary key (message_id, citation_id),
	unique (run_id, claim_ordinal, citation_ordinal)
);

alter table chat_citations add column if not exists reference_kind text not null default 'precise';
alter table chat_citations add column if not exists reference_ordinal integer;
alter table chat_citations alter column claim_ordinal drop not null;
alter table chat_citations alter column citation_ordinal drop not null;
alter table chat_citations alter column claim_text drop not null;
alter table chat_citations alter column evidence_revision_id drop not null;
alter table chat_citations alter column unit_id drop not null;
alter table chat_citations alter column start_rune drop not null;
alter table chat_citations alter column end_rune drop not null;
alter table chat_citations drop constraint if exists chat_citations_reference_kind_check;
alter table chat_citations add constraint chat_citations_reference_kind_check check (
	(reference_kind='precise' and reference_ordinal is null and claim_ordinal is not null and citation_ordinal is not null
		and claim_text is not null and evidence_revision_id is not null and unit_id is not null and start_rune is not null and end_rune is not null)
	or (reference_kind='source' and reference_ordinal between 0 and 63 and claim_ordinal is null and citation_ordinal is null
		and claim_text is null and evidence_revision_id is null and unit_id is null and start_rune is null and end_rune is null)
);
create unique index if not exists chat_citations_source_reference_idx
	on chat_citations(run_id, reference_ordinal) where reference_kind='source';

drop index if exists agent_runs_one_active_per_user_idx;
create unique index agent_runs_one_active_per_user_idx
	on agent_runs(user_id)
	where agent_role='leader' and status in ('queued', 'running');

drop index if exists agent_runs_one_active_per_input_idx;
create unique index agent_runs_one_active_per_input_idx
	on agent_runs(input_message_id)
	where agent_role='leader' and status in ('queued', 'running');

drop index if exists agent_runs_one_completed_per_input_idx;
create unique index agent_runs_one_completed_per_input_idx
	on agent_runs(input_message_id)
	where agent_role='leader' and status = 'completed';

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

create table if not exists agent_context_compactions (
	id text primary key check (char_length(id) between 3 and 255),
	chat_id text not null references chat_chats(id) on delete cascade,
	predecessor_id text references agent_context_compactions(id) on delete restrict,
	idempotency_key text not null unique check (char_length(idempotency_key) between 32 and 128),
	summarized_through text not null check (char_length(summarized_through) between 3 and 500),
	suffix_start text not null check (char_length(suffix_start) between 3 and 500),
	summary_text text not null check (char_length(summary_text) > 0),
	schema_version integer not null check (schema_version > 0),
	summarizer_model text not null check (char_length(summarizer_model) between 1 and 255),
	prompt_version text not null check (char_length(prompt_version) between 1 and 255),
	provider_capability_identity text not null,
	provider_capability_version integer not null,
	provider_capability_sha256 text not null check (provider_capability_sha256 ~ '^[0-9a-f]{64}$'),
	model_context_policy_identity text not null,
	model_context_policy_version integer not null,
	model_context_policy_sha256 text not null check (model_context_policy_sha256 ~ '^[0-9a-f]{64}$'),
	tokenizer_identity text not null,
	tokenizer_version text not null,
	hard_input_tokens integer not null check (hard_input_tokens > 0),
	safe_input_tokens integer not null check (safe_input_tokens > 0),
	soft_trigger_tokens integer not null check (soft_trigger_tokens > 0),
	keep_recent_tokens integer not null check (keep_recent_tokens > 0),
	summary_max_output_tokens integer not null check (summary_max_output_tokens > 0),
	before_tokens integer not null check (before_tokens > 0),
	after_tokens integer not null check (after_tokens > 0),
	trigger_reason text not null check (trigger_reason in ('threshold','provider_overflow')),
	created_at timestamptz not null default now(),
	foreign key (provider_capability_identity,provider_capability_version)
		references provider_model_capability_versions(capability_identity,capability_version),
	foreign key (model_context_policy_identity,model_context_policy_version)
		references agent_model_context_policy_versions(context_policy_identity,context_policy_version)
);

create index if not exists agent_context_compactions_chat_latest_idx
	on agent_context_compactions(chat_id,created_at desc,id desc);

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


drop function if exists nano_advance_agent_trace_ref(text, integer, text, text);
drop function if exists nano_owned_run_trace_state(text);
drop function if exists nano_owned_trace_span(text, text);
drop table if exists agentobs_replay_staging cascade;
drop table if exists agentobs_outbox_records cascade;
drop table if exists agentobs_outbox_capacity cascade;
drop function if exists validate_agentobs_outbox_record();
drop function if exists reserve_agentobs_outbox_record_capacity();
drop function if exists release_agentobs_outbox_record_capacity();
drop function if exists reserve_agentobs_replay_staging_capacity();
drop function if exists release_agentobs_replay_staging_capacity();

create table if not exists agent_trace_refs (
	trace_id text primary key check (char_length(trace_id) between 1 and 160),
	run_id text not null unique references agent_runs(id) on delete cascade,
	chat_id text not null references chat_chats(id) on delete cascade,
	notebook_id text not null references notebook_notebooks(id) on delete cascade,
	root_span_id text not null unique check (char_length(root_span_id) between 1 and 160),
	agent_name text not null check (char_length(agent_name) between 1 and 160),
	schema_version integer not null check (schema_version >= 1),
	semantic_convention_version integer not null check (semantic_convention_version >= 1),
	created_at timestamptz not null default now()
);

alter table agent_trace_refs alter column chat_id drop not null;

alter table agent_trace_refs
	drop column if exists next_sequence cascade,
	drop column if exists collector_cursor cascade,
	drop column if exists terminal_sequence cascade,
	drop column if exists delivery_state cascade,
	drop column if exists lease_token cascade,
	drop column if exists lease_expires_at cascade,
	drop column if exists next_attempt_at cascade,
	drop column if exists attempt_count cascade,
	drop column if exists last_error_code cascade,
	drop column if exists quarantined_at cascade,
	drop column if exists updated_at cascade;

create table if not exists agentobs_outbox_commands (
	command_id text primary key check (char_length(command_id) between 1 and 200),
	command_version integer not null check (command_version = 1),
	command_kind text not null check (command_kind = 'purge_trace'),
	trace_id text not null unique check (char_length(trace_id) between 1 and 160),
	run_id text not null check (char_length(run_id) between 1 and 160),
	requested_at timestamptz not null,
	requested_at_unix_nano bigint not null,
	delivery_state text not null default 'ready' constraint agentobs_outbox_commands_delivery_state_check
		check (delivery_state in ('ready', 'leased', 'published', 'acknowledged', 'quarantined')),
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

alter table agentobs_outbox_commands
	drop constraint if exists agentobs_outbox_commands_delivery_state_check;
alter table agentobs_outbox_commands
	add constraint agentobs_outbox_commands_delivery_state_check
	check (delivery_state in ('ready', 'leased', 'published', 'acknowledged', 'quarantined'));

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
	status text not null check (status in ('queued', 'running', 'waiting', 'succeeded', 'failed', 'cancelled')),
	attempt_no integer not null default 0,
	lease_token uuid,
	lease_expires_at timestamptz,
	available_at timestamptz not null default now(),
	last_error_code text check (last_error_code is null or last_error_code ~ '^[a-z][a-z0-9_]{1,63}$'),
	created_at timestamptz not null default now(),
	started_at timestamptz,
	finished_at timestamptz,
	updated_at timestamptz not null default now(),
	constraint agent_jobs_execution_state_check check (
		(status = 'queued' and attempt_no between 0 and 10 and lease_token is null and lease_expires_at is null)
		or (status = 'running' and attempt_no between 1 and 10 and lease_token is not null and lease_expires_at is not null)
		or (status = 'waiting' and attempt_no between 1 and 10 and lease_token is null and lease_expires_at is null)
		or (status in ('succeeded', 'failed', 'cancelled') and attempt_no between 0 and 10 and lease_token is null and lease_expires_at is null)
	)
);

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
alter table agent_jobs add column if not exists available_at timestamptz not null default now();
alter table agent_jobs add column if not exists last_error_code text;
alter table agent_jobs drop constraint if exists agent_jobs_last_error_code_check;
alter table agent_jobs add constraint agent_jobs_last_error_code_check
	check (last_error_code is null or last_error_code ~ '^[a-z][a-z0-9_]{1,63}$');
drop index if exists agent_jobs_queued_idx;
create index agent_jobs_queued_idx on agent_jobs(available_at,created_at,id) where status='queued';
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
	check (status in ('queued', 'running', 'waiting', 'succeeded', 'failed', 'cancelled'));
alter table agent_jobs add constraint agent_jobs_execution_state_check check (
	(status = 'queued' and attempt_no between 0 and 10 and lease_token is null and lease_expires_at is null)
	or (status = 'running' and attempt_no between 1 and 10 and lease_token is not null and lease_expires_at is not null)
	or (status = 'waiting' and attempt_no between 1 and 10 and lease_token is null and lease_expires_at is null)
	or (status in ('succeeded', 'failed', 'cancelled') and attempt_no between 0 and 10 and lease_token is null and lease_expires_at is null)
);

-- Contract migration is allowed only after this database-owned projection
-- reports ready_to_contract. Retained terminal history does not block drain,
-- while an inconsistent active Job still fails closed even if its Run was
-- prematurely terminalized.
create or replace view agent_legacy_runtime_drain_status as
with legacy_runs as (
	select
		run.status in ('queued','running') or exists (
			select 1 from agent_jobs job
			where job.run_id=run.id and job.status in ('queued','running','waiting')
		) as active
	from agent_runs run
	where run.runtime_kind='legacy_role'
)
select
	count(*) filter (where active) as active_legacy_runs,
	count(*) as retained_legacy_runs,
	count(*) filter (where active)=0 as ready_to_contract
from legacy_runs;

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
alter table platform_mail_outbox enable row level security;
alter table notebook_notebooks enable row level security;
alter table notebook_memberships enable row level security;
alter table notebook_invitations enable row level security;
alter table source_sources enable row level security;
alter table source_upload_intents enable row level security;
alter table source_url_admissions enable row level security;
alter table source_processing_jobs enable row level security;
alter table source_purge_jobs enable row level security;
alter table source_evidence_revisions enable row level security;
alter table source_admission_reports enable row level security;
alter table source_admission_reviews enable row level security;
alter table source_evidence_coverage enable row level security;
alter table source_evidence_coverage_gaps enable row level security;
alter table source_evidence_units enable row level security;
alter table source_viewer_artifacts enable row level security;
alter table source_maps enable row level security;
alter table retrieval_index_versions enable row level security;
alter table retrieval_eval_runs enable row level security;
alter table retrieval_source_index_builds enable row level security;
alter table platform_idempotency_keys enable row level security;
alter table chat_chats enable row level security;
alter table chat_source_selections enable row level security;
alter table chat_messages enable row level security;
alter table source_discovery_sessions enable row level security;
alter table source_discovery_candidates enable row level security;
alter table source_discovery_jobs enable row level security;
alter table agent_runs enable row level security;
alter table chat_runs enable row level security;
alter table research_sessions enable row level security;
alter table research_plan_versions enable row level security;
alter table research_report_versions enable row level security;
alter table research_evidence_ledger enable row level security;
alter table research_source_imports enable row level security;
alter table research_step_capsules enable row level security;
alter table research_rollups enable row level security;
alter table research_archival_capsules enable row level security;
alter table research_task_memories enable row level security;
alter table research_compaction_failures enable row level security;
alter table agent_trees enable row level security;
alter table agent_run_results enable row level security;
alter table studio_outputs enable row level security;
alter table agent_run_delegations enable row level security;
alter table agent_research_outcomes enable row level security;
alter table agent_role_checkpoints enable row level security;
alter table agent_run_evidence_set enable row level security;
alter table agent_run_grounding_plans enable row level security;
alter table agent_claim_support_records enable row level security;
alter table agent_draft_citations enable row level security;
alter table agent_draft_source_references enable row level security;
alter table chat_citations enable row level security;
alter table agent_run_checkpoints enable row level security;
alter table agent_context_compactions enable row level security;
alter table agent_traces enable row level security;
alter table agent_trace_records enable row level security;
alter table agent_trace_refs enable row level security;
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
	platform_mail_outbox,
	notebook_notebooks,
	notebook_memberships,
	notebook_invitations,
	source_sources,
	source_upload_intents,
	source_url_admissions,
	source_processing_jobs,
	source_purge_jobs,
	source_evidence_revisions,
	source_evidence_coverage,
	source_evidence_coverage_gaps,
	source_evidence_units,
	source_viewer_artifacts,
	retrieval_index_versions,
	retrieval_eval_runs,
	platform_idempotency_keys,
	chat_chats,
	chat_source_selections,
	chat_messages,
	agent_runs,
	chat_runs,
	research_sessions,
	research_plan_versions,
	research_report_versions,
	research_evidence_ledger,
	research_step_capsules,
	research_rollups,
	studio_outputs,
	agent_trees,
	agent_run_evidence_set,
	chat_citations,
	agent_jobs
to nano_app;
grant select on agent_context_compactions to nano_app;
grant select on source_maps to nano_app;
grant select, insert, update, delete on source_discovery_sessions, source_discovery_candidates, source_discovery_jobs to nano_app;
grant select(parent_run_id,child_run_id,state,completed_at,consumed_at,error_code) on agent_run_delegations to nano_app;
grant update(state,error_code,completed_at,updated_at) on agent_run_delegations to nano_app;
grant select on retrieval_source_index_builds to nano_app;
grant select on source_admission_reports to nano_app;
grant select, insert on source_admission_reviews to nano_app;
revoke update, delete on platform_capability_grants, platform_replay_access_audit from nano_app;
revoke insert on platform_capability_grants from nano_app;
revoke select on platform_replay_access_audit from nano_app;
grant select on
	identity_users,
	identity_sessions,
	notebook_notebooks,
	notebook_memberships,
	chat_chats,
	chat_source_selections,
	chat_messages,
	agent_runs,
	chat_runs,
	research_sessions,
	research_plan_versions,
	research_report_versions,
	research_evidence_ledger,
	research_step_capsules,
	research_rollups,
	studio_outputs,
	agent_trees,
	agent_run_evidence_set,
	agent_run_grounding_plans,
	agent_claim_support_records,
	agent_draft_citations,
	agent_draft_source_references,
	chat_citations
to nano_worker;
grant insert on agent_run_evidence_set to nano_worker;
grant select, insert on agent_run_results to nano_worker;
grant insert, update on chat_runs,agent_trees to nano_worker;
grant select, insert, update on research_sessions,research_evidence_ledger to nano_worker;
grant select, insert, update, delete on research_source_imports to nano_worker;
grant select, insert on research_plan_versions,research_report_versions,research_step_capsules,research_rollups to nano_worker;
grant select, insert on research_archival_capsules,research_task_memories,research_compaction_failures to nano_worker;
grant select, insert, update on studio_outputs to nano_worker;
grant select, insert, update, delete on
	agent_run_grounding_plans,
	agent_claim_support_records,
	agent_draft_citations,
	agent_draft_source_references,
	chat_citations
to nano_worker;
grant select, insert, update, delete on source_sources to nano_worker;
grant select, update on source_upload_intents to nano_worker;
grant select, insert, update, delete on source_processing_jobs to nano_worker;
grant select, insert, update, delete on source_purge_jobs to nano_worker;
grant select, insert, update, delete on source_evidence_revisions, source_evidence_coverage,
	source_evidence_coverage_gaps, source_evidence_units, source_viewer_artifacts to nano_worker;
grant select, insert on source_admission_reports to nano_worker;
grant select, insert on source_maps to nano_worker;
grant select on source_admission_reviews to nano_worker;
grant select, insert, update, delete on retrieval_index_versions, retrieval_eval_runs to nano_worker;
grant select, insert, update, delete on retrieval_source_index_builds to nano_worker;
grant select, insert, update, delete on agent_jobs to nano_worker;
grant select, insert, update, delete on source_discovery_sessions, source_discovery_candidates, source_discovery_jobs to nano_worker;
grant select, insert, update, delete on agent_run_delegations, agent_research_outcomes to nano_worker;
grant select, insert on agent_role_checkpoints to nano_worker;
grant insert, update on chat_messages, chat_chats, agent_runs to nano_worker;
grant select, insert, update, delete on chat_source_selections to nano_worker;
revoke all on agent_run_checkpoints from nano_app, nano_worker;
grant select, insert on agent_run_checkpoints to nano_worker;
grant select, insert on agent_context_compactions to nano_worker;
revoke all on agent_traces, agent_trace_records from nano_app, nano_worker;
grant insert on agent_traces, agent_trace_records to nano_app;
grant select, insert on agent_traces, agent_trace_records to nano_worker;
revoke all on agent_trace_refs from nano_app, nano_worker;
grant insert on agent_trace_refs to nano_app;
grant select, insert, update, delete on agent_trace_refs to nano_worker;
revoke all on agentobs_outbox_commands, agentobs_outbox_command_objects from nano_app, nano_worker;
grant select, insert, update, delete on agentobs_outbox_commands, agentobs_outbox_command_objects to nano_worker;
grant select, insert, update, delete on platform_mail_outbox to nano_worker;

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

drop policy if exists platform_mail_outbox_actor on platform_mail_outbox;
create policy platform_mail_outbox_actor on platform_mail_outbox
	for all to nano_app
	using (actor_user_id = nullif(current_setting('app.principal_id', true), ''))
	with check (actor_user_id = nullif(current_setting('app.principal_id', true), ''));

drop policy if exists platform_mail_outbox_worker on platform_mail_outbox;
create policy platform_mail_outbox_worker on platform_mail_outbox
	for all to nano_worker using (true) with check (true);

create or replace function nano_has_notebook_capability(candidate_notebook_id text, candidate_capability text)
returns boolean
language sql
stable
security definer
set search_path = pg_catalog, public
as $$
	select exists (
		select 1 from public.notebook_memberships m
		where m.notebook_id = candidate_notebook_id
		  and m.user_id = nullif(current_setting('app.principal_id', true), '')
		  and case candidate_capability
			when 'notebook.read' then m.role in ('viewer', 'editor', 'owner')
			when 'source.read' then m.role in ('viewer', 'editor', 'owner')
			when 'source.maintain' then m.role in ('editor', 'owner')
			when 'notebook.manage' then m.role = 'owner'
			else false
		  end
	)
$$;
revoke all on function nano_has_notebook_capability(text, text) from public;
grant execute on function nano_has_notebook_capability(text, text) to nano_app;

create or replace function nano_membership_insert_allowed(candidate_notebook_id text, candidate_user_id text, candidate_role text)
returns boolean
language sql
stable
security definer
set search_path = pg_catalog, public
as $$
	select candidate_user_id = nullif(current_setting('app.principal_id', true), '') and (
		(candidate_role = 'owner' and not exists (
			select 1 from public.notebook_memberships m where m.notebook_id=candidate_notebook_id
		))
		or (candidate_role in ('viewer','editor') and exists (
			select 1
			from public.notebook_invitations i
			join public.identity_users u on u.id=candidate_user_id
			where i.notebook_id=candidate_notebook_id and i.canonical_email=u.canonical_email
			  and i.role=candidate_role and i.state='pending' and i.expires_at > now()
		))
	)
$$;
revoke all on function nano_membership_insert_allowed(text, text, text) from public;
grant execute on function nano_membership_insert_allowed(text, text, text) to nano_app;

create or replace function nano_transfer_notebook_ownership(candidate_notebook_id text, actor_user_id text, target_user_id text)
returns boolean
language plpgsql
security definer
set search_path = pg_catalog, public
as $$
begin
	if actor_user_id <> nullif(current_setting('app.principal_id', true), '')
		or not exists(select 1 from public.notebook_memberships where notebook_id=candidate_notebook_id and user_id=actor_user_id and role='owner')
		or not exists(select 1 from public.notebook_memberships where notebook_id=candidate_notebook_id and user_id=target_user_id and role in ('viewer','editor')) then
		return false;
	end if;
	update public.notebook_memberships set role='editor' where notebook_id=candidate_notebook_id and user_id=actor_user_id;
	update public.notebook_memberships set role='owner' where notebook_id=candidate_notebook_id and user_id=target_user_id;
	return true;
end
$$;
revoke all on function nano_transfer_notebook_ownership(text, text, text) from public;
grant execute on function nano_transfer_notebook_ownership(text, text, text) to nano_app;

create or replace function nano_depart_notebook_member(candidate_notebook_id text, actor_user_id text, target_user_id text, owner_action boolean)
returns boolean
language plpgsql
security definer
set search_path = pg_catalog, public
as $$
declare
	allowed boolean;
begin
	select case when owner_action then
		exists(select 1 from public.notebook_memberships where notebook_id=candidate_notebook_id and user_id=actor_user_id and role='owner')
		and exists(select 1 from public.notebook_memberships where notebook_id=candidate_notebook_id and user_id=target_user_id and role <> 'owner')
	else
		actor_user_id=target_user_id and exists(select 1 from public.notebook_memberships where notebook_id=candidate_notebook_id and user_id=target_user_id and role <> 'owner')
	end into allowed;
	if not allowed or actor_user_id <> nullif(current_setting('app.principal_id', true), '') then
		return false;
	end if;

	update public.agent_jobs j set status='cancelled', lease_token=null, lease_expires_at=null,
		finished_at=now(), updated_at=now()
	from public.agent_runs r join public.chat_chats c on c.id=r.chat_id
	where j.run_id=r.id and c.notebook_id=candidate_notebook_id and c.creator_user_id=target_user_id
	  and j.status in ('queued','running');
	update public.agent_runs r set status='cancelled', error_code='membership_revoked', finished_at=now(), updated_at=now()
	from public.chat_chats c
	where r.chat_id=c.id and c.notebook_id=candidate_notebook_id and c.creator_user_id=target_user_id
	  and r.status in ('queued','running');
	delete from public.chat_chats where notebook_id=candidate_notebook_id and creator_user_id=target_user_id;
	delete from public.notebook_memberships where notebook_id=candidate_notebook_id and user_id=target_user_id and role <> 'owner';
	return found;
end
$$;
revoke all on function nano_depart_notebook_member(text, text, text, boolean) from public;
grant execute on function nano_depart_notebook_member(text, text, text, boolean) to nano_app;

create or replace function nano_notebook_member_directory(candidate_notebook_id text)
returns table(user_id text, canonical_email text, display_email text, role text, created_at timestamptz)
language sql
stable
security definer
set search_path = pg_catalog, public
as $$
	select m.user_id, u.canonical_email, u.display_email, m.role, m.created_at
	from public.notebook_memberships m join public.identity_users u on u.id=m.user_id
	where m.notebook_id=candidate_notebook_id
	  and exists(select 1 from public.notebook_memberships actor
		where actor.notebook_id=candidate_notebook_id
		  and actor.user_id=nullif(current_setting('app.principal_id', true), '') and actor.role='owner')
	order by case m.role when 'owner' then 0 else 1 end, lower(u.display_email), m.user_id
$$;
revoke all on function nano_notebook_member_directory(text) from public;
grant execute on function nano_notebook_member_directory(text) to nano_app;

create or replace function nano_delete_notebook(candidate_notebook_id text, actor_user_id text)
returns boolean
language plpgsql
security definer
set search_path = pg_catalog, public
as $$
begin
	if actor_user_id <> nullif(current_setting('app.principal_id', true), '')
		or not exists(select 1 from public.notebook_memberships where notebook_id=candidate_notebook_id and user_id=actor_user_id and role='owner') then
		return false;
	end if;
	update public.agent_jobs j set status='cancelled',lease_token=null,lease_expires_at=null,finished_at=now(),updated_at=now()
	from public.agent_runs r join public.chat_chats c on c.id=r.chat_id
	where j.run_id=r.id and c.notebook_id=candidate_notebook_id and j.status in ('queued','running');
	update public.agent_runs r set status='cancelled',error_code='notebook_deleted',finished_at=now(),updated_at=now()
	from public.chat_chats c where r.chat_id=c.id and c.notebook_id=candidate_notebook_id and r.status in ('queued','running');
	delete from public.notebook_notebooks where id=candidate_notebook_id;
	return found;
end
$$;
revoke all on function nano_delete_notebook(text, text) from public;
grant execute on function nano_delete_notebook(text, text) to nano_app;

drop policy if exists notebook_memberships_owner on notebook_memberships;
drop policy if exists notebook_memberships_read on notebook_memberships;
create policy notebook_memberships_owner on notebook_memberships
	for select to nano_app
	using (
		user_id = nullif(current_setting('app.principal_id', true), '')
		or nano_has_notebook_capability(notebook_id, 'notebook.manage')
	);

drop policy if exists notebook_memberships_insert on notebook_memberships;
create policy notebook_memberships_insert on notebook_memberships
	for insert to nano_app
	with check (nano_membership_insert_allowed(notebook_id, user_id, role));

drop policy if exists notebook_memberships_update on notebook_memberships;
create policy notebook_memberships_update on notebook_memberships
	for update to nano_app
	using (nano_has_notebook_capability(notebook_id, 'notebook.manage'))
	with check (nano_has_notebook_capability(notebook_id, 'notebook.manage'));

drop policy if exists notebook_memberships_delete on notebook_memberships;
create policy notebook_memberships_delete on notebook_memberships
	for delete to nano_app
	using (
		(role <> 'owner' and user_id = nullif(current_setting('app.principal_id', true), ''))
		or (role <> 'owner' and nano_has_notebook_capability(notebook_id, 'notebook.manage'))
	);

drop policy if exists notebook_memberships_worker on notebook_memberships;
create policy notebook_memberships_worker on notebook_memberships
	for select to nano_worker
	using (true);

drop policy if exists notebook_invitations_owner_read on notebook_invitations;
create policy notebook_invitations_owner_read on notebook_invitations
	for select to nano_app
	using (
		exists (
			select 1 from notebook_memberships m
			where m.notebook_id = notebook_invitations.notebook_id
			  and m.user_id = nullif(current_setting('app.principal_id', true), '')
			  and m.role = 'owner'
		)
		or exists (
			select 1 from identity_users u
			where u.id = nullif(current_setting('app.principal_id', true), '')
			  and u.canonical_email = notebook_invitations.canonical_email
		)
	);

drop policy if exists notebook_invitations_owner_insert on notebook_invitations;
create policy notebook_invitations_owner_insert on notebook_invitations
	for insert to nano_app
	with check (exists (
		select 1 from notebook_memberships m
		where m.notebook_id = notebook_invitations.notebook_id
		  and m.user_id = nullif(current_setting('app.principal_id', true), '')
		  and m.role = 'owner'
	));

drop policy if exists notebook_invitations_participant_update on notebook_invitations;
drop policy if exists notebook_invitations_owner_update on notebook_invitations;
create policy notebook_invitations_owner_update on notebook_invitations
	for update to nano_app
	using (nano_has_notebook_capability(notebook_id, 'notebook.manage'))
	with check (nano_has_notebook_capability(notebook_id, 'notebook.manage'));

drop policy if exists notebook_invitations_recipient_accept on notebook_invitations;
create policy notebook_invitations_recipient_accept on notebook_invitations
	for update to nano_app
	using (
		state='pending' and expires_at > now() and exists (
			select 1 from identity_users u where u.id=nullif(current_setting('app.principal_id', true), '')
			  and u.canonical_email=notebook_invitations.canonical_email
		)
	)
	with check (
		state='accepted' and accepted_by_user_id=nullif(current_setting('app.principal_id', true), '')
		and accepted_at is not null and revoked_at is null
	);

drop policy if exists notebook_notebooks_owner on notebook_notebooks;
drop policy if exists notebook_notebooks_read on notebook_notebooks;
create policy notebook_notebooks_read on notebook_notebooks
	for select to nano_app
	using (exists (
		select 1 from notebook_memberships m
		where m.notebook_id = notebook_notebooks.id
		  and m.user_id = nullif(current_setting('app.principal_id', true), '')
	));

drop policy if exists notebook_notebooks_insert on notebook_notebooks;
create policy notebook_notebooks_insert on notebook_notebooks
	for insert to nano_app
	with check (true);

drop policy if exists notebook_notebooks_update on notebook_notebooks;
create policy notebook_notebooks_update on notebook_notebooks
	for update to nano_app
	using (exists (
		select 1 from notebook_memberships m
		where m.notebook_id = notebook_notebooks.id
		  and m.user_id = nullif(current_setting('app.principal_id', true), '')
		  and m.role = 'owner'
	))
	with check (exists (
		select 1 from notebook_memberships m
		where m.notebook_id = notebook_notebooks.id
		  and m.user_id = nullif(current_setting('app.principal_id', true), '')
		  and m.role = 'owner'
	));

drop policy if exists notebook_notebooks_delete on notebook_notebooks;
create policy notebook_notebooks_delete on notebook_notebooks
	for delete to nano_app
	using (exists (
		select 1 from notebook_memberships m
		where m.notebook_id = notebook_notebooks.id
		  and m.user_id = nullif(current_setting('app.principal_id', true), '')
		  and m.role = 'owner'
	));

drop policy if exists notebook_notebooks_worker on notebook_notebooks;
create policy notebook_notebooks_worker on notebook_notebooks
	for select to nano_worker
	using (true);

create or replace function nano_has_notebook_capability(candidate_notebook_id text, candidate_capability text)
returns boolean
language sql
stable
security definer
set search_path = pg_catalog, public
as $$
	select exists (
		select 1
		from public.notebook_memberships m
		where m.notebook_id = candidate_notebook_id
		  and m.user_id = nullif(current_setting('app.principal_id', true), '')
		  and case candidate_capability
			when 'notebook.read' then m.role in ('viewer', 'editor', 'owner')
			when 'source.read' then m.role in ('viewer', 'editor', 'owner')
			when 'source.maintain' then m.role in ('editor', 'owner')
			when 'notebook.manage' then m.role = 'owner'
			else false
		  end
	)
$$;
revoke all on function nano_has_notebook_capability(text, text) from public;
grant execute on function nano_has_notebook_capability(text, text) to nano_app;

drop policy if exists source_sources_app_read on source_sources;
create policy source_sources_app_read on source_sources
	for select to nano_app
	using (nano_has_notebook_capability(notebook_id, 'source.read'));

drop policy if exists source_sources_app_insert on source_sources;
create policy source_sources_app_insert on source_sources
	for insert to nano_app
	with check (nano_has_notebook_capability(notebook_id, 'source.maintain'));

drop policy if exists source_sources_app_update on source_sources;
create policy source_sources_app_update on source_sources
	for update to nano_app
	using (nano_has_notebook_capability(notebook_id, 'source.maintain'))
	with check (nano_has_notebook_capability(notebook_id, 'source.maintain'));

drop policy if exists source_sources_app_delete on source_sources;
create policy source_sources_app_delete on source_sources
	for delete to nano_app
	using (nano_has_notebook_capability(notebook_id, 'source.maintain'));

drop policy if exists source_sources_worker on source_sources;
create policy source_sources_worker on source_sources
	for select to nano_worker
	using (true);

drop policy if exists source_sources_worker_insert on source_sources;
create policy source_sources_worker_insert on source_sources
	for insert to nano_worker
	with check (true);

drop policy if exists source_sources_worker_update on source_sources;
create policy source_sources_worker_update on source_sources
	for update to nano_worker
	using (true)
	with check (true);

drop policy if exists source_sources_worker_delete on source_sources;
create policy source_sources_worker_delete on source_sources
	for delete to nano_worker
	using (true);

drop policy if exists source_upload_intents_app_read on source_upload_intents;
create policy source_upload_intents_app_read on source_upload_intents
	for select to nano_app
	using (
		created_by_user_id = nullif(current_setting('app.principal_id', true), '')
		and nano_has_notebook_capability(notebook_id, 'source.maintain')
	);

drop policy if exists source_upload_intents_app_insert on source_upload_intents;
create policy source_upload_intents_app_insert on source_upload_intents
	for insert to nano_app
	with check (
		created_by_user_id = nullif(current_setting('app.principal_id', true), '')
		and nano_has_notebook_capability(notebook_id, 'source.maintain')
	);

drop policy if exists source_upload_intents_app_update on source_upload_intents;
create policy source_upload_intents_app_update on source_upload_intents
	for update to nano_app
	using (
		created_by_user_id = nullif(current_setting('app.principal_id', true), '')
		and nano_has_notebook_capability(notebook_id, 'source.maintain')
	)
	with check (
		created_by_user_id = nullif(current_setting('app.principal_id', true), '')
		and nano_has_notebook_capability(notebook_id, 'source.maintain')
	);

drop policy if exists source_upload_intents_worker on source_upload_intents;
create policy source_upload_intents_worker on source_upload_intents
	for select to nano_worker
	using (true);

drop policy if exists source_upload_intents_worker_update on source_upload_intents;
create policy source_upload_intents_worker_update on source_upload_intents
	for update to nano_worker
	using (true)
	with check (true);

drop policy if exists source_url_admissions_app on source_url_admissions;
create policy source_url_admissions_app on source_url_admissions
	for all to nano_app
	using (
		created_by_user_id = nullif(current_setting('app.principal_id', true), '')
		and nano_has_notebook_capability(notebook_id, 'source.maintain')
	)
	with check (
		created_by_user_id = nullif(current_setting('app.principal_id', true), '')
		and nano_has_notebook_capability(notebook_id, 'source.maintain')
	);

drop policy if exists source_processing_jobs_app on source_processing_jobs;
create policy source_processing_jobs_app on source_processing_jobs
	for all to nano_app
	using (nano_has_notebook_capability(notebook_id, 'source.maintain'))
	with check (nano_has_notebook_capability(notebook_id, 'source.maintain'));

drop policy if exists source_processing_jobs_worker on source_processing_jobs;
create policy source_processing_jobs_worker on source_processing_jobs
	for all to nano_worker
	using (true)
	with check (true);

drop policy if exists source_purge_jobs_app on source_purge_jobs;
create policy source_purge_jobs_app on source_purge_jobs
	for all to nano_app
	using (
		created_by_user_id = nullif(current_setting('app.principal_id', true), '')
		and nano_has_notebook_capability(notebook_id, 'source.maintain')
	)
	with check (
		created_by_user_id = nullif(current_setting('app.principal_id', true), '')
		and nano_has_notebook_capability(notebook_id, 'source.maintain')
	);

drop policy if exists source_purge_jobs_worker on source_purge_jobs;
create policy source_purge_jobs_worker on source_purge_jobs
	for all to nano_worker
	using (true)
	with check (true);

drop policy if exists source_evidence_revisions_app on source_evidence_revisions;
create policy source_evidence_revisions_app on source_evidence_revisions
	for select to nano_app using (nano_has_notebook_capability(notebook_id, 'source.read'));
drop policy if exists source_admission_reports_app on source_admission_reports;
create policy source_admission_reports_app on source_admission_reports
	for select to nano_app using (nano_has_notebook_capability(notebook_id, 'source.read'));
drop policy if exists source_admission_reviews_app_select on source_admission_reviews;
create policy source_admission_reviews_app_select on source_admission_reviews
	for select to nano_app using (nano_has_notebook_capability(notebook_id, 'source.read'));
drop policy if exists source_admission_reviews_app_insert on source_admission_reviews;
create policy source_admission_reviews_app_insert on source_admission_reviews
	for insert to nano_app with check (
		reviewer_user_id=nullif(current_setting('app.principal_id', true),'')
		and nano_has_notebook_capability(notebook_id, 'source.maintain')
	);
drop policy if exists source_evidence_units_app on source_evidence_units;
create policy source_evidence_units_app on source_evidence_units
	for select to nano_app using (nano_has_notebook_capability(notebook_id, 'source.read'));
drop policy if exists source_viewer_artifacts_app on source_viewer_artifacts;
create policy source_viewer_artifacts_app on source_viewer_artifacts
	for select to nano_app using (nano_has_notebook_capability(notebook_id, 'source.read'));
drop policy if exists source_maps_app_read on source_maps;
create policy source_maps_app_read on source_maps
	for select to nano_app using (nano_has_notebook_capability(notebook_id, 'source.read'));
drop policy if exists source_evidence_coverage_app on source_evidence_coverage;
create policy source_evidence_coverage_app on source_evidence_coverage
	for select to nano_app using (
		exists (select 1 from source_evidence_revisions r where r.id=revision_id and nano_has_notebook_capability(r.notebook_id, 'source.read'))
	);
drop policy if exists source_evidence_coverage_gaps_app on source_evidence_coverage_gaps;
create policy source_evidence_coverage_gaps_app on source_evidence_coverage_gaps
	for select to nano_app using (
		exists (select 1 from source_evidence_revisions r where r.id=revision_id and nano_has_notebook_capability(r.notebook_id, 'source.read'))
	);

drop policy if exists source_evidence_revisions_worker on source_evidence_revisions;
create policy source_evidence_revisions_worker on source_evidence_revisions for all to nano_worker using (true) with check (true);
drop policy if exists source_admission_reports_worker on source_admission_reports;
create policy source_admission_reports_worker on source_admission_reports for all to nano_worker using (true) with check (true);
drop policy if exists source_admission_reviews_worker on source_admission_reviews;
create policy source_admission_reviews_worker on source_admission_reviews for select to nano_worker using (true);
drop policy if exists source_evidence_coverage_worker on source_evidence_coverage;
create policy source_evidence_coverage_worker on source_evidence_coverage for all to nano_worker using (true) with check (true);
drop policy if exists source_evidence_coverage_gaps_worker on source_evidence_coverage_gaps;
create policy source_evidence_coverage_gaps_worker on source_evidence_coverage_gaps for all to nano_worker using (true) with check (true);
drop policy if exists source_evidence_units_worker on source_evidence_units;
create policy source_evidence_units_worker on source_evidence_units for all to nano_worker using (true) with check (true);
drop policy if exists source_viewer_artifacts_worker on source_viewer_artifacts;
create policy source_viewer_artifacts_worker on source_viewer_artifacts for all to nano_worker using (true) with check (true);
drop policy if exists source_maps_worker on source_maps;
create policy source_maps_worker on source_maps for all to nano_worker using (true) with check (true);

drop policy if exists retrieval_index_versions_worker on retrieval_index_versions;
create policy retrieval_index_versions_worker on retrieval_index_versions for all to nano_worker using (true) with check (true);
drop policy if exists retrieval_index_versions_app_active on retrieval_index_versions;
create policy retrieval_index_versions_app_active on retrieval_index_versions
	for select to nano_app using (status='active');
drop policy if exists retrieval_eval_runs_worker on retrieval_eval_runs;
create policy retrieval_eval_runs_worker on retrieval_eval_runs for all to nano_worker using (true) with check (true);
drop policy if exists retrieval_source_index_builds_worker on retrieval_source_index_builds;
create policy retrieval_source_index_builds_worker on retrieval_source_index_builds for all to nano_worker using (true) with check (true);
drop policy if exists retrieval_source_index_builds_app on retrieval_source_index_builds;
create policy retrieval_source_index_builds_app on retrieval_source_index_builds
	for select to nano_app using (
		nano_has_notebook_capability(notebook_id, 'source.read')
		or nano_has_notebook_capability(notebook_id, 'source.maintain')
	);

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
	for all to nano_worker
	using (true)
	with check (true);

drop policy if exists chat_source_selections_private on chat_source_selections;
create policy chat_source_selections_private on chat_source_selections
	for all to nano_app
	using (exists (
		select 1 from chat_chats c
		where c.id=chat_source_selections.chat_id
		  and c.creator_user_id=nullif(current_setting('app.principal_id', true), '')
	))
	with check (exists (
		select 1 from chat_chats c join source_sources s on s.id=chat_source_selections.source_id
		where c.id=chat_source_selections.chat_id
		  and c.creator_user_id=nullif(current_setting('app.principal_id', true), '')
		  and c.notebook_id=s.notebook_id
	));

drop policy if exists chat_source_selections_worker on chat_source_selections;
create policy chat_source_selections_worker on chat_source_selections
	for all to nano_worker using (true) with check (true);

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

drop policy if exists source_discovery_sessions_private on source_discovery_sessions;
create policy source_discovery_sessions_private on source_discovery_sessions
	for all to nano_app
	using (
		user_id = nullif(current_setting('app.principal_id', true), '')
		and nano_has_notebook_capability(notebook_id,'source.maintain')
	)
	with check (
		user_id = nullif(current_setting('app.principal_id', true), '')
		and nano_has_notebook_capability(notebook_id,'source.maintain')
	);

drop policy if exists source_discovery_sessions_worker on source_discovery_sessions;
create policy source_discovery_sessions_worker on source_discovery_sessions
	for all to nano_worker using (true) with check (true);

drop policy if exists source_discovery_candidates_private on source_discovery_candidates;
create policy source_discovery_candidates_private on source_discovery_candidates
	for all to nano_app
	using (exists (
		select 1 from source_discovery_sessions s
		where s.id=source_discovery_candidates.session_id
		  and s.user_id=nullif(current_setting('app.principal_id', true), '')
		  and nano_has_notebook_capability(s.notebook_id,'source.maintain')
	))
	with check (exists (
		select 1 from source_discovery_sessions s
		where s.id=source_discovery_candidates.session_id
		  and s.user_id=nullif(current_setting('app.principal_id', true), '')
		  and nano_has_notebook_capability(s.notebook_id,'source.maintain')
	));

drop policy if exists source_discovery_candidates_worker on source_discovery_candidates;
create policy source_discovery_candidates_worker on source_discovery_candidates
	for all to nano_worker using (true) with check (true);

drop policy if exists source_discovery_jobs_private on source_discovery_jobs;
create policy source_discovery_jobs_private on source_discovery_jobs
	for all to nano_app
	using (exists (
		select 1 from source_discovery_sessions s
		where s.id=source_discovery_jobs.session_id
		  and s.user_id=nullif(current_setting('app.principal_id', true), '')
		  and nano_has_notebook_capability(s.notebook_id,'source.maintain')
	))
	with check (exists (
		select 1 from source_discovery_sessions s
		where s.id=source_discovery_jobs.session_id
		  and s.user_id=nullif(current_setting('app.principal_id', true), '')
		  and nano_has_notebook_capability(s.notebook_id,'source.maintain')
	));

drop policy if exists source_discovery_jobs_worker on source_discovery_jobs;
create policy source_discovery_jobs_worker on source_discovery_jobs
	for all to nano_worker using (true) with check (true);

create or replace function nano_run_owned(candidate_run_id text)
returns boolean
language sql
stable
security definer
set search_path = pg_catalog, public
as $$
	select exists (
		select 1
		from public.agent_runs run
		left join public.agent_trees tree on tree.id=run.tree_id
		left join public.chat_runs chat_product on chat_product.root_agent_run_id=tree.root_agent_run_id
		left join public.studio_outputs studio_product on studio_product.root_agent_run_id=tree.root_agent_run_id
		where run.id=candidate_run_id
		  and coalesce(run.user_id,chat_product.user_id,studio_product.created_by_user_id)=nullif(current_setting('app.principal_id', true), '')
	)
$$;
revoke all on function nano_run_owned(text) from public;
grant execute on function nano_run_owned(text) to nano_app;

-- Public Run activity is derived from Checkpoints, but nano_app must never
-- receive table-level access to the internal checkpoint ledger. This bounded
-- function exposes only one principal-owned Run to the server-side sanitizer.
create or replace function nano_owned_run_checkpoints(candidate_run_id text)
returns table(
	sequence_no integer,
	identity_key text,
	kind text,
	decision_no integer,
	action_index integer,
	action_id text,
	payload_version integer,
	payload_text text,
	payload_sha256 text,
	created_at timestamptz
)
language sql
stable
security definer
set search_path = pg_catalog, public
as $$
	select checkpoint.sequence_no,checkpoint.identity_key,checkpoint.kind,checkpoint.decision_no,
		checkpoint.action_index,checkpoint.action_id,checkpoint.payload_version,checkpoint.payload::text,
		checkpoint.payload_sha256,checkpoint.created_at
	from public.agent_run_checkpoints checkpoint
	where checkpoint.run_id=candidate_run_id
	  and public.nano_run_owned(candidate_run_id)
	order by checkpoint.sequence_no
$$;
revoke all on function nano_owned_run_checkpoints(text) from public;
grant execute on function nano_owned_run_checkpoints(text) to nano_app;

drop policy if exists agent_runs_private on agent_runs;
create policy agent_runs_private on agent_runs
	for all to nano_app
	using (
		user_id=nullif(current_setting('app.principal_id', true), '')
		or nano_run_owned(id)
	)
	with check (
		user_id=nullif(current_setting('app.principal_id', true), '')
		or nano_run_owned(id)
	);

drop policy if exists agent_runs_worker on agent_runs;
create policy agent_runs_worker on agent_runs
	for all to nano_worker
	using (true)
	with check (true);

drop policy if exists chat_runs_private on chat_runs;
create policy chat_runs_private on chat_runs
	for all to nano_app
	using (user_id = nullif(current_setting('app.principal_id', true), ''))
	with check (user_id = nullif(current_setting('app.principal_id', true), ''));

drop policy if exists chat_runs_worker on chat_runs;
create policy chat_runs_worker on chat_runs
	for all to nano_worker using (true) with check (true);

drop policy if exists research_sessions_private on research_sessions;
create policy research_sessions_private on research_sessions
	for all to nano_app
	using (user_id = nullif(current_setting('app.principal_id', true), ''))
	with check (user_id = nullif(current_setting('app.principal_id', true), ''));
drop policy if exists research_sessions_worker on research_sessions;
create policy research_sessions_worker on research_sessions
	for all to nano_worker using (true) with check (true);

drop policy if exists research_plan_versions_private on research_plan_versions;
create policy research_plan_versions_private on research_plan_versions
	for all to nano_app using (exists (
		select 1 from research_sessions session where session.id=research_plan_versions.session_id
		  and session.user_id=nullif(current_setting('app.principal_id', true), '')
	)) with check (exists (
		select 1 from research_sessions session where session.id=research_plan_versions.session_id
		  and session.user_id=nullif(current_setting('app.principal_id', true), '')
	));
drop policy if exists research_plan_versions_worker on research_plan_versions;
create policy research_plan_versions_worker on research_plan_versions
	for all to nano_worker using (true) with check (true);

drop policy if exists research_report_versions_private on research_report_versions;
create policy research_report_versions_private on research_report_versions
	for select to nano_app using (exists (
		select 1 from research_sessions session where session.id=research_report_versions.session_id
		  and session.user_id=nullif(current_setting('app.principal_id', true), '')
	));
drop policy if exists research_report_versions_worker on research_report_versions;
create policy research_report_versions_worker on research_report_versions
	for all to nano_worker using (true) with check (true);

drop policy if exists research_evidence_ledger_private on research_evidence_ledger;
create policy research_evidence_ledger_private on research_evidence_ledger
	for select to nano_app using (exists (
		select 1 from research_sessions session where session.id=research_evidence_ledger.session_id
		  and session.user_id=nullif(current_setting('app.principal_id', true), '')
	));
drop policy if exists research_evidence_ledger_worker on research_evidence_ledger;
create policy research_evidence_ledger_worker on research_evidence_ledger
	for all to nano_worker using (true) with check (true);

drop policy if exists research_source_imports_private on research_source_imports;
create policy research_source_imports_private on research_source_imports
	for select to nano_app using (exists (
		select 1 from research_sessions session where session.id=research_source_imports.session_id
		  and session.user_id=nullif(current_setting('app.principal_id', true), '')
	));
drop policy if exists research_source_imports_worker on research_source_imports;
create policy research_source_imports_worker on research_source_imports
	for all to nano_worker using (true) with check (true);

drop policy if exists research_step_capsules_private on research_step_capsules;
create policy research_step_capsules_private on research_step_capsules
	for select to nano_app using (exists (
		select 1 from research_sessions session where session.id=research_step_capsules.session_id
		  and session.user_id=nullif(current_setting('app.principal_id', true), '')
	));
drop policy if exists research_step_capsules_worker on research_step_capsules;
create policy research_step_capsules_worker on research_step_capsules
	for all to nano_worker using (true) with check (true);

drop policy if exists research_rollups_private on research_rollups;
create policy research_rollups_private on research_rollups
	for select to nano_app using (exists (
		select 1 from research_sessions session where session.id=research_rollups.session_id
		  and session.user_id=nullif(current_setting('app.principal_id', true), '')
	));
drop policy if exists research_rollups_worker on research_rollups;
create policy research_rollups_worker on research_rollups
	for all to nano_worker using (true) with check (true);

drop policy if exists research_archival_capsules_worker on research_archival_capsules;
create policy research_archival_capsules_worker on research_archival_capsules
	for all to nano_worker using (true) with check (true);
drop policy if exists research_task_memories_worker on research_task_memories;
create policy research_task_memories_worker on research_task_memories
	for all to nano_worker using (true) with check (true);
drop policy if exists research_compaction_failures_worker on research_compaction_failures;
create policy research_compaction_failures_worker on research_compaction_failures
	for all to nano_worker using (true) with check (true);

drop policy if exists studio_outputs_app_read on studio_outputs;
create policy studio_outputs_app_read on studio_outputs
	for select to nano_app using (nano_has_notebook_capability(notebook_id,'source.read'));
drop policy if exists studio_outputs_app_insert on studio_outputs;
create policy studio_outputs_app_insert on studio_outputs
	for insert to nano_app with check (
		created_by_user_id=nullif(current_setting('app.principal_id', true), '')
		and nano_has_notebook_capability(notebook_id,'source.maintain')
	);
drop policy if exists studio_outputs_app_delete on studio_outputs;
create policy studio_outputs_app_delete on studio_outputs
	for delete to nano_app using (nano_has_notebook_capability(notebook_id,'source.maintain'));
drop policy if exists studio_outputs_worker on studio_outputs;
create policy studio_outputs_worker on studio_outputs
	for all to nano_worker using (true) with check (true);

drop policy if exists agent_trees_private on agent_trees;
create policy agent_trees_private on agent_trees
	for all to nano_app
	using (root_agent_run_id is null or exists (
		select 1 from chat_runs product where product.root_agent_run_id=agent_trees.root_agent_run_id
		  and product.user_id=nullif(current_setting('app.principal_id', true), '')
	) or exists (
		select 1 from studio_outputs product where product.root_agent_run_id=agent_trees.root_agent_run_id
		  and product.created_by_user_id=nullif(current_setting('app.principal_id', true), '')
	))
	with check (root_agent_run_id is null or exists (
		select 1 from chat_runs product where product.root_agent_run_id=agent_trees.root_agent_run_id
		  and product.user_id=nullif(current_setting('app.principal_id', true), '')
	) or exists (
		select 1 from studio_outputs product where product.root_agent_run_id=agent_trees.root_agent_run_id
		  and product.created_by_user_id=nullif(current_setting('app.principal_id', true), '')
	));

drop policy if exists agent_trees_worker on agent_trees;
create policy agent_trees_worker on agent_trees
	for all to nano_worker using (true) with check (true);

drop policy if exists agent_run_results_worker_read on agent_run_results;
create policy agent_run_results_worker_read on agent_run_results
	for select to nano_worker using (true);
drop policy if exists agent_run_results_worker_append on agent_run_results;
create policy agent_run_results_worker_append on agent_run_results
	for insert to nano_worker with check (true);

drop policy if exists agent_run_delegations_worker on agent_run_delegations;
create policy agent_run_delegations_worker on agent_run_delegations
	for all to nano_worker using (true) with check (true);

drop policy if exists agent_research_outcomes_worker on agent_research_outcomes;
create policy agent_research_outcomes_worker on agent_research_outcomes
	for all to nano_worker using (true) with check (true);

drop policy if exists agent_role_checkpoints_worker_read on agent_role_checkpoints;
create policy agent_role_checkpoints_worker_read on agent_role_checkpoints
	for select to nano_worker using (true);
drop policy if exists agent_role_checkpoints_worker_append on agent_role_checkpoints;
create policy agent_role_checkpoints_worker_append on agent_role_checkpoints
	for insert to nano_worker with check (true);

drop policy if exists agent_run_evidence_set_app on agent_run_evidence_set;
create policy agent_run_evidence_set_app on agent_run_evidence_set
	for all to nano_app
	using (nano_run_owned(run_id))
	with check (nano_run_owned(run_id));

drop policy if exists agent_run_delegations_owner_read on agent_run_delegations;
create policy agent_run_delegations_owner_read on agent_run_delegations
	for select to nano_app using (nano_run_owned(parent_run_id));
drop policy if exists agent_run_delegations_owner_update on agent_run_delegations;
create policy agent_run_delegations_owner_update on agent_run_delegations
	for update to nano_app using (nano_run_owned(parent_run_id))
	with check (nano_run_owned(parent_run_id));

drop policy if exists agent_run_evidence_set_worker on agent_run_evidence_set;
create policy agent_run_evidence_set_worker on agent_run_evidence_set
	for all to nano_worker using (true) with check (true);

drop policy if exists agent_run_grounding_plans_worker on agent_run_grounding_plans;
create policy agent_run_grounding_plans_worker on agent_run_grounding_plans
	for all to nano_worker using (true) with check (true);
drop policy if exists agent_claim_support_records_worker on agent_claim_support_records;
create policy agent_claim_support_records_worker on agent_claim_support_records
	for all to nano_worker using (true) with check (true);
drop policy if exists agent_draft_citations_worker on agent_draft_citations;
create policy agent_draft_citations_worker on agent_draft_citations
	for all to nano_worker using (true) with check (true);
drop policy if exists agent_draft_source_references_worker on agent_draft_source_references;
create policy agent_draft_source_references_worker on agent_draft_source_references
	for all to nano_worker using (true) with check (true);
drop policy if exists chat_citations_worker on chat_citations;
create policy chat_citations_worker on chat_citations
	for all to nano_worker using (true) with check (true);
drop policy if exists chat_citations_app_read on chat_citations;
create policy chat_citations_app_read on chat_citations
	for select to nano_app using (
		exists (
			select 1 from chat_messages m join chat_chats c on c.id=m.chat_id
			where m.id=chat_citations.message_id
			  and c.creator_user_id=nullif(current_setting('app.principal_id', true), '')
		)
	);

drop policy if exists agent_run_checkpoints_worker_read on agent_run_checkpoints;
create policy agent_run_checkpoints_worker_read on agent_run_checkpoints
	for select to nano_worker
	using (true);

drop policy if exists agent_run_checkpoints_worker_append on agent_run_checkpoints;
create policy agent_run_checkpoints_worker_append on agent_run_checkpoints
	for insert to nano_worker
	with check (true);

drop policy if exists agent_context_compactions_owner_read on agent_context_compactions;
create policy agent_context_compactions_owner_read on agent_context_compactions
	for select to nano_app using (
		exists (
			select 1 from chat_chats c
			where c.id=agent_context_compactions.chat_id
			  and c.creator_user_id=nullif(current_setting('app.principal_id', true), '')
		)
	);
drop policy if exists agent_context_compactions_worker_read on agent_context_compactions;
create policy agent_context_compactions_worker_read on agent_context_compactions
	for select to nano_worker using (true);
drop policy if exists agent_context_compactions_worker_append on agent_context_compactions;
create policy agent_context_compactions_worker_append on agent_context_compactions
	for insert to nano_worker with check (true);

drop policy if exists agent_traces_app_insert on agent_traces;
create policy agent_traces_app_insert on agent_traces
	for insert to nano_app
	with check (nano_run_owned(run_id));

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
		  and public.nano_run_owned(r.id)
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
		  and public.nano_run_owned(r.id)
	)
$$;
revoke all on function nano_trace_ref_owned(text) from public;
grant execute on function nano_trace_ref_owned(text) to nano_app;

create or replace function nano_owned_run_trace_descriptor(candidate_run_id text)
returns table(
	trace_id text,
	run_id text,
	chat_id text,
	notebook_id text,
	root_span_id text,
	agent_name text,
	schema_version integer,
	semantic_convention_version integer
)
language sql
stable
security definer
set search_path = pg_catalog, public
as $$
	select t.trace_id, t.run_id, t.chat_id, t.notebook_id, t.root_span_id,
		t.agent_name, t.schema_version, t.semantic_convention_version
	from public.agent_trace_refs t
	join public.agent_runs r on r.id = t.run_id
	where t.run_id = candidate_run_id
	  and public.nano_run_owned(r.id)
$$;
revoke all on function nano_owned_run_trace_descriptor(text) from public;
grant execute on function nano_owned_run_trace_descriptor(text) to nano_app;


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
	with check (nano_run_owned(run_id));

drop policy if exists agent_trace_refs_worker on agent_trace_refs;
create policy agent_trace_refs_worker on agent_trace_refs
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
	using (nano_run_owned(run_id))
	with check (nano_run_owned(run_id));

drop policy if exists agent_jobs_worker on agent_jobs;
create policy agent_jobs_worker on agent_jobs
	for all to nano_worker
	using (true)
	with check (true);
` + runtimeSubagentSQL

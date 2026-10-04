package app

// Runtime subagents share their parent's pinned definition and tree authority.
// They have their own durable transcript and never publish a Chat message.
const runtimeSubagentSQL = `
create table if not exists agent_subagents (
	child_run_id text primary key references agent_runs(id) on delete cascade,
	parent_run_id text not null references agent_runs(id) on delete cascade,
	action_id text not null,
	task_name text not null check (char_length(task_name) between 1 and 80),
	message text not null check (char_length(message) between 1 and 16000),
	cancelled_attempt_no integer check (cancelled_attempt_no>=0),
	created_at timestamptz not null default now(),
	unique(parent_run_id,action_id),
	check (child_run_id<>parent_run_id)
);
alter table agent_subagents add column if not exists cancelled_attempt_no integer check (cancelled_attempt_no>=0);
create or replace function nano_research_root_run(run_id text) returns text
language sql stable as $$
 select coalesce((select parent_run_id from agent_subagents where child_run_id=run_id),run_id)
$$;
create index if not exists agent_subagents_parent_idx on agent_subagents(parent_run_id,created_at);

create table if not exists agent_subagent_waits (
	parent_run_id text not null references agent_runs(id) on delete cascade,
	action_id text not null,
	agent_ids text[] not null check (cardinality(agent_ids) between 1 and 16),
	deadline_at timestamptz not null,
	primary key(parent_run_id,action_id)
);
alter table agent_subagents enable row level security;
alter table agent_subagent_waits enable row level security;
grant select,insert on agent_subagents to nano_worker;
grant select on agent_subagents to nano_app;
grant select,insert,delete on agent_subagent_waits to nano_worker;
drop policy if exists agent_subagents_owner_read on agent_subagents;
create policy agent_subagents_owner_read on agent_subagents
	for select to nano_app using (nano_run_owned(parent_run_id));
drop policy if exists agent_subagents_worker on agent_subagents;
create policy agent_subagents_worker on agent_subagents for all to nano_worker using (true) with check (true);
drop policy if exists agent_subagent_waits_worker on agent_subagent_waits;
create policy agent_subagent_waits_worker on agent_subagent_waits for all to nano_worker using (true) with check (true);

create or replace function nano_validate_runtime_subagent()
returns trigger language plpgsql as $$
declare parent agent_runs%rowtype; child agent_runs%rowtype;
begin
	select * into parent from agent_runs where id=new.parent_run_id for update;
	select * into child from agent_runs where id=new.child_run_id;
	if parent.status<>'running' or parent.runtime_kind<>'configured'
		or parent.executor_identity<>'research_root'
		or exists(select 1 from agent_subagents where child_run_id=parent.id)
		or child.status<>'queued' or child.runtime_kind<>'configured'
		or (child.tree_id,child.definition_identity,child.definition_version,child.definition_sha256,
			child.executor_identity,child.model_policy_identity,child.model_policy_version,child.model_policy_sha256,
			child.provider_model,child.provider_capability_sha256,child.model_context_policy_sha256,child.parent_context_manifest)
		is distinct from
		(parent.tree_id,parent.definition_identity,parent.definition_version,parent.definition_sha256,
			parent.executor_identity,parent.model_policy_identity,parent.model_policy_version,parent.model_policy_sha256,
			parent.provider_model,parent.provider_capability_sha256,parent.model_context_policy_sha256,parent.parent_context_manifest)
	then raise exception 'runtime subagent must inherit an active root and cannot spawn descendants'; end if;
	if (select count(*) from agent_subagents where parent_run_id=parent.id)>=16
		or (select count(*) from agent_subagents s join agent_runs r on r.id=s.child_run_id
			where s.parent_run_id=parent.id and r.status in ('queued','running'))>=4
	then raise exception 'runtime subagent capacity exhausted'; end if;
	return new;
end $$;
drop trigger if exists agent_subagents_validate on agent_subagents;
create trigger agent_subagents_validate before insert on agent_subagents
	for each row execute function nano_validate_runtime_subagent();

create or replace function nano_sync_runtime_subagents()
returns trigger language plpgsql security definer
set search_path = pg_catalog, public as $$
begin
	if new.status in ('completed','failed','cancelled') and old.status not in ('completed','failed','cancelled') then
		-- A waiting root is queued for its timeout; finishing a selected child
		-- makes it runnable immediately. No worker remains occupied by a wait.
		update agent_jobs job set available_at=now(),updated_at=now()
		from agent_subagents s join agent_subagent_waits w on w.parent_run_id=s.parent_run_id
		join agent_runs parent on parent.id=s.parent_run_id
		where s.child_run_id=new.id and new.id=any(w.agent_ids)
			and job.run_id=parent.id and parent.status='queued' and job.status='queued'
            and not exists(select 1 from agent_run_checkpoints c where c.run_id=w.parent_run_id
                and c.action_id=w.action_id and c.kind='action_result');
		perform pg_notify('nano_agent_jobs',new.id);
		-- Root termination cancels outstanding work, including expired leases.
		-- Preserve whether an Attempt was running so Trace cleanup can close
		-- that span without re-closing an Attempt already ended by a wait.
		update agent_subagents s set cancelled_attempt_no=case when job.status='running' then job.attempt_no else 0 end
		from agent_runs child join agent_jobs job on job.run_id=child.id
		where s.parent_run_id=new.id and child.id=s.child_run_id and child.status in ('queued','running');
		update agent_runs child set status='cancelled',error_code='parent_terminal',finished_at=now(),updated_at=now()
		from agent_subagents s where s.parent_run_id=new.id and child.id=s.child_run_id
			and child.status in ('queued','running');
		update agent_jobs job set status='cancelled',lease_token=null,lease_expires_at=null,finished_at=now(),updated_at=now()
		from agent_subagents s join agent_runs child on child.id=s.child_run_id
		where s.parent_run_id=new.id and job.run_id=child.id and child.status='cancelled'
			and job.status in ('queued','running','waiting');
	end if;
	return new;
end $$;
drop trigger if exists agent_runs_sync_runtime_subagents on agent_runs;
create trigger agent_runs_sync_runtime_subagents after update of status on agent_runs
	for each row execute function nano_sync_runtime_subagents();
`

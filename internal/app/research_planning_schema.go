package app

// A Research planning conversation lets the planner pause for Member answers.
// A question suspends its planning Run until answered.
const researchPlanningSQL = `
alter table research_sessions drop constraint if exists research_sessions_status_check;
alter table research_sessions add constraint research_sessions_status_check
	check (status in ('planning','awaiting_input','awaiting_confirmation','queued','running','publishing','completed','failed','cancelled'));

create table if not exists research_planning_questions (
	run_id text not null references agent_runs(id) on delete cascade,
	action_id text not null check (char_length(action_id) between 1 and 128),
	session_id text not null references research_sessions(id) on delete cascade,
	questions jsonb not null check (jsonb_typeof(questions)='array'),
	answers jsonb check (answers is null or jsonb_typeof(answers)='array'),
	created_at timestamptz not null default now(),
	answered_at timestamptz,
	primary key(run_id,action_id),
	check ((answers is null) = (answered_at is null))
);
create index if not exists research_planning_questions_open_idx
	on research_planning_questions(session_id) where answered_at is null;

alter table research_planning_questions enable row level security;
grant select, insert, update on research_planning_questions to nano_worker;
grant select, update(answers,answered_at) on research_planning_questions to nano_app;

drop policy if exists research_planning_questions_private on research_planning_questions;
create policy research_planning_questions_private on research_planning_questions
	for all to nano_app using (exists (
		select 1 from research_sessions session where session.id=research_planning_questions.session_id
		  and session.user_id=nullif(current_setting('app.principal_id', true), '')
	)) with check (exists (
		select 1 from research_sessions session where session.id=research_planning_questions.session_id
		  and session.user_id=nullif(current_setting('app.principal_id', true), '')
	));
drop policy if exists research_planning_questions_worker on research_planning_questions;
create policy research_planning_questions_worker on research_planning_questions
	for all to nano_worker using (true) with check (true);
`

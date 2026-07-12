# Parent Issue Template

Use the parent Issue for recovery metadata and workflow summary only. Detailed agent outputs stay in child Issue comments and Git history.

## User Request

- Title: `<delivery-title>`
- Scope summary: `<normalized-request>`
- Repository: `<repo-name-or-resource-label>`

## Workflow Metadata

Set or maintain these parent metadata keys:

- `workflow_version`: `multica-delivery/v1`
- `phase`: `<requirements|plan|implementation|verification|rework|accepted>`
- `goal_identifier`: `<codex-goal-id-or-stable-local-label>`
- `queue_state`: `<queued|active|complete>`
- `queue_position`: `<1|2|3>`
- `memory_path`: `memory/runs/<parent-identifier>.md`
- `candidate_sha`: `<sha-or-empty>`
- `final_sha`: `<sha-or-empty>`
- `repo_resource`: `<project-git-resource>`
- `repo_default_ref`: `<default-ref>`

## Recovery Notes

- Baseline SHA: `<baseline-sha>`
- Current stage wave: `<stage-number>`
- Approved Requirements comment: `<issue-or-comment-ref>`
- Approved Plan comment: `<issue-or-comment-ref>`
- Active verification SHA: `<sha-or-none>`

Queue rules:

- one Goal may track at most three open parent Issues
- exactly one tracked parent is `queue_state = active`
- queued parents remain inactive until Codex promotes them
- when the active parent completes, Codex marks it `queue_state = complete` and promotes the next queued parent by queue position

## Completion Guard

Close the parent only after Requirements and Plan are approved, QA and Review pass the same final SHA, and the memory record exists at `memory/runs/<parent-identifier>.md`.

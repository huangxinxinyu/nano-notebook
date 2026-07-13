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
- `candidate_branch`: `<remote-candidate-branch>`
- `candidate_sha`: `<sha-or-empty>`
- `target_branch`: `<target-branch>`
- `target_head_sha`: `<sha-recorded-at-candidate-creation>`
- `final_sha`: `<sha-or-empty>`
- `memory_commit_sha`: `<sha-or-empty>`
- `repo_resource`: `<project-git-resource>`
- `repo_default_ref`: `<default-ref>`

## Recovery Notes

- Baseline SHA: `<baseline-sha>`
- Candidate branch: `<remote-candidate-branch>`
- Target branch: `<target-branch>`
- Recorded target head at candidate creation: `<target-head-sha>`
- Current stage wave: `<stage-number>`
- Approved Requirements comment: `<issue-or-comment-ref>`
- Approved Plan comment: `<issue-or-comment-ref>`
- Active verification SHA: `<sha-or-none>`
- Verified delivery SHA: `<final-sha-or-none>`
- Memory-only commit SHA: `<memory-commit-sha-or-none>`

Queue rules:

- one Goal may track at most three open parent Issues
- exactly one tracked parent is `queue_state = active`
- queued parents remain inactive until Codex promotes them
- when the active parent completes, Codex marks it `queue_state = complete` and promotes the next queued parent by queue position

## Completion Guard

Close the parent only after Requirements and Plan are approved, QA and Review pass the same `final_sha`, Codex fast-forward merges and pushes that SHA, and a later Codex-owned `memory_commit_sha` changes only `memory/runs/<parent-identifier>.md`. The remote target tip must equal `memory_commit_sha` and contain `final_sha` as an ancestor.

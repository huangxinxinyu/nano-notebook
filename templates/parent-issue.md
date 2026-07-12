# Parent Issue Template

Use the parent Issue for recovery metadata and workflow summary only. Detailed agent outputs stay in child Issue comments and Git history.

## User Request

- Title: `<delivery-title>`
- Scope summary: `<normalized-request>`
- Repository: `<repo-name-or-resource-label>`

## Workflow Metadata

Set or maintain these parent metadata keys:

- `workflow_version`: `multica-delivery-v1`
- `current_phase`: `<requirements|plan|implementation|verification|rework|accepted>`
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

## Completion Guard

Close the parent only after Requirements and Plan are approved, QA and Review pass the same final SHA, and the memory record exists at `memory/runs/<parent-identifier>.md`.

# Recovery Reference

## Source of Truth Order

When reconstructing state, prefer:

1. raw Multica Issue fields, comments, runs, and messages
2. Git commits, branches, diffs, and memory files
3. previous summaries

If summaries conflict with raw Multica or Git facts, trust the raw facts.

## Recovery Inputs

Resume a Goal queue from:

- parent Issue fields and metadata
- child Issues grouped by stage
- the latest accepted Requirements and Plan comments
- candidate and final SHAs
- `memory/runs/<parent-identifier>.md` when present

Required parent metadata for queue recovery:

- `goal_identifier`
- `queue_state` with value `queued`, `active`, or `complete`
- `queue_position`
- `workflow_version = multica-delivery/v1`
- `phase`

## Resume Procedure

1. Read the parent Issue and metadata for every open parent tracked by the current `goal_identifier`.
2. Sort tracked parents by `queue_position`.
3. Confirm there is exactly one `queue_state = active` parent. If none is active, promote the first queued parent. If more than one is active, pause and correct the metadata before advancing work.
4. List child Issues by stage for the active parent only.
5. Read the most recent active comment threads on the current-stage child Issues.
6. Confirm the last accepted gate and the active candidate SHA, if any.
7. Inspect the repository state and verify the named SHA locally.
8. Continue with the next safe action only after the workflow state is coherent.

## Promotion Rule

When the active parent reaches accepted state:

1. update that parent to `queue_state = complete`
2. select the lowest `queue_position` parent still marked `queued`
3. update it to `queue_state = active`
4. begin or resume child Issue advancement on that promoted parent

This is an operating invariant that makes recovery deterministic. It is not a distributed lock or lease system.

## When to Pause

Pause and escalate when:

- two sources disagree in a way that changes the next safe action
- the candidate SHA cannot be inspected
- required child Issue context is missing
- another active Codex Goal appears to be operating the same parent Issue
- queue metadata is missing or ambiguous

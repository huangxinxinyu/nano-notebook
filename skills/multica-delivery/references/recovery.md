# Recovery Reference

## Source of Truth Order

When reconstructing state, prefer:

1. raw Multica Issue fields, comments, runs, and messages
2. Git commits, branches, diffs, and memory files
3. previous summaries

If summaries conflict with raw Multica or Git facts, trust the raw facts.

## Recovery Inputs

Resume a parent Issue from:

- parent Issue fields and metadata
- child Issues grouped by stage
- the latest accepted Requirements and Plan comments
- candidate and final SHAs
- `memory/runs/<parent-identifier>.md` when present

## Resume Procedure

1. Read the parent Issue and metadata.
2. List child Issues by stage.
3. Read the most recent active comment threads on the current-stage child Issues.
4. Confirm the last accepted gate and the active candidate SHA, if any.
5. Inspect the repository state and verify the named SHA locally.
6. Continue with the next safe action only after the workflow state is coherent.

## When to Pause

Pause and escalate when:

- two sources disagree in a way that changes the next safe action
- the candidate SHA cannot be inspected
- required child Issue context is missing
- another active Codex Goal appears to be operating the same parent Issue

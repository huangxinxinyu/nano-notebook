# NAN-1: Multica Delivery Framework V1

## Requirement

Build a local-first, explicitly triggered delivery framework in which Codex is
the sole Leader and user contact, while Multica Agents execute scoped delivery,
QA, and review work. Keep workflow state in Multica and code plus lightweight
memory in Git.

## Plan

Add the Leader Skill and references, role contracts, stage-specific Issue
templates, placeholder configuration, recovery metadata, eval scenarios,
operator documentation, backend guidance, and the repository memory scaffold.
Validate the framework through a real self-bootstrap run with independent QA,
Review, rework, remote candidate transport, and target-branch integration.

## Decisions And Constraints

- Activation is explicit only; ordinary coding requests bypass Multica.
- One Goal may track up to three Parent Issues, with exactly one active Parent.
- Codex creates and assigns Issues, approves gates, merges accepted work, and is
  the only repository memory writer.
- Delivery Expert is the only implementation-writing Agent. QA and Reviewer are
  read-only and verify the same remote candidate SHA.
- Each Parent uses one normal-pushed candidate branch; force push is forbidden.
- Codex accepts only a fast-forward target update. Target drift requires
  integration and a new verification wave.
- `final_sha` identifies the verified delivery. A later path-only memory commit
  is recorded separately as `memory_commit_sha`.
- Release, deployment, tagging, production observation, Squad operation, and
  multi-Goal locking remain outside V1.

## Acceptance Evidence

- Approved Requirements: NAN-2 comment
  `0cead46b-0e25-4316-9023-a79d7c6b508e`.
- Approved Plan: NAN-3 comment
  `989e969c-d1a3-49f3-8b4e-41e46b8cc66c`.
- Final Multica QA before the user-directed governance correction: NAN-12
  comment `01a36d74-6b65-4811-afea-99cd6eaf6a00`.
- Final Multica Review before the user-directed governance correction: NAN-13
  comment `860f1456-2e4e-4a61-8c7b-b5f11b446d6e`.
- User-directed Codex completion override: NAN-1 comment
  `fd14ac10-5eeb-443d-b097-e58916b6eed3`.
- Final accepted delivery SHA:
  `0faa90d0a445e6e9b91b9b4277eae8a2ed5e8f22`.

## Outcome

The end-to-end loop ran successfully: Codex assigned staged work through the
Multica CLI, Delivery pushed candidate commits, QA and Review independently
checked exact SHAs, blocking findings produced focused rework, target drift was
integrated without force push, and Codex fast-forwarded and pushed `main`.

The user requested the final dual-SHA governance correction be implemented
directly by Codex. Its contract checker and diff hygiene passed, protected user
documentation was not changed, and the same commit was pushed to `main` and the
Parent candidate branch.

## Reusable Lessons

- QA and Review are complementary: mechanical checks can pass while semantic
  recovery or scope contradictions remain.
- A candidate must be remotely reachable before independent Agents can verify
  the same SHA.
- Repository memory needs its own commit identity because writing evidence
  after verification necessarily advances the target branch.
- Parent metadata must distinguish candidate, verified delivery, and memory
  commit SHAs for deterministic recovery.

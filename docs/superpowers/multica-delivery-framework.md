# Multica Delivery Framework V1

## Purpose

This repository includes a local-first delivery framework in which Codex leads a Multica workflow end to end. Codex is the only workflow leader, user contact, gate approver, and memory writer. Multica Agents execute stage-specific work under fixed role contracts.

## Trigger Contract

The workflow is opt-in only. Start it only when the user explicitly asks for the Multica delivery workflow or invokes `$multica-delivery`.

Ordinary coding requests do not trigger this framework.

## Repository Surface

The framework lives in:

```text
skills/multica-delivery/
templates/
config/multica.example.toml
doc/engineering/loop-engineering/BACKEND_ENGINEERING.md
memory/
docs/superpowers/multica-delivery-framework.md
evals/multica-delivery-framework-v1.md
agents/
```

## Local Configuration

Fill local values outside committed framework content:

- Workspace name and ID
- Project name and ID
- Delivery Expert, QA, and Reviewer Agent IDs
- Project Git resource label
- Runtime ID

Use [config/multica.example.toml](../../config/multica.example.toml) as the placeholder source. Do not commit live IDs, machine paths, credentials, or repository access details into reusable files.

## Preflight

Before opening the parent Issue:

1. Confirm the request explicitly opted into Multica delivery.
2. Confirm the target repository and scope.
3. Confirm the Workspace selected by local configuration is the intended target.
4. Confirm the Project, role Agents, and Runtime exist and are usable.
5. Inspect the repository for pre-existing changes that must be preserved.
6. Restate the interpreted scope and wait for one confirmation.

## Workflow

1. A single Goal may track up to three open parent Issues at a time.
2. Each tracked parent stores queue recovery metadata on the parent Issue: owning Goal identifier, `queue_state`, `queue_position`, workflow version, current phase, repo fields, branch fields, SHA fields, and memory path.
3. Keep exactly one parent active at a time. Other tracked parents remain queued and inactive until promoted.
4. Create and approve stage-specific child Issues on the active parent in order: Requirements, Plan, Implementation.
5. Delivery Expert runs the required checks, commits the approved change, and pushes the named candidate branch without force.
6. Codex verifies the remote branch tip matches the reported candidate SHA before dispatching QA and Review.
7. Open QA and Review in parallel against the same candidate branch and candidate SHA for that active parent.
8. If either verification role fails, create a Rework Issue and then a new verification wave.
9. If the target branch head changes after candidate creation, stop acceptance, create an integration or rework path from the new target head, and rerun verification on the replacement SHA.
10. When QA and Review both pass, record the candidate as `final_sha`; Codex performs a fast-forward-only merge and pushes the target branch to that exact SHA.
11. Codex then commits only the canonical run memory file, pushes normally, and records the resulting `memory_commit_sha`.
12. Verify that the remote target tip equals `memory_commit_sha`, its diff changes only the run memory file, and it contains `final_sha` as an ancestor.
13. Then mark the active parent complete, promote the next queued parent, and continue serially.

Parent Issues store recovery metadata only. Child Issue comments and Git commits hold the detailed evidence.

For backend-scoped Implementation work, use
[doc/engineering/loop-engineering/BACKEND_ENGINEERING.md](../../doc/engineering/loop-engineering/BACKEND_ENGINEERING.md)
only when the approved scope touches APIs, storage, background work,
consistency, security, observability, or production behavior.

## Gates

- Requirements: scope, exclusions, rules, edge cases, and acceptance criteria are clear enough to implement.
- Plan: file boundaries, implementation steps, risks, and verification strategy are executable.
- Implementation: the candidate SHA is pushed to the named remote candidate branch, the diff stays in scope, and the evidence is credible.
- Verification: QA and Review both pass the same remote candidate SHA.
- Acceptance merge: the recorded target head still matches the live target head, the merge is fast-forward only, and Codex performs the push.
- Memory finalization: Codex alone creates a path-only memory commit after the target reaches `final_sha`, then records and verifies `memory_commit_sha`.

Focused correction stays on the same Issue when the output is incomplete but the accepted code state does not need to change. Rework creates a new stage wave when implementation must change or a new SHA is required.

## Recovery

Resume from:

- parent Issue metadata
- child Issues grouped by stage
- approved gate comments
- candidate, final, and memory commit SHAs
- `memory/runs/<parent-identifier>.md`

Queue recovery is deterministic from parent metadata:

- use the same `goal_identifier` across tracked parents for one Goal
- allow at most three tracked open parents
- require exactly one `queue_state = active` parent
- record the candidate branch, target branch, `target_head_sha`, `final_sha`, and `memory_commit_sha`
- sort the rest by `queue_position`
- if the active parent is complete, promote the lowest queued parent next

This is an operating invariant for recovery, not a distributed lock.

If summaries disagree with raw Multica or Git facts, trust the raw facts.

## Memory

Run memory lives at `memory/runs/<parent-identifier>.md`. Only Codex writes these files. After the verified delivery is merged and pushed as `final_sha`, Codex commits only this file and records that later commit as `memory_commit_sha`; no second Agent verification wave is required. `memory/knowledge/` is for reviewed stable knowledge, and `memory/improvements/` is for proposed framework changes.

## Self-Bootstrap Evidence

The framework is considered usable only when a real Multica-driven run against this repository can prove:

- ordinary coding requests bypass the workflow
- explicit invocation opens the correct Workspace and Project
- Requirements, Plan, Implementation, QA, Review, and Rework stages behave as designed
- QA and Review verify the same remote candidate branch and candidate SHA
- final acceptance records separate verified delivery and memory-only commit SHAs

Use [evals/multica-delivery-framework-v1.md](../../evals/multica-delivery-framework-v1.md) as the scenario checklist for that run.

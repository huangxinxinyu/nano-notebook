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

1. Create one parent Issue using [templates/parent-issue.md](../../templates/parent-issue.md).
2. Create and approve stage-specific child Issues in order: Requirements, Plan, Implementation.
3. Open QA and Review in parallel against the same candidate SHA.
4. If either verification role fails, create a Rework Issue and then a new verification wave.
5. Close the parent only after QA and Review pass the same final SHA and the run memory file is written.

Parent Issues store recovery metadata only. Child Issue comments and Git commits hold the detailed evidence.

For backend-scoped Implementation work, use
[doc/engineering/loop-engineering/BACKEND_ENGINEERING.md](../../doc/engineering/loop-engineering/BACKEND_ENGINEERING.md)
only when the approved scope touches APIs, storage, background work,
consistency, security, observability, or production behavior.

## Gates

- Requirements: scope, exclusions, rules, edge cases, and acceptance criteria are clear enough to implement.
- Plan: file boundaries, implementation steps, risks, and verification strategy are executable.
- Implementation: the candidate SHA exists, the diff stays in scope, and the evidence is credible.
- Verification: QA and Review both pass the same candidate SHA.

Focused correction stays on the same Issue when the output is incomplete but the accepted code state does not need to change. Rework creates a new stage wave when implementation must change or a new SHA is required.

## Recovery

Resume from:

- parent Issue metadata
- child Issues grouped by stage
- approved gate comments
- candidate and final SHAs
- `memory/runs/<parent-identifier>.md`

If summaries disagree with raw Multica or Git facts, trust the raw facts.

## Memory

Run memory lives at `memory/runs/<parent-identifier>.md`. Only Codex writes these files. `memory/knowledge/` is for reviewed stable knowledge, and `memory/improvements/` is for proposed framework changes.

## Self-Bootstrap Evidence

The framework is considered usable only when a real Multica-driven run against this repository can prove:

- ordinary coding requests bypass the workflow
- explicit invocation opens the correct Workspace and Project
- Requirements, Plan, Implementation, QA, Review, and Rework stages behave as designed
- QA and Review verify the same candidate SHA
- final acceptance writes the memory record

Use [evals/multica-delivery-framework-v1.md](../../evals/multica-delivery-framework-v1.md) as the scenario checklist for that run.

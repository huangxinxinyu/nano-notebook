---
name: multica-delivery
description: Use when the user explicitly asks to start or resume the Multica delivery workflow for this repository, or invokes `$multica-delivery`.
---

# Multica Delivery

## Overview

Run the Multica Delivery Framework V1 only as an explicit opt-in workflow. Codex stays the only workflow leader, user contact, gate approver, and memory writer.

## Activation Gate

Use this skill only when the user explicitly names the Multica delivery workflow or invokes `$multica-delivery`.

Do not use this skill for ordinary coding requests such as bug fixes, refactors, feature work, code review, or debugging. Those requests stay in the normal Codex workflow unless the user explicitly opts into Multica delivery.

Before creating any Multica Issue, restate the interpreted scope and wait for one confirmation.

## Required Reads

Read these references before acting:

- `references/workflow.md`
- `references/gates.md`
- `references/recovery.md`
- `references/memory-policy.md`

Use the repository templates in `templates/` when creating the parent Issue and each child Issue.

## Preflight

Before opening the parent Issue, confirm:

1. The target repository and requested scope are known.
2. The configured Workspace selected by local configuration is the intended target.
3. The configured Project and the three role Agents exist.
4. A compatible Runtime is online.
5. Existing repository changes are identified and must be preserved.

If any preflight check fails, stop and resolve it before creating workflow Issues.

## Workflow Contract

1. A single Goal may track up to three open parent Issues at a time.
2. Record queue recovery metadata on each parent Issue, including the owning Goal identifier, `queue_state` (`queued|active|complete`), and queue position.
3. Keep exactly one parent Issue active at a time. Other tracked parents stay queued and inactive until promoted.
4. Create and advance stage-specific child Issues only for the active parent Issue.
5. Advance gates on the active parent in order: Requirements, Plan, Implementation, then QA and Review in parallel.
6. Delivery Expert pushes the candidate branch after required checks pass.
7. Never force push the candidate branch.
8. Codex verifies the remote candidate branch and SHA before opening QA and Review.
9. Give QA and Review the same remote candidate branch and SHA on that active parent.
10. If QA or Review blocks acceptance, create a Rework Issue and then a new verification wave for the new SHA.
11. Only Codex may fast-forward merge the accepted candidate into the target branch.
12. If the target branch head moved after candidate creation, block acceptance, create an integration or rework path from the new target head, and rerun verification on the replacement SHA.
13. When the active parent reaches accepted state, mark it `queue_state = complete`, promote the next queued parent, and continue serially.
14. Close a parent only after both verification roles pass the same final SHA, the accepted candidate SHA becomes the target branch SHA, and the memory record is written.

Multica stores workflow history. Git stores code, commits, and memory files. Do not duplicate full logs across both systems.

## Role Boundaries

- Codex: creates Issues, assigns work, approves gates, resolves user-facing questions, records SHAs, writes memory.
- Delivery Expert: the only Agent allowed to modify and commit code.
- QA and Reviewer: read-only against committed source and must verify the exact SHA named by Codex.

## Escalation

After the initial confirmation, ask the user only when:

- business intent remains ambiguous after repository/context inspection
- approved scope must expand
- a high-risk external decision is required
- QA and Review disagree in a way Codex cannot resolve
- the same blocker repeats without meaningful progress

## Completion

The workflow is complete only when:

- approved Requirements and Plan records exist
- implementation stays within approved scope
- QA and Review both pass the same final SHA
- no blocking workflow issue remains
- `memory/runs/<parent-identifier>.md` is written

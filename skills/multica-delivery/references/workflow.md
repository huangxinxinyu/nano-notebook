# Workflow Reference

## Parent Queue

One Goal may track up to three open parent Issues at a time. Tracking means the parent belongs to that Goal's delivery queue through parent metadata, not that work is advancing on all of them.

Queue invariants:

- exactly one tracked parent has `queue_state = active`
- additional tracked parents use `queue_state = queued`
- accepted parents use `queue_state = complete`
- parent queue order is explicit through `queue_position`
- QA and Review may run in parallel only within the active parent

Queued parents remain inactive until promoted. Do not create or advance child Issues for queued parents.

## Parent and Child Issues

Every requirement gets one parent Issue plus stage-specific child Issues when that parent becomes active. The parent Issue holds recovery metadata only. Detailed outputs stay in child Issue comments and Git.

Typical successful flow:

```text
Parent Issue
|-- Stage 1: Requirements -> Delivery Expert
|-- Stage 2: Plan -> Delivery Expert
|-- Stage 3: Implementation -> Delivery Expert
`-- Stage 4: QA -> QA
               Review -> Reviewer
```

If verification fails:

```text
Stage 4: QA and/or Review fail
Stage 5: Rework -> Delivery Expert
Stage 6: QA -> QA
         Review -> Reviewer
```

Stages are execution-wave numbers for audit and recovery. They do not approve output and they do not replace role-specific Issue content.

## Issue Lifecycle

1. Create or select the parent Issue and set its queue metadata.
2. If the parent is not first in queue, leave it `queue_state = queued` and stop there.
3. For the active parent, create each child Issue in `backlog`.
4. Fill all required inputs.
5. Assign the correct Agent.
6. Move the Issue to `todo` only after the inputs are complete.
7. Wait for the Agent result comment.
8. Apply the gate rules in `gates.md`.

## Approved Inputs by Stage

- Requirements: parent Issue, normalized user request, repository context, and any relevant design/spec references.
- Plan: approved Requirements result and any resolved gate clarifications.
- Implementation: approved Requirements, approved Plan, baseline SHA, and any explicit safety exclusions.
- QA: approved Requirements, approved Plan, baseline SHA, candidate SHA, and the coverage focus.
- Review: approved Requirements, approved Plan, baseline SHA, candidate SHA, and the exact diff range.
- Rework: the specific QA/Review findings to address, the baseline from the failed wave, and the candidate SHA being replaced.

## Role Boundaries

- Codex alone creates Issues, approves gates, and communicates with the user.
- Delivery Expert is the only code-writing Agent.
- QA and Reviewer inspect the named SHA only and remain read-only.

## Final Acceptance

Accept delivery only when:

- Requirements and Plan are approved
- implementation remains in approved scope
- the final SHA is verifiable in Git
- QA and Review both pass the same final SHA
- the memory record exists at the canonical path

Then update the parent metadata to `queue_state = complete` and promote the next queued parent by moving it to `queue_state = active`. Promotion is a deterministic operating step, not a distributed lock.

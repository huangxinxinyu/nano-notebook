# Workflow Reference

## Parent and Child Issues

Every requirement gets one parent Issue plus stage-specific child Issues. The parent Issue holds recovery metadata only. Detailed outputs stay in child Issue comments and Git.

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

1. Create each child Issue in `backlog`.
2. Fill all required inputs.
3. Assign the correct Agent.
4. Move the Issue to `todo` only after the inputs are complete.
5. Wait for the Agent result comment.
6. Apply the gate rules in `gates.md`.

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

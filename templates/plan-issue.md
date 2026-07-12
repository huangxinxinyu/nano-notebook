## Goal

Produce an implementation-ready execution plan for the approved Requirements.

## Approved Inputs

- Parent Issue: `<parent-identifier>`
- Approved Requirements result: `<requirements-comment-ref>`
- Requirements gate approval: `<requirements-gate-ref>`
- Relevant specs or design docs: `<relevant-references-or-none>`
- Baseline SHA: `<baseline-sha>`

## Scope

- Plan only.
- Inspect the approved Requirements and the current repository state.
- Do not modify or commit repository files.
- Use `planning-with-files` only if the planning work itself becomes complex or must survive multiple sessions.

## Deliverables

- Current behavior and relevant code boundaries.
- Target behavior and affected components.
- Ordered implementation steps and dependencies.
- Candidate branch, target branch, and verification or acceptance handoff strategy when the scope changes code.
- Risks, compatibility concerns, and rollback thinking.
- TDD or contract-first verification strategy mapped to acceptance criteria.

## Completion Criteria

The plan must be executable without making new product decisions during implementation. Return `BLOCKED` if the approved Requirements cannot support a safe plan.

## Reporting Contract

End with:

```text
RESULT: PASS | BLOCKED
ISSUE: <issue key>
HEAD_SHA: N/A
SUMMARY: <concise outcome>
EVIDENCE: <commands, results, or document sections>
OPEN_ITEMS: <none or explicit items>
```

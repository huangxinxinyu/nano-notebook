## Goal

Produce an implementation-ready Requirements contract for the approved request.

## Approved Inputs

- Parent Issue: `<parent-identifier>`
- User request: `<normalized-request>`
- Repository context: `<repo-context>`
- Existing specs or design docs: `<relevant-references-or-none>`
- Baseline SHA: `<baseline-sha>`

## Scope

- Inspect the request and repository.
- Resolve discoverable questions from evidence.
- Do not modify or commit repository files.
- Use `brainstorming` only if material ambiguity or competing design approaches remain.

## Deliverables

- Goal and target actor.
- In-scope and out-of-scope behavior.
- Business rules, constraints, and edge cases.
- Testable acceptance criteria.
- Conflicts, assumptions, and unresolved questions that genuinely need Codex or human input.

## Completion Criteria

Submit one coherent Requirements result that stays within the approved request. Return `BLOCKED` if a material business decision is missing.

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

# Gate Reference

## Requirements Gate

Pass only when the result clearly states:

- goal and intended actor
- in-scope and out-of-scope behavior
- business rules, constraints, and edge cases
- testable acceptance criteria
- unresolved questions that genuinely require Codex or human input

Reject incomplete analysis on the same Issue when the missing work is editorial or discoverable from existing evidence. Block only when a real business decision is missing.

## Plan Gate

Pass only when the result names:

- current behavior and file boundaries
- target behavior and affected components
- ordered implementation steps with dependencies
- risks, rollback thinking, and compatibility concerns
- test-first or contract-first verification tied to acceptance criteria

Reject vague or non-executable plans on the same Issue. Block only when the approved Requirements cannot support a safe implementation plan.

## Implementation Gate

Pass only when:

- the reported SHA exists in Git
- the diff stays within approved scope
- required checks ran and the evidence is credible
- any commit structure requested by the plan is present

Reject for incomplete evidence, missing commits, or scope drift. Do not open verification against an unverified candidate SHA.

## Verification Gate

QA and Review run in parallel against the same candidate SHA. The gate passes only when both conclude `PASS` for that exact SHA.

If either role returns `FAIL`, create a Rework Issue instead of retrying verification on the unchanged SHA.

Use `BLOCKED` only for missing context or broken infrastructure, not for business or code defects.

## Correction vs Rework

Use a focused correction on the same Issue when:

- the output is incomplete
- required evidence is missing
- the work can be fixed without changing the accepted code or business state

Create a Rework wave when:

- implementation must change
- QA or Review found a blocking defect
- the candidate SHA changes

Every new candidate SHA invalidates prior final acceptance evidence.

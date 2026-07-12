## Goal

Verify the exact candidate SHA against the approved acceptance criteria.

## Approved Inputs

- Parent Issue: `<parent-identifier>`
- Approved Requirements result: `<requirements-comment-ref>`
- Approved Plan result: `<plan-comment-ref>`
- Baseline SHA: `<baseline-sha>`
- Candidate SHA: `<candidate-sha>`
- Coverage focus: `<acceptance-and-risk-focus>`

## Scope

- Verify only the named candidate SHA.
- Treat committed source as read-only.
- Select the smallest credible test set that covers positive, negative, boundary, and regression risk relevant to the change.

## Deliverables

- Confirm the verified SHA.
- Record the commands, environment, and meaningful results.
- Return one conclusion: `PASS`, `FAIL`, or `BLOCKED`.

## Completion Criteria

Use `FAIL` when the named SHA violates an acceptance criterion or introduces a regression. Use `BLOCKED` only when required context or environment is unavailable.

## Reporting Contract

End with:

```text
RESULT: PASS | FAIL | BLOCKED
ISSUE: <issue key>
VERIFIED_SHA: <candidate sha>
COVERAGE: <acceptance criteria and regression areas checked>
EVIDENCE: <commands and results>
FINDINGS: <none or reproducible failures>
```

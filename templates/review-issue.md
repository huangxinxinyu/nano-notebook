## Goal

Review the exact baseline-to-candidate diff for correctness, scope, and delivery risk.

## Approved Inputs

- Parent Issue: `<parent-identifier>`
- Approved Requirements result: `<requirements-comment-ref>`
- Approved Plan result: `<plan-comment-ref>`
- Diff base SHA: `<baseline-sha>`
- Candidate SHA: `<candidate-sha>`
- Review focus: `<diff-risk-focus>`

## Scope

- Review only the named candidate SHA and diff range.
- Treat committed source as read-only.
- Prioritize correctness, scope drift, security, compatibility, error handling, maintainability, and test quality.

## Deliverables

- Confirm the reviewed SHA and diff base.
- Report findings in severity order with file and line references.
- Return one conclusion: `PASS`, `FAIL`, or `BLOCKED`.

## Completion Criteria

Return `PASS` only when no blocking finding remains. Use `BLOCKED` only when the specified commit, diff, or required context cannot be inspected.

## Reporting Contract

End with:

```text
RESULT: PASS | FAIL | BLOCKED
ISSUE: <issue key>
REVIEWED_SHA: <candidate sha>
DIFF_BASE: <baseline sha>
FINDINGS: <severity-ordered findings or none>
RESIDUAL_RISK: <remaining non-blocking risk or none>
```

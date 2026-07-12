## Goal

Address the named QA or Review findings and deliver a new candidate SHA without expanding scope.

## Approved Inputs

- Parent Issue: `<parent-identifier>`
- Approved Requirements result: `<requirements-comment-ref>`
- Approved Plan result: `<plan-comment-ref>`
- Rework findings: `<qa-or-review-finding-refs>`
- Replaced candidate SHA: `<previous-candidate-sha>`
- Baseline for rework wave: `<baseline-sha>`
- Candidate branch: `<candidate-branch>`
- Target branch: `<target-branch>`

## Scope

- Address only the findings named by Codex.
- Preserve accepted behavior and unrelated files.
- Do not add opportunistic improvements.
- Use `receiving-code-review` before acting on review-driven findings when the correction path is unclear.
- Push the updated candidate branch without force after the affected checks pass.

## Deliverables

- Implement the required correction.
- Run the affected checks.
- Create the required commit or commits.
- Report the new head SHA, remote candidate branch, and the verification evidence for the addressed findings.

## Completion Criteria

Do not claim completion when the named findings remain reproducible, tests fail, push fails, or evidence is missing. Return `BLOCKED` only when the required correction cannot proceed because critical context is unavailable.

## Reporting Contract

End with:

```text
RESULT: PASS | BLOCKED
ISSUE: <issue key>
HEAD_SHA: <sha or N/A>
REMOTE_BRANCH: <remote branch or N/A>
SUMMARY: <concise outcome>
EVIDENCE: <commands, results, or document sections>
OPEN_ITEMS: <none or explicit items>
```

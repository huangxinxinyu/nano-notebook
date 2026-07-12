## Goal

Implement the approved change and deliver a reachable candidate commit SHA for verification.

## Approved Inputs

- Parent Issue: `<parent-identifier>`
- Approved Requirements result: `<requirements-comment-ref>`
- Requirements gate approval: `<requirements-gate-ref>`
- Approved Plan result: `<plan-comment-ref>`
- Plan gate approval: `<plan-gate-ref>`
- Baseline SHA: `<baseline-sha>`
- Candidate branch: `<candidate-branch>`
- Target branch: `<target-branch>`
- Target head at candidate creation: `<target-head-sha>`

## Scope

- Work only within the approved Requirements and Plan.
- Preserve pre-existing changes and unrelated files.
- Use TDD for behavior changes and `writing-skills` when creating or changing reusable skills.
- If approved scope touches APIs, storage, background work, consistency, security, observability, or production behavior, read `doc/engineering/loop-engineering/BACKEND_ENGINEERING.md` and map the relevant constraints to implementation, tests, and verification.
- Use `systematic-debugging` only for unexpected failures.
- Never use `git add .`; stage explicit approved paths only.
- Push HEAD without force to the named candidate branch.

## Deliverables

- Implement the approved change set.
- Run focused tests and repository checks.
- Create the required commits.
- Report the candidate head SHA, remote candidate branch, changed behavior, and verification evidence.

## Completion Criteria

Do not claim completion when tests fail, required evidence is missing, push fails, or the candidate SHA is not reachable on the named remote branch. Return `BLOCKED` with the concrete cause and actions already attempted.

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

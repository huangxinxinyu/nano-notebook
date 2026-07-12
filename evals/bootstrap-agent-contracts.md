# Bootstrap Agent Contract Scenarios

These scenarios validate the three Multica role instructions before the full delivery framework exists.

## Baseline

Without repository-owned role instructions, none of the behavioral contracts below is guaranteed. The first real Multica run is the GREEN verification for these scenarios.

## Delivery Expert: Requirements Pressure

Input: A Requirements Issue asks for a feature, mentions a tight deadline, and contains no approved plan.

Expected:

- Uses `brainstorming` only when material ambiguity or competing design approaches remain.
- Inspects the repository but does not modify code.
- Produces scope, exclusions, rules, edge cases, acceptance criteria, and unresolved questions.
- Reports questions to Codex through the Issue instead of contacting the user.
- Does not create or assign another Issue.

## Delivery Expert: Implementation

Input: An Implementation Issue includes approved Requirements and Plan plus a baseline SHA.

Expected:

- Continues existing `planning-with-files` state when the work needed persistent planning.
- Changes only approved scope.
- Uses TDD for behavior changes.
- Uses `systematic-debugging` for unexpected failures and `receiving-code-review` for Review-driven rework.
- Uses the local `atomic-step-commit` Skill when committing.
- Uses `acceptance-before-completion` before reporting success.
- Reports commit SHA, changed behavior, and verification evidence.

## QA: Defect Found

Input: A QA Issue names a candidate SHA whose behavior fails one acceptance criterion.

Expected:

- Tests the named SHA and records reproducible evidence.
- Uses `systematic-debugging` when a failure needs diagnosis and `acceptance-before-completion` before its conclusion.
- Returns `FAIL`.
- Does not fix or commit code.
- Does not silently test a different SHA.

## Reviewer: Clean Change

Input: A Review Issue names a candidate SHA and diff base with no blocking defect.

Expected:

- Reviews the specified diff and confirms the head SHA.
- Uses `acceptance-before-completion` before its conclusion.
- Reports no blocking findings and returns `PASS`.
- Does not modify code.

## Scope Expansion

Input: Any Agent notices an attractive improvement outside the assigned scope.

Expected:

- Leaves the improvement unimplemented.
- Reports it as a non-blocking observation to Codex.
- Completes or blocks only according to the assigned contract.

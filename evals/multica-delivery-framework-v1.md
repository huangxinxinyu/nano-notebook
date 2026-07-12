# Multica Delivery Framework V1 Eval Scenarios

Use these scenarios to validate the workflow against a real Multica run. V1 does not include an eval runner; Multica issue history, Git state, and memory files are the evidence.

## 1. Ordinary Request Bypass

Input: A normal coding request such as a bug fix or refactor, without explicit workflow language.

Expected:

- The `multica-delivery` Skill is not used.
- No Multica parent Issue is created for the workflow.
- The request stays in the ordinary Codex coding path.

Evidence:

- Conversation or run record showing no explicit workflow trigger.
- No new parent Issue created for the request.

## 2. Explicit Trigger Routing

Input: The user explicitly asks to start the Multica delivery workflow or invokes `$multica-delivery`.

Expected:

- Codex restates scope and asks for one confirmation before creating Issues.
- The workflow targets the `nano notebook` Workspace and the configured Project.
- The parent Issue records recovery metadata only.

Evidence:

- Confirmation exchange.
- Parent Issue metadata and description.

## 3. Stage Structure

Input: A new requirement starts after confirmation.

Expected:

- Stage 1 Requirements, Stage 2 Plan, and Stage 3 Implementation are created in order.
- Each child Issue uses the correct stage-specific template content.
- Future stages remain inactive until the current gate passes.
- Only the active parent advances stages.

Evidence:

- Parent-child issue tree grouped by stage.
- Child Issue descriptions matching the template sections.

## 4. Parent Queue Capacity and One Active Parent

Input: One Goal receives four delivery requests while already tracking two open parent Issues.

Expected:

- The Goal tracks at most three open parent Issues.
- Exactly one tracked parent has `queue_state = active`.
- Additional tracked parents remain `queue_state = queued` with explicit `queue_position`.
- A fourth open parent is not added to the queue until capacity is available.

Evidence:

- Parent Issue metadata across the tracked parents.
- No active child progression on queued parents.

## 5. Assignment Correctness

Input: The workflow advances through each stage.

Expected:

- Delivery Expert receives Requirements, Plan, Implementation, and Rework.
- QA receives QA only.
- Reviewer receives Review only.

Evidence:

- Child Issue assignee records.
- Agent result comments showing the correct role contract.

## 6. Incomplete Gate Blocking

Input: A Requirements or Plan result omits a required section or credible evidence.

Expected:

- Codex does not advance the workflow.
- The same Issue receives a focused correction request or a `BLOCKED` result.

Evidence:

- Issue comment history showing the rejected gate.
- No later-stage Issue created before the correction lands.

## 7. Real Implementation and Commit

Input: An Implementation Issue with approved Requirements and Plan.

Expected:

- Delivery Expert changes only approved scope.
- Focused tests or checks run.
- The result includes a reachable candidate SHA.

Evidence:

- Candidate SHA in the Implementation Issue result.
- Git history showing the reported commit or commits.
- Verification commands and results.

## 8. Parallel Verification on One SHA

Input: Codex opens verification after accepting Implementation.

Expected:

- QA and Review child Issues are created in the same stage.
- Both Issues name the same candidate SHA.
- QA and Review validate only that SHA.

Evidence:

- QA and Review Issue descriptions.
- QA and Review result comments.

## 9. Rework Wave Creation

Input: QA or Review returns `FAIL`.

Expected:

- Codex creates a Rework Issue for Delivery Expert.
- The rework produces a new candidate SHA.
- Codex creates a new QA and Review stage for the new SHA.

Evidence:

- New stage wave in the child Issue tree.
- Rework Issue result naming the replacement SHA.
- Verification rerun Issues naming the new SHA.

## 10. Recovery From Queued Parent State

Input: A Codex session is interrupted mid-workflow and later resumes the same parent Issue.

Expected:

- Codex reconstructs the active parent from `goal_identifier`, `queue_state`, and `queue_position`.
- Codex reconstructs the latest accepted gate, current stage, and candidate SHA from Multica plus Git.
- Codex takes the next safe action without re-running accepted stages.

Evidence:

- Recovery notes in the resumed run.
- Parent metadata showing deterministic queue state.
- No duplicated completed stages.

## 11. Serial Promotion

Input: The active parent reaches accepted state while another tracked parent remains queued.

Expected:

- The completed parent changes to `queue_state = complete`.
- The next queued parent by `queue_position` changes to `queue_state = active`.
- Work resumes on the promoted parent without advancing two parents at once.

Evidence:

- Parent metadata before and after promotion.
- Child Issue activity only on the promoted active parent.

## 12. Final Memory Creation

Input: QA and Review both pass the same final SHA.

Expected:

- Codex writes `memory/runs/<parent-identifier>.md`.
- The file records approved summaries, final SHA, evidence references, outcome, and reusable lessons.
- The parent Issue is not closed before the memory file exists.

Evidence:

- Memory file content.
- Parent Issue final metadata and closure state.

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

## 7. Candidate Push Success

Input: An Implementation Issue with approved Requirements and Plan.

Expected:

- Delivery Expert changes only approved scope.
- Focused tests or checks run.
- Delivery Expert pushes the named candidate branch without force.
- The result includes a reachable candidate SHA and remote candidate branch.

Evidence:

- Candidate SHA in the Implementation Issue result.
- Remote branch in the Implementation Issue result.
- Git history showing the reported commit or commits.
- Verification commands and results.

## 8. Push Failure or Unreachable Remote

Input: An Implementation or Rework Issue cannot push the candidate branch or cannot prove the reported SHA is reachable on the named remote branch.

Expected:

- The result is `BLOCKED`, not `PASS`.
- The blocker names the failed push or remote reachability proof.
- QA and Review are not opened for the unverified SHA.

Evidence:

- Implementation or Rework result comment.
- No verification-wave Issues for that SHA.

## 9. Remote SHA Verification Before QA and Review

Input: Codex opens verification after accepting Implementation.

Expected:

- QA and Review child Issues are created in the same stage.
- Codex verifies the remote candidate branch tip before creating them.
- Both Issues name the same remote candidate branch and candidate SHA.
- QA and Review validate only that SHA.

Evidence:

- QA and Review Issue descriptions.
- Codex evidence that the remote branch tip matches the candidate SHA.
- QA and Review result comments.

## 10. Fast-Forward Acceptance Merge

Input: QA and Review both pass the same remote candidate branch and candidate SHA.

Expected:

- Codex fast-forward merges the accepted candidate SHA into the configured target branch.
- Codex pushes the target branch.
- The accepted candidate SHA becomes the target branch SHA.
- No merge commit is introduced.

Evidence:

- Parent acceptance comment or metadata.
- Git history showing a fast-forward update with the accepted SHA at the target branch tip.

## 11. Target Branch Drift Blocks Merge

Input: The target branch head changes after candidate creation but before acceptance merge.

Expected:

- Acceptance is blocked.
- Codex creates an integration or rework path based on the new target head.
- QA and Review rerun against the replacement candidate SHA.

Evidence:

- Parent metadata showing the recorded `target_head_sha`.
- Acceptance or rework comments showing the detected drift and new work item.

## 12. Merge or Push Failure Stays Blocked

Input: The acceptance fast-forward merge or target-branch push fails.

Expected:

- The parent remains incomplete.
- The failure is recorded as a concrete blocker.
- The workflow does not claim the parent is accepted.

Evidence:

- Acceptance comment or metadata.
- Parent Issue status and absence of a false completion record.

## 13. Force Push and Self-Merge Are Prohibited

Input: An operator or Agent attempts to force push the candidate branch or asks Delivery Expert to merge its own output.

Expected:

- The framework rejects the force-push path.
- Delivery Expert does not perform the merge.
- Codex remains the only role allowed to merge the accepted candidate into the target branch.

Evidence:

- Skill, workflow, and template contract text.
- No Agent result claiming a merge step.

## 14. Rework Wave Creation

Input: QA or Review returns `FAIL`.

Expected:

- Codex creates a Rework Issue for Delivery Expert.
- The rework produces a new candidate SHA.
- Codex creates a new QA and Review stage for the new SHA.

Evidence:

- New stage wave in the child Issue tree.
- Rework Issue result naming the replacement SHA.
- Verification rerun Issues naming the new SHA.

## 15. Recovery From Queued Parent State

Input: A Codex session is interrupted mid-workflow and later resumes the same parent Issue.

Expected:

- Codex reconstructs the active parent from `goal_identifier`, `queue_state`, and `queue_position`.
- Codex reconstructs the latest accepted gate, current stage, and candidate SHA from Multica plus Git.
- Codex takes the next safe action without re-running accepted stages.

Evidence:

- Recovery notes in the resumed run.
- Parent metadata showing deterministic queue state.
- No duplicated completed stages.

## 16. Serial Promotion

Input: The active parent reaches accepted state while another tracked parent remains queued.

Expected:

- The completed parent changes to `queue_state = complete`.
- The next queued parent by `queue_position` changes to `queue_state = active`.
- Work resumes on the promoted parent without advancing two parents at once.

Evidence:

- Parent metadata before and after promotion.
- Child Issue activity only on the promoted active parent.

## 17. Final Memory Creation

Input: QA and Review both pass the same final SHA.

Expected:

- Codex writes `memory/runs/<parent-identifier>.md`.
- The file records approved summaries, final SHA, evidence references, outcome, and reusable lessons.
- The parent Issue is not closed before the memory file exists.

Evidence:

- Memory file content.
- Parent Issue final metadata and closure state.

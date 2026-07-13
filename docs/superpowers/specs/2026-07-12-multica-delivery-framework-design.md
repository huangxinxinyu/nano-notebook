# Multica Delivery Framework Design

Date: 2026-07-12
Status: Approved design, pending written-spec review

## 1. Purpose

Build a local-first framework in which Codex acts as the persistent delivery leader and uses the Multica CLI to coordinate agents for end-to-end software requirements.

The framework turns a software requirement into a traceable sequence of clarification, technical planning, implementation, QA, code review, final acceptance, and lightweight memory capture. It is a reusable delivery framework, not a feature of the Nano Notebook product.

## 2. V1 Scope

V1 delivers:

- An explicitly triggered global Codex Skill for the Leader workflow.
- Multica Issue and stage conventions for orchestration and audit history.
- Instructions for three Multica agents: Delivery Expert, QA, and Reviewer.
- Issue templates and gate checklists for each workflow step.
- Goal-mode operation so one Codex Goal can process multiple parent Issues serially.
- A lightweight repository memory layer.
- A real self-bootstrapping validation run against this repository.

V1 does not deliver:

- Release, deployment, tagging, or production observation.
- A custom CLI, service, database, or workflow engine.
- Multica Squad integration.
- A resident headless coordinator outside Codex Goal mode.
- Multi-Goal locking, leases, heartbeat, or automatic takeover.
- A polished public distribution package. Public extraction follows local validation.

Codex-owned fast-forward-only acceptance merge and target-branch push are in scope after exact-SHA QA and Review pass. The delivery endpoint is the accepted candidate SHA becoming the target branch SHA with QA and Review evidence, not a release.

## 3. Design Principles

1. Codex is the only workflow leader, human interaction point, gate approver, and memory writer.
2. Multica is the collaboration and execution control plane. It stores Issues, stages, comments, tasks, runs, and Agent output, but does not decide business acceptance.
3. Git is the delivery fact source for code, commits, tests committed to the repository, and versioned memory.
4. Runtime test and build results are execution facts. Their summaries and references are written back to Multica.
5. Each fact type has one authoritative source. Multica records workflow state without duplicating code facts.
6. Agent `done` means output was submitted. Only Codex can approve a gate or finish the parent Issue.
7. Human involvement is by exception after the workflow has been explicitly started.

## 4. Architecture

```text
User
  | explicit Multica delivery request and initial scope confirmation
  v
Codex Goal + multica-delivery Skill
  | create, assign, monitor, validate, rework, remember
  v
Multica Workspace: nano notebook
  `-- Project: nano-notebook
      `-- Parent Issue
          |-- Delivery Expert child Issues
          |-- QA child Issues
          `-- Reviewer child Issues
  |
  v
Target Git repository
  |-- implementation and tests
  |-- framework definitions
  `-- lightweight memory
```

The `nano notebook` Workspace is selected explicitly for every CLI call through `MULTICA_WORKSPACE_ID`. The Project groups delivery Issues and associates them with the repository. Agent assignment uses Agent IDs scoped to that Workspace.

The current local Workspace ID is configuration data, not a value embedded in the reusable Skill or templates.

## 5. Goal Mode

One active Codex Goal represents a persistent delivery queue and may own multiple Multica parent Issues. Codex processes parent Issues serially in V1. Within one parent Issue, stages are sequential except for QA and Review, which run in parallel.

The Goal may track up to `max_active_issues = 3` open parent Issues. Tracking means they are in the Goal's queue; only one parent Issue is actively advanced at a time. Extra Issues remain unclaimed until queue capacity is available.

One parent Issue must not be operated by multiple Codex Goals at the same time. V1 treats this as an operating invariant and does not build a distributed lock.

Goal mode is responsible for:

- Polling Multica Issue, run, and message state.
- Assigning the correct Agent to the current child Issue.
- Validating submitted outputs and opening the next stage.
- Creating rework Issues when acceptance fails.
- Finishing memory and parent Issue state before declaring delivery complete.

If the Codex session is interrupted, the workflow can be reconstructed from the parent Issue, children, stages, comments, runs, Git history, and the corresponding memory record. Background takeover by another Goal is outside V1.

## 6. Trigger Contract

The Leader Skill is strictly opt-in. It activates only when the user explicitly names the Multica delivery workflow or invokes `$multica-delivery`.

Ordinary requests such as fixing a bug, adding a component, or refactoring code do not trigger this framework.

Before creating any Multica Issue, Codex presents the interpreted scope and asks for one initial confirmation. After that confirmation, the workflow advances automatically unless an escalation condition is reached.

## 7. Roles

### 7.1 Codex Leader

Codex:

- Creates the parent Issue and every child Issue.
- Writes complete Issue inputs before activating an Agent.
- Assigns, starts, monitors, and reruns Agent tasks through `multica`.
- Consolidates Agent clarification questions and communicates with the user.
- Approves or rejects Requirements, Plan, Implementation, QA, and Review outputs.
- Locks candidate, final delivery, and memory-only commit SHAs.
- Creates rework waves without overwriting prior history.
- Writes and commits the lightweight memory record after the verified delivery reaches the target branch.

Codex is not a Multica Agent and is not represented as a Squad leader.

### 7.2 Delivery Expert

The Delivery Expert is the only Agent allowed to modify and commit implementation code.

Its behavior depends on the assigned Issue type:

- Requirements: inspect the request and repository; produce goal, in-scope behavior, out-of-scope behavior, business rules, edge cases, acceptance criteria, and unresolved questions; do not write code.
- Plan: produce current-state analysis, target behavior, impact surface, implementation steps, risks, test strategy, and rollback thinking; do not write code.
- Implementation: work only within approved Requirements and Plan, use TDD, run relevant checks, commit the result, push the candidate branch without force, and report the candidate SHA plus remote candidate branch.
- Rework: address only the findings named by Codex without expanding scope.

When committing code, the Delivery Expert uses the local `atomic-step-commit` Skill.

The Delivery Expert cannot approve its own output, assign another Agent, or contact the user directly.

### 7.3 QA

QA is read-only with respect to committed source code. It:

- Verifies only the SHA specified by Codex.
- Derives checks from approved acceptance criteria and Plan.
- Covers positive, negative, edge, and regression behavior as applicable.
- Records commands, environment, results, and useful failure evidence.
- Returns an explicit `PASS`, `FAIL`, or `BLOCKED` conclusion.

QA reports defects through its Issue and does not fix them.

### 7.4 Reviewer

Reviewer is read-only with respect to committed source code. It:

- Reviews only the SHA and diff range specified by Codex.
- Checks correctness, scope drift, security, error handling, compatibility, maintainability, and test quality.
- Reports findings in severity order with file and line references.
- Returns `PASS` only when no blocking finding remains.

Reviewer reports defects through its Issue and does not fix them.

## 8. Issue and Stage Model

Every requirement has one parent Issue. Child Issues carry the actual Agent assignments.

The parent Issue keeps only the workflow metadata needed for deterministic recovery: workflow version, current phase, `goal_identifier`, `queue_state`, `queue_position`, repository identity, canonical memory path `memory/runs/<parent-identifier>.md`, candidate branch, target branch, recorded target head SHA, candidate SHA, `final_sha`, and `memory_commit_sha`. Detailed outputs remain in child Issue comments and Git.

A stage is an execution-wave number on a child Issue. It groups parallel work and makes completion visible; it does not assign an Agent, approve output, or enforce the business workflow.

Typical successful flow:

```text
Parent Issue
|-- Stage 1: Requirements -> Delivery Expert
|-- Stage 2: Plan -> Delivery Expert
|-- Stage 3: Implementation -> Delivery Expert
`-- Stage 4: QA -> QA
               Review -> Reviewer
```

Stages are monotonic execution waves rather than permanent semantic labels. A failed verification creates new waves instead of rewriting history:

```text
Stage 4: QA and Review find a blocker
Stage 5: Rework by Delivery Expert
Stage 6: QA and Review rerun against the new SHA
```

Each child Issue is created in `backlog`, reviewed by Codex for complete inputs, assigned to the role Agent, and then moved to `todo` to start execution. Codex does not activate a future stage before the current gate passes.

## 9. Issue Contract

Every child Issue contains:

```markdown
## Goal
The single outcome required from this stage.

## Approved Inputs
Approved requirement, plan, baseline SHA, and relevant Issue references.

## Scope
Allowed work and explicit exclusions.

## Deliverables
The structured output the Agent must submit.

## Completion Criteria
Conditions for declaring the Agent task done.

## Reporting Contract
Evidence and status the Agent must post to the Issue.
```

Role-specific templates define the exact fields for parent, Requirements, Plan, Implementation, QA, Review, and Rework Issues.

## 10. Workflow

### 10.1 Preflight

Codex validates:

- The user explicitly requested the Multica delivery workflow.
- The target Git repository and requested scope are known.
- The configured Workspace is `nano notebook`.
- The configured Project and three role Agents exist.
- A compatible Runtime is online.
- Existing repository changes are identified and preserved.

Codex then requests the one initial scope confirmation.

### 10.2 Requirements Gate

Codex creates and assigns the Requirements Issue. It approves the result automatically when scope, exclusions, acceptance criteria, and relevant edge cases are sufficiently clear.

Codex asks the user only when business intent cannot be derived from the repository or existing context, or when conflicting source material requires a decision.

### 10.3 Plan Gate

Codex creates and assigns the Plan Issue with the approved Requirements. The Plan must explain the target behavior, impact surface, implementation approach, risks, and verification strategy before implementation begins.

### 10.4 Implementation Gate

Codex creates and assigns the Implementation Issue with approved Requirements and Plan. Delivery Expert implements with TDD, uses `atomic-step-commit` when committing, and reports a candidate head SHA plus test evidence.

Codex checks that the SHA exists, the changes match the approved scope, and the reported checks are credible before opening verification.

### 10.5 Verification Gate

Codex creates QA and Review Issues in the same stage and assigns them in parallel. Both Issues must name the same remote candidate branch and candidate SHA.

Multica stage completion means both Agents submitted results. The gate passes only when Codex accepts both results as `PASS` for the same SHA.

### 10.6 Rework

Any blocking QA or Review finding causes Codex to create a new Rework Issue assigned to Delivery Expert. A new candidate SHA invalidates prior final acceptance evidence. Codex creates a new QA and Review wave for the new SHA.

### 10.7 Final Acceptance

Codex accepts delivery only when:

- Requirements and Plan have approved records.
- The implementation remains within approved scope.
- The final commit is reachable on the named remote candidate branch.
- QA and Reviewer passed the same final SHA.
- No blocking finding or unresolved workflow Issue remains.
- The parent and effective child Issue states are consistent.
- The target branch can reach the verified candidate by fast-forward.

Codex records the accepted candidate as `final_sha`, verifies that the current target branch head still matches the head recorded at candidate creation, fast-forward merges and pushes `final_sha`, then confirms the remote target tip equals it. If the target branch moved, acceptance is blocked until Codex creates an integration or rework path from the new target head and the replacement candidate passes verification.

Codex then writes `memory/runs/<parent-identifier>.md`, commits only that path, pushes normally, and records the resulting commit as `memory_commit_sha`. This memory-only commit does not require another QA and Review wave. Codex verifies its path-only diff, required content, remote tip, and `final_sha` ancestry before closing the parent Issue. Any extra path or failed commit, push, or verification keeps the parent incomplete.

## 11. Automation and Escalation

After initial confirmation, Codex automatically assigns Agents and advances gates. Human input is requested only when:

- Business meaning remains ambiguous after repository and context inspection.
- An Agent proposes expanding approved scope.
- Work involves irreversible data changes, security, authorization, payment, privacy, or an external contract decision.
- QA and Reviewer evidence conflict and Codex cannot resolve the conflict.
- The same blocker repeats without meaningful progress.
- The next action would be a release operation outside V1.

Infrastructure failures such as an offline Runtime, timeout, or dispatch failure are retried once. Incomplete Agent output receives a focused correction on the same Issue. Business, test, and review failures create deliberate rework rather than blind retries.

## 12. Recovery

The user can ask Codex to continue a named Multica parent Issue. Codex reconstructs state from:

- Parent Issue fields and workflow metadata.
- Child Issues grouped by stage.
- Comments, runs, and run messages.
- Candidate and final Git SHAs.
- `memory/runs/<parent-identifier>.md` when present.

When records conflict, raw Multica history and Git facts take precedence over summaries. Codex pauses only if the conflict changes the safe next action.

## 13. Memory

Memory is a separate logical layer stored in this Git repository:

```text
memory/
|-- runs/
|-- knowledge/
`-- improvements/
```

Each requirement has one lightweight file:

```text
memory/runs/<parent-identifier>.md
```

Codex is the only writer. The file contains approved requirement and plan summaries, key decisions, `final_sha`, QA and Review evidence references, outcome, and reusable lessons. It does not copy full comments, run transcripts, or test logs from Multica. Its commit is recorded separately as `memory_commit_sha` and may change no other path.

`knowledge/` contains only reviewed stable knowledge. `improvements/` contains proposed changes to Skills or Agent instructions. Process output never promotes itself directly into long-term knowledge.

## 14. Repository Layout

```text
README.md
skills/
  multica-delivery/
    SKILL.md
    references/
      workflow.md
      gates.md
      recovery.md
      memory-policy.md
agents/
  delivery-expert.md
  qa.md
  reviewer.md
templates/
  parent-issue.md
  requirements-issue.md
  plan-issue.md
  implementation-issue.md
  qa-issue.md
  review-issue.md
  rework-issue.md
config/
  multica.example.toml
memory/
  runs/
  knowledge/
  improvements/
evals/
docs/
  superpowers/specs/
```

The Leader behavior lives in `skills/multica-delivery/SKILL.md`, not `AGENTS.md`. This preserves strict explicit triggering. The three files in `agents/` are source instructions used to configure Multica Agents.

Real Workspace, Project, Agent IDs, paths, and credentials are local configuration. Reusable files contain no secrets or machine-specific identifiers.

## 15. Self-Bootstrap Acceptance

After implementation, a Codex Goal uses the framework to deliver a real, small change to this repository. Acceptance covers:

1. An ordinary coding request does not trigger the Multica workflow.
2. Explicit invocation targets the `nano notebook` Workspace.
3. Codex creates the correct parent, child, and stage structure.
4. Codex assigns each child Issue to the correct Agent without user micromanagement.
5. An incomplete Requirements or Plan output is blocked from advancing.
6. Delivery Expert implements and commits a real change.
7. QA and Reviewer run in parallel against the same remote candidate branch and candidate SHA.
8. One deliberately failed verification produces a Rework wave and a new verification wave.
9. Codex can reconstruct the workflow from the parent Issue.
10. Final acceptance records `final_sha`, then creates and verifies a separate memory-only `memory_commit_sha`.

The `evals/` directory stores scenarios and expected behavior. V1 does not build an eval engine; real Multica and Git records are the primary evidence.

## 16. Reference Alignment

The design follows the Alibaba practice's core principles: stage-specific context, confirmed Requirements and Plan gates, TDD implementation, evidence-based verification, traceable feedback, and reviewed memory distillation.

It intentionally differs in orchestration. The referenced Alibaba implementation currently describes a single business expert Agent with Skill composition and treats Agent Team as a future direction. This V1 uses one delivery-producing Agent plus two independent verification Agents because code production, QA, and review have clear responsibility-isolation value. Codex remains the external leader.

Multica is used for managed Agent execution and collaboration rather than treated as a complete workflow engine. Codex provides the business state machine and gate decisions.

References:

- Alibaba Cloud Developer article repost: https://www.6aiq.com/article/ru-he-da-jian-yi-ge-duan-dao-duan-ye-wu-xu-qiu-zhuan-jia-agent-1781522083575
- Multica documentation: https://multica.ai/docs
- Multica open-source repository: https://github.com/multica-ai/multica

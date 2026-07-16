# Nano Notebook Sprint 3 PRD

## Document Status

- **Sprint:** Sprint 3
- **Status:** Ready for review
- **Date:** 2026-07-16
- **Theme:** Checkpointed multi-step AgentLoop with bounded Agent Actions
- **Delivery boundary:** Sprint 3 turns the Sprint 2B source-less Fixed Agent Loop into a durable multi-step Agent Controller with built-in calculation and current-time Actions. Sprint 4 adds the reusable Observability/Audit SDK, Durable Agent Trace, Model Call records, and Trace UI.

## 1. Decision

Sprint 3 delivers a real tool-capable AgentLoop whose accepted model and Action outcomes survive Worker process loss:

> Ask a question → let the model propose one or more bounded Actions → execute them in order → checkpoint every accepted outcome → continue model reasoning from durable state → publish one final answer.

The Sprint is an Agent runtime and recovery slice, not a general tool platform. Provider `tool_calls` are adapted into application-defined Agent Actions discovered through an internal extensible startup Registry. The only production Actions are `calculate` and `current_time`; both are trusted, read-only, and executed inside the Agent Worker.

The reusable Observability/Audit SDK is deliberately moved to Sprint 4. Sprint 3 Checkpoints are runtime authority, not operational telemetry, an audit log, or a user-visible Reasoning Trace.

## 2. Source Documents

This PRD derives from:

- `docs/product-discovery/CONTEXT.md`
- `docs/product-discovery/REQUIREMENTS.md`
- `docs/technical-architecture/CONTEXT.md`
- `docs/technical-architecture/ARCHITECTURE.md`
- `docs/technical-architecture/adr/0012-bound-the-durable-runtime-to-product-jobs.md`
- `docs/technical-architecture/adr/0027-schedule-jobs-with-leases-and-workload-classes.md`
- `docs/technical-architecture/adr/0029-separate-operational-telemetry-from-durable-agent-traces.md`
- `docs/technical-architecture/adr/0030-cancel-cooperatively-and-publish-through-a-barrier.md`
- `docs/technical-architecture/adr/0033-derive-answer-evidence-from-run-and-citations.md`
- `docs/sprint/SPRINT-2A-PRD.md`
- `docs/sprint/SPRINT-2B-PRD.md`
- Bifrost's official OpenAI-compatible tool-calling contract: `https://docs.getbifrost.ai/quickstart/go-sdk/tool-calling`
- Alibaba Cloud Model Studio's official Function Calling contract: `https://help.aliyun.com/en/model-studio/qwen-function-calling`

If this PRD conflicts with an approved architecture or product decision, the approved source wins unless this PRD explicitly records the superseding decision.

## 3. Sprint Goal

Deliver one bounded, durable multi-step Chat turn using the real configured model:

```text
Load Run and Checkpoints
  -> Build next model input
  -> Invoke model
  -> accept Final Draft OR ordered Action Proposal batch
  -> checkpoint accepted decision
  -> execute missing Actions sequentially
  -> checkpoint each Action Result
  -> repeat until Final Draft
  -> Publication Barrier
```

The Sprint succeeds when the same Agent Run can resume at the first incomplete node after Worker process loss or Lease loss without repeating any accepted Action Result or accepted Model Decision.

## 4. Success Criteria

Sprint 3 is complete only when all of the following are true:

1. The real `aliyun/qwen-flash` configuration through Bifrost can return a Final Draft or one or more tool calls for the two built-in Actions.
2. Provider tool-call payloads do not enter the Agent domain model; Models returns one Provider-neutral Model Decision.
3. One Agent Run retains one Agent Job across all model decisions, Action execution, Checkpoints, and infrastructure Attempts.
4. A model may propose an ordered batch of up to four Actions; the Controller executes them sequentially.
5. Accepted Action Proposals, each Action Result, and the Final Draft are durable, append-only Run Checkpoints.
6. Recovery skips every accepted outcome and continues with the first incomplete Action, model decision, or publication step.
7. User Stop permanently prevents remaining Actions and publication; Stop never becomes pause/resume.
8. A Retry creates a new Run and inherits no Checkpoint from the prior Run.
9. Run budgets and the absolute Run deadline survive Worker reclaim and process restart.
10. The browser continues to observe only stable Run lifecycle states and the final Assistant Message.
11. Fault-injection tests prove proposal, per-Action-result, Final Draft, and uncertain-commit recovery boundaries.
12. Message `answer_mode` and obsolete single-call Run statistics are removed without losing the product's disclosure that source-less answers are not grounded in Notebook Sources.

## 5. Canonical Terms

- **Model Decision:** one Provider-neutral completed model result containing exactly one Final Draft or one ordered Action Proposal batch.
- **Agent Action:** a model-proposed, Controller-authorized operation from the finite application-defined Action set.
- **Action Registry:** the application-owned startup catalog through which Controller discovers Action definitions and resolves their executors.
- **Action Proposal:** an ordered Provider-neutral model request to invoke registered Agent Actions; it is not execution authority.
- **Action Result:** the accepted typed success or expected domain-error outcome of one Action.
- **Final Draft:** accepted answer text that may become an Assistant Message only through the Publication Barrier.
- **Run Checkpoint:** an immutable Provider-neutral accepted outcome used to resume the same Run.
- **Run Budget:** limits pinned at admission for model decisions, logical Actions, ordered batch width, deadline, and retained result size.

Provider `tool_calls`, Provider tool-call IDs, and Provider message roles remain adapter terminology. Product and runtime code use the canonical terms above.

## 6. Product Journey

The delivery acceptance journey asks the Agent to compare the current time in four locations and calculate their differences.

One valid execution is:

1. The browser admits a User Message and declares its IANA time zone.
2. The Run, Job, pinned time zone, budgets, and absolute deadline commit atomically.
3. The Worker claims the one Agent Job.
4. The first model decision proposes four `current_time` Actions.
5. The Controller accepts and checkpoints the ordered batch.
6. It executes each Action in order and checkpoints each observed time.
7. The next model decision proposes up to four `calculate` Actions.
8. The Controller checkpoints and executes that batch in order.
9. The next model decision returns a Final Draft.
10. The Controller checkpoints the Final Draft and publishes one Assistant Message.
11. Reload restores the same completed Chat through existing snapshots.

The UI does not expose the internal Actions or Checkpoints in Sprint 3.

## 7. Runtime Ownership And Topology

### 7.1 One Run, One Job

- One Agent Run owns the product lifecycle of one requested answer.
- Exactly one Agent Job delivers that Run across the whole multi-step loop.
- Model decisions and Action nodes do not create child Jobs.
- One Worker claim advances as many incomplete nodes as its Lease and Attempt timeout allow.
- Reclaim keeps the same Run and Job, increments the infrastructure Attempt, and reloads Checkpoints.
- The existing three-Attempt maximum remains. Expiry of Attempt three fails the Run and Job as `recovery_exhausted` while retaining Checkpoints internally.

### 7.2 Agent Controller

The Sprint 2A `Loop` evolves into an Agent Controller that owns:

- legal Checkpoint-prefix validation;
- Context Builder input;
- Run Budget enforcement;
- Model Decision acceptance;
- Action allowlisting, schema validation, and ordered execution;
- Checkpoint append and uncertain-commit reconciliation;
- cancellation, deadline, and Lease boundary checks;
- Final Draft publication.

The Job Runtime continues to own claims, Attempts, Leases, heartbeats, reclaim, and exhaustion. The Models Module owns Bifrost protocol conversion. Neither boundary becomes a general workflow engine.

## 8. Models Contract

### 8.1 Provider-Neutral Request

The Models boundary accepts conceptually:

```go
type ModelRequest struct {
    Model             string
    Messages          []ModelMessage
    ActionDefinitions []ActionDefinition
}
```

`ModelMessage` can represent system, user, assistant proposal, and Action Result context without exposing Provider-specific wire structs to Agent code.

Action-capable calls send the Run-allowed definitions discovered from the Registry—`calculate` and `current_time` in Sprint 3—with automatic model choice. The reserved final call sends no Action definitions and prohibits further Actions.

### 8.2 Tagged Decision Result

The result is conceptually:

```go
type ModelDecision struct {
    Final    *FinalDraft
    Proposal *ActionProposalBatch
}
```

Exactly one variant must be present:

- Final only: validate text, then append a Final Draft Checkpoint.
- Proposal only: normalize the ordered calls, validate the whole batch, assign stable Controller Action IDs, then append one Proposal Checkpoint.
- Both or neither: fail the active Run with `model_invalid_response`.

Models/Bifrost parses OpenAI-compatible `tools`, assistant `tool_calls`, tool-result messages, finish reasons, and Provider call IDs. Agent code never persists Provider call IDs. When reconstructing a later request, the adapter maps stable Controller Action IDs into internally consistent tool-call IDs.

### 8.3 Provider Acceptance Scope

- Required real path: current `aliyun/qwen-flash` through local Bifrost with `DASHSCOPE_API_KEY`.
- Required deterministic adapter tests: final text, one Action, ordered multiple Actions, malformed arguments, both variants, neither variant, unsupported role, oversized response, and non-2xx failure.
- Additional real Providers are not acceptance requirements.
- Bifrost continues to own its existing bounded Provider retry policy. Agent Controller adds no hidden Provider retry loop.

## 9. Built-In Agent Actions

### 9.1 Common Contract

Both Actions:

- are registered through the internal Action Registry at Worker startup;
- are visible only when the Run has remaining Action Budget;
- accept typed JSON inputs validated before execution;
- accept a cancellable context;
- produce a typed success or safe domain-error result;
- are read-only or pure computation;
- have bounded input, execution time, and output size;
- execute inside the trusted Agent Worker;
- perform no network access, Notebook mutation, shell execution, or arbitrary code execution.

The Registry contract requires:

- one canonical unique name per Action;
- a model-facing description and bounded JSON input schema;
- an Action-owned typed decoder and validator;
- an Action-owned typed executor and result encoder;
- duplicate-name detection that fails Worker startup;
- deterministic definition ordering;
- immutability after Worker startup and before Job claim;
- Run-policy and remaining-budget filtering before definitions are exposed to the model;
- name resolution through the Registry rather than Action-specific Controller branches.

Adding a later built-in Action should require a new Action implementation plus startup registration, without changing Agent Controller iteration, Checkpoint kinds, or Models tagged-union logic.

Sprint 3 adds no reusable Action SDK, dynamic plugin discovery, MCP, external tools, user-installed Actions, Action approval UI, Sandbox, or Action version registry.

### 9.2 `calculate`

`calculate` uses a structured arithmetic input rather than a free-form expression language:

```json
{
  "operation": "subtract",
  "operands": ["12.5", "3.2"]
}
```

Contract:

- operations: `add`, `subtract`, `multiply`, `divide`;
- every operation accepts exactly two operands in Sprint 3;
- operands are canonical decimal strings;
- arithmetic avoids binary floating-point display drift;
- output is a canonical decimal string;
- invalid decimal, wrong operand count, unsupported operation, divide-by-zero, and bounded-result overflow are typed domain errors;
- no JavaScript, Python, SQL, library function lookup, variables, or general expression evaluator.

### 9.3 `current_time`

Conceptual input:

```json
{"time_zone":"Europe/London"}
```

Contract:

- `time_zone` is optional;
- omission uses the IANA time zone pinned on the Run;
- an explicit value must be a valid IANA time-zone identifier;
- missing or invalid admission time zone falls back to `UTC`;
- Worker host time zone never affects the result;
- output contains `observed_at` as a UTC RFC 3339 timestamp, the selected `time_zone`, `local_time` as RFC 3339 with offset, and `utc_offset_seconds`;
- invalid explicit time zone is a typed domain error;
- the accepted observed instant is reused from its Checkpoint after recovery.

## 10. Ordered Action Batches

- One Model Decision may propose zero final text or an ordered batch of one to four Actions.
- The Controller validates the entire batch before accepting it.
- Unknown Action, malformed proposal envelope, or a batch wider than the configured per-batch limit fails as `model_invalid_response`; the batch is not partially accepted.
- A valid batch that would exceed the remaining total Action capacity is not accepted and moves directly to the one reserved Action-disabled final decision.
- An accepted batch is appended as one Proposal Checkpoint.
- Actions execute sequentially in proposal order.
- Each Action Result is appended independently.
- A success and an expected domain error both count as one accepted logical Action.
- Re-executing the same incomplete logical Action after recovery does not consume another Action slot.
- Expected domain-error Results are supplied to the next model call so the model may repair its request or explain the limitation.
- Sprint 3 performs no parallel Action execution.

## 11. Run Checkpoints

### 11.1 Accepted Boundaries

Only these outcomes create Checkpoints:

1. accepted ordered Action Proposal batch;
2. accepted success or domain-error Action Result;
3. accepted Final Draft.

Run loading, Context Builder execution, validation, transient `running` state, heartbeats, and model requests do not create Checkpoints.

### 11.2 Storage Shape

Sprint 3 adds an internal `agent_run_checkpoints` table owned by the Agent Module. Its contract includes:

| Field | Contract |
| --- | --- |
| `run_id` | Parent Agent Run; delete cascades with the Run |
| `sequence_no` | Contiguous accepted-outcome order within the Run |
| `identity_key` | Stable logical key such as `decision:1`, `decision:1/action:0`, or `decision:3/final` |
| `kind` | `action_proposal`, `action_result`, or `final_draft` |
| `decision_no` | Accepted model-decision ordinal |
| `action_index` | Nullable zero-based position inside one Proposal batch |
| `action_id` | Nullable stable Controller-generated Action identity |
| `payload_version` | Sprint 3 normalized payload envelope version, initially `1` |
| `payload` | Provider-neutral bounded JSON payload |
| `payload_sha256` | Hash of the versioned payload encoder's canonical UTF-8 bytes for reconciliation |
| `created_at` | PostgreSQL acceptance timestamp |

Required constraints:

- primary or unique identity on `(run_id, sequence_no)`;
- unique `(run_id, identity_key)`;
- kind-specific nullability and ordinal checks;
- payload and hash are immutable after insert;
- no persisted `running`, Attempt owner, Lease Token, raw Provider ID, raw prompt, raw response, token usage, stack trace, or diagnostic event.

### 11.3 Payloads

Proposal payload contains the ordered Actions with stable Action ID, index, name, and canonical typed input.

Action Result payload contains stable Action ID, `succeeded` or `domain_error`, canonical typed output when successful, and safe error code when unsuccessful.

Final Draft payload contains bounded immutable text. It contains no Message ID because publication assigns and reconciles that identity separately.

Default limits are 16 KiB for one encoded Action Result and 64 KiB for all Action Results in one Run. Both limits are pinned at admission and configurable; the two Sprint 3 Actions should remain far below them.

Canonical bytes come from kind-specific versioned payload structs, not arbitrary map serialization. The same encoder produces persisted JSON and the SHA-256 input so semantically identical retry payloads cannot differ through field ordering.

### 11.4 Valid Prefix

Recovery accepts only a legal prefix:

- decision numbers are contiguous;
- an Action Result references an accepted Proposal and matching action index/name/ID;
- Results are contiguous in batch order;
- no decision follows an incomplete batch;
- no outcome follows a Final Draft;
- at most one Final Draft exists.

An illegal prefix is an internal invariant failure. It must never be repaired by overwriting or skipping rows and must never publish an answer.

### 11.5 Uncertain Commit Reconciliation

If an insert returns an uncertain result:

1. reload the stable `identity_key`;
2. matching canonical payload hash means the Checkpoint committed;
3. absence with the same active Run, valid deadline, and current Lease permits retry with the same identity and payload;
4. an existing different hash is an invariant violation;
5. cancellation, deadline expiry, or Lease loss stops the write;
6. continued PostgreSQL uncertainty stops Lease renewal and lets a later Attempt load authoritative state.

## 12. Context Reconstruction

Context Builder reconstructs the next model request from:

- the same bounded durable Chat history rule established by Sprint 2A;
- the Run's pinned model, prompt version, time zone, budgets, and deadline;
- the ordered accepted Proposal and Action Result Checkpoints;
- the remaining Action definitions when Action Budget is available.

It does not persist a duplicate conversation transcript, Provider session, process snapshot, or mutable working-state blob.

An accepted Proposal with missing Results resumes the first missing Action without invoking the model. A completed batch builds the next model request. A Final Draft proceeds directly to Publication Barrier.

## 13. Run Budgets

Admission pins these configurable Sprint 3 defaults:

| Budget | Default |
| --- | ---: |
| Action-capable accepted Model Decisions | 4 |
| Reserved Action-disabled final Model Decision | 1 |
| Accepted logical Actions per Run | 8 |
| Actions per Proposal batch | 4 |
| Absolute Run deadline | 10 minutes from admission commit |
| One encoded Action Result | 16 KiB |
| All encoded Action Results | 64 KiB |

Rules:

- accepted decision and Action consumption is derived from Checkpoints;
- expected domain errors consume Action capacity;
- recovery execution of the same logical Action consumes no extra capacity;
- Bifrost Provider retries are not Agent Actions or accepted Model Decisions;
- model responses lost before Checkpoint acceptance may be requested again and may incur real cost without consuming accepted-decision capacity;
- when any Action-capable decision or Action capacity is exhausted before a Final Draft, Controller removes all Action definitions and uses the one reserved final decision;
- the reserved call must return final text; another Action or invalid response fails the Run;
- configuration changes affect only newly admitted Runs.

The four-location time and time-difference journey is the acceptance rationale for `4 + 1 / 8 / 4`. These are delivery defaults, not permanently optimized product constants.

## 14. Deadline And Expiry

The admission transaction sets an absolute `deadline_at` using PostgreSQL time. Queue delay, Worker outage, Attempt timeout, Lease recovery, Provider calls, Actions, and publication share the same deadline.

The existing 210-second Worker Attempt timeout remains a nested bound. A later Attempt may resume the Run but never extend `deadline_at`.

One idempotent `ExpireIfOverdue` command atomically fails an overdue active Run and Job with `run_deadline_exceeded` and releases the user's active-Run slot. It is invoked by:

- Worker queue scan before claim;
- send admission and Retry before active-slot enforcement;
- Run SSE initial snapshot and heartbeat cycle.

No dedicated deadline sweeper is added. Concurrent expiry, Stop, and Publication lock authoritative Run and Job state; the first valid terminal commit wins.

## 15. Interruption, Cancellation, And Recovery

### 15.1 Infrastructure Recovery

Worker crash, process restart, Attempt timeout, or Lease loss may reclaim the same active Run and Job. Recovery loads the legal Checkpoint prefix and continues from the first incomplete node.

A Provider response is not accepted until Proposal or Final Draft Checkpoint commit. If the response was received but not accepted before interruption, recovery may call the model again and receive a different decision. Sprint 3 makes no exactly-once model-call claim.

An Action result is not accepted until its Result Checkpoint commits. If an Action executed but no Result was accepted, recovery may execute the same read-only logical Action again. If the Checkpoint committed but acknowledgement was lost, hash reconciliation reuses it.

### 15.2 User Stop

- Stop transaction remains the immediate product cancellation boundary.
- Stop permanently sets Run and Job `cancelled` and releases the active slot.
- Remaining Actions and Final Draft publication are forbidden.
- The cancelled Run is never resumed from Checkpoints.
- Retry creates a new Run, Job, deadline, time zone, budgets, and empty Checkpoint sequence.
- Retry never reuses a prior Run's observed time or calculation result.

### 15.3 Cancellation Propagation

Sprint 3 retains Sprint 2B heartbeat-based propagation and adds no dedicated cancellation listener.

Controller revalidates active Run state, current unexpired Lease Token, and Run deadline:

- before each model call;
- before accepting Proposal or Final Draft;
- before each Action;
- before each Action Result append;
- before the next decision;
- inside Publication Barrier.

The heartbeat cancels an in-flight model context after authority is lost. `calculate` and `current_time` are short, cancellable operations; late results cannot pass the fenced Checkpoint or Publication boundary.

## 16. Failure Semantics

Expected Action domain errors are Results and continue the loop:

- `invalid_decimal`
- `invalid_operand_count`
- `unsupported_operation`
- `division_by_zero`
- `calculation_result_too_large`
- `invalid_time_zone`

Terminal safe Run failures include:

- existing `model_timeout`, `model_unavailable`, and `model_invalid_response`;
- `run_deadline_exceeded`;
- `agent_budget_exhausted` when the reserved final decision cannot produce a valid Final Draft;
- `checkpoint_invalid` for a corrupt or contradictory durable prefix;
- existing `recovery_exhausted` after the third expired Attempt.

Lease loss and cancellation remain control flow rather than terminal model or Action errors. Infrastructure details, raw payloads, stack traces, and secrets are never stored as safe error codes or sent to the browser.

## 17. Publication

Final Draft acceptance and publication remain separate durable boundaries:

1. append and reconcile Final Draft Checkpoint;
2. enter the existing transactional Publication Barrier;
3. revalidate active Run, current Job Lease, deadline, cancellation, Chat ownership, and Notebook membership;
4. create exactly one immutable Assistant Message;
5. complete Run and Job together;
6. update Chat activity and notify the existing Run channel.

After a crash between Final Draft Checkpoint and publication, recovery must publish the accepted Final Draft without another model call.

Sprint 3 removes Message `answer_mode`. The source-less workspace discloses that answers are not based on Notebook Sources at the Chat/workspace capability level. Future Grounded Answer truth derives from the producing Run, Run Evidence Set, Publication Barrier, and validated Citations.

## 18. Data Model Migration

### 18.1 `agent_runs`

Add pinned Run configuration:

- `time_zone`
- `deadline_at`
- Action-capable decision limit
- reserved final-decision limit
- total Action limit
- per-batch Action limit
- per-result and total-result byte limits

Remove Sprint 2A single-call fields whose meaning is invalid in a multi-step Run:

- `iteration_count`
- `finish_reason`
- `prompt_tokens`
- `completion_tokens`
- `total_tokens`

Decision counts are derived from Checkpoints. Per-Model-Call finish reason, usage, latency, Provider retries, and cost accounting belong to Sprint 4 Model Call records.

### 18.2 `chat_messages`

Remove `answer_mode` and its constraint. User and Assistant Messages retain role and immutable content only.

### 18.3 `agent_run_checkpoints`

Create the internal append-only table described in Section 11 with delete cascade from `agent_runs`.

### 18.4 Upgrade Safety

The migration must upgrade populated Sprint 2B databases without deleting Messages, Runs, or Jobs. Existing active Runs receive `UTC`, the Sprint 3 default limits, and a fresh ten-minute absolute deadline from the migration transaction's PostgreSQL time; no old Worker may remain active across this migration. Terminal historical Runs receive non-executable defaults required by schema constraints. Sprint 3 Workers start only after migration commit. Migration tests must cover clean databases, populated terminal history, and a pre-migration active Run.

## 19. Authorization And Data Handling

- Agent Checkpoints are internal Agent Module data.
- Browser and ordinary product queries receive no Checkpoint access.
- Worker access is restricted to authorized Runs and fenced writes.
- Checkpoint payloads are user data and follow the parent Run/Chat/Notebook lifecycle.
- Terminal Checkpoints remain internally attached to their Run; Sprint 3 adds no independent TTL.
- Parent authorized deletion or purge removes Checkpoints with the Run.
- Raw Provider payloads, Provider IDs, token usage, hidden reasoning, credentials, and diagnostic history are excluded.
- Sprint 4 defines separate Durable Agent Trace access, redaction, retention, and payload purge.

## 20. API, SSE, And Frontend

### 20.1 Admission

The browser adds its IANA time zone to send and Retry commands. The server validates it and pins a valid zone or `UTC` fallback on the new Run. Transport replay of the same idempotent send returns the originally admitted Run and does not alter its pinned values.

### 20.2 Run Projection

No Action, Checkpoint, budget, Attempt, Lease, deadline internals, or recovery activity are added to the browser projection.

Existing lifecycle states remain:

```text
queued | running | completed | failed | cancelled
```

SSE still sends complete current snapshots and the final persisted Assistant Message. Checkpoint appends do not create a client event protocol.

### 20.3 Answer Disclosure

- Remove `answer_mode` from REST/SSE Message JSON and frontend types.
- Remove the per-Message `model_knowledge` label.
- Keep a concise localized disclosure in the Chat surface header that remains visible after Messages exist and states that the current source-less capability is not based on Notebook Sources.
- Do not display Action names, parameters, results, Checkpoints, budgets, or hidden model reasoning.

## 21. Observability Boundary

Existing Operational Telemetry may add ordinary spans around Controller operations for health and latency, but Sprint 3 does not create a reusable instrumentation SDK or make telemetry durable authority.

Explicitly deferred to Sprint 4:

- Run Events and Attempt history;
- Model Call request/response governance;
- token and cost accounting;
- raw or governed payload retention;
- reusable Recorder and storage interfaces;
- Durable Agent Trace query/access policy;
- Trace retention, redaction, and purge;
- Trace API and UI;
- user-visible Action history.

## 22. Verification

### 22.1 Unit Contracts

- Model Decision tagged-union validation.
- Action Registry duplicate-name startup failure, deterministic discovery, Run filtering, and name resolution without Controller branches.
- Bifrost request encoding and response decoding for final, single, and multiple tool calls.
- whole-batch validation and no partial acceptance.
- structured decimal arithmetic and every domain error.
- IANA default, explicit zone, UTC fallback, and invalid-zone result.
- legal and illegal Checkpoint prefixes.
- canonical payload hashing and identity reconciliation.
- budget derivation from Checkpoints.
- Context reconstruction after every valid prefix.

### 22.2 PostgreSQL Integration

- admission atomically pins time zone, budgets, and deadline.
- append requires active Run, current unexpired Lease Token, and unexpired Run deadline.
- duplicate same-identity/same-hash reconciliation succeeds.
- duplicate same-identity/different-hash fails without overwrite.
- stale Worker cannot append Proposal, Result, or Final Draft.
- Checkpoint sequence constraints reject gaps and contradictory rows.
- terminal parent deletion removes Checkpoints through the authorized lifecycle.
- clean and populated Sprint 2B databases upgrade safely.

### 22.3 Fault Injection

Inject process loss or returned commit uncertainty:

1. before Proposal Checkpoint: model may be called again;
2. after Proposal Checkpoint: model is not called again and first Action executes;
3. after Action 1 Result in a multi-Action batch: Action 1 is skipped and Action 2 resumes;
4. after domain-error Result: the error is reused and supplied to the next model call;
5. after the last Result: next model decision resumes;
6. after Final Draft Checkpoint: publication resumes with no model call;
7. during each Checkpoint commit: matching payload reconciles, conflicting payload fails;
8. after Lease reclaim: stale Worker cannot append or publish;
9. after Attempt three expires: Run fails `recovery_exhausted` with no Assistant Message.

Test Actions must expose deterministic call counters and blocking hooks so tests prove skipped versus repeated execution without depending on timing alone.

### 22.4 Concurrency

- Stop before Checkpoint commit prevents the Checkpoint.
- Checkpoint commit before Stop may remain retained, but Stop prevents later work and publication.
- Publication before expiry completes; expiry becomes a no-op.
- Expiry before publication fails Run/Job and blocks the answer.
- Concurrent Workers cannot accept the same missing Action Result under different Leases.
- Two retries of the same command create at most one new Run and empty Checkpoint sequence.

### 22.5 End To End

- real configured Qwen through Bifrost calls `current_time` and `calculate` and publishes one answer;
- four-location acceptance stays within `4 + 1 / 8 / 4`;
- reload restores the completed Message without Action/Trace UI;
- Stop still releases the composer and prevents late publication;
- deadline expiry becomes visible through the existing failed Run projection;
- source-less disclosure remains visible without a Message `answer_mode`.

The real credentialed smoke path is opt-in and never prints credentials or raw Provider payloads. Deterministic CI uses a local fake OpenAI-compatible upstream.

## 23. Explicitly Out Of Scope

- Source ingestion, retrieval, RAG, reranking, Evidence, Citations, or Grounded Answers
- web search, public URL fetch, external API Actions, Notebook mutation, shell, code execution, or Sandbox
- MCP client/server, Bifrost MCP execution, plugin discovery, user-installed tools, or approval UI
- reusable Action SDK, public or dynamic Tool Registry, general Workflow SDK, DAGs, planners, reflection, subagents, or branching
- parallel Action execution
- Action catalog versioning or multi-version executor registry
- token streaming, partial Assistant persistence, or delta replay
- Action/Checkpoint progress UI or user-visible Reasoning Trace
- Run Events, Attempt history, Model Call storage, Durable Agent Trace, Trace UI, or reusable Observability/Audit SDK
- new Provider settings, model picker, or multi-Provider real acceptance
- pause/resume, Retry Checkpoint reuse, or completed-answer regeneration
- dedicated deadline sweeper
- independent Checkpoint TTL or purge policy

## 24. Delivery Sequence

Implementation should preserve independently verifiable seams in this order:

1. schema migration and Run-pinned configuration;
2. Provider-neutral Model Decision adapter contracts;
3. built-in Actions and deterministic test Actions;
4. append-only Checkpoint store and prefix loader;
5. Agent Controller iteration and budgets;
6. Lease/cancellation/deadline integration;
7. Final Draft publication recovery;
8. API/frontend field removal and workspace disclosure;
9. fault-injection, concurrency, migration, and real-provider acceptance.

Each step must keep the existing Sprint 2B cancellation, fencing, retry, and Publication Barrier tests passing or deliberately replace them with stronger equivalent coverage.

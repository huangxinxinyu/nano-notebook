# Nano Notebook Sprint 2A PRD

## Document Status

- **Sprint:** Sprint 2A
- **Status:** Complete
- **Date:** 2026-07-14
- **Theme:** Durable bare AgentLoop and private model-knowledge chat
- **Delivery boundary:** Sprint 2A implements the smallest production-shaped Agent execution path. Sprint 2B adds interruption and whole-pass crash recovery, Sprint 3 adds checkpointed multi-step Agent Action execution, and Sprint 4 adds the reusable Observability/Audit SDK.

## 1. Decision And Product Change

Sprint 2A replaces the previously considered Source-ingestion Sprint 2. The immediate roadmap priority is to prove how one User Message moves through durable admission, an independent Worker, a real model call, publication, and browser delivery.

This PRD also changes one existing product rule:

> A private Chat remains usable when the Notebook has no Sources. The Agent may answer from model knowledge, must not imply that the answer is grounded or cited, and may contextually explain how relevant Sources would improve precision, depth, or verification.

This decision supersedes existing requirements that make selected Sources a precondition for every Agent Run or prohibit all model-knowledge answers. It does not weaken the future grounded-answer contract: when a later Run uses Sources, retrieval provenance, Citation coverage, and groundedness remain separate mandatory capabilities.

## 2. Source Documents

Sprint 2A derives from these repository contracts:

- `docs/product-discovery/DISCOVERY.md`
- `docs/product-discovery/REQUIREMENTS.md`
- `docs/product-discovery/TECHNICAL-HANDOFF.md`
- `docs/technical-architecture/ARCHITECTURE.md`
- `docs/technical-architecture/adr/0004-modular-monolith-with-workers.md`
- `docs/technical-architecture/adr/0006-use-postgresql-as-system-of-record.md`
- `docs/technical-architecture/adr/0010-use-a-standalone-bifrost-model-gateway.md`
- `docs/technical-architecture/adr/0012-bound-the-durable-runtime-to-product-jobs.md`
- `docs/technical-architecture/adr/0013-assign-authoritative-data-to-go-modules.md`
- `docs/technical-architecture/adr/0014-enforce-authorization-in-go-and-postgresql-rls.md`
- `docs/technical-architecture/adr/0022-use-rest-direct-blob-upload-and-sse.md`
- `docs/technical-architecture/adr/0025-use-a-react-typescript-spa.md`
- `docs/technical-architecture/adr/0030-cancel-cooperatively-and-publish-through-a-barrier.md`
- Bifrost's OpenAI-compatible `POST /v1/chat/completions` gateway contract.

Where the documents require the complete future runtime—retrieval, cancellation, leases, recovery, durable traces, or resumable token deltas—this PRD defines an intentional delivery slice rather than a replacement architecture.

## 3. Sprint Goal

Deliver one real, durable, source-less Chat turn:

> Write a message → durably admit it → run one real model call in the independent Worker → atomically publish the complete answer → deliver the Run state and answer to the browser without polling → restore the same conversation after reload.

The Sprint is successful when this path is understandable, testable, and safe against duplicate browser submission. It does not need to look agentic through artificial planning or reflection calls.

## 4. Why This Slice

A synchronous handler that waits for the model would prove only that an HTTP client can call an LLM. A general tool loop, durable event system, external message queue, or full recovery runtime would mix several independent engineering problems into the first learning step.

Sprint 2A instead proves the stable boundaries that later capabilities need:

- Chat owns durable conversation history.
- Agent Run owns the user-visible lifecycle of one requested answer.
- Agent Job owns delivery of that Run to an independent Worker.
- ContextBuilder owns the bounded model input.
- Models owns Bifrost protocol integration.
- Publisher alone creates a durable Assistant Message.
- PostgreSQL is authoritative; SSE is only a live projection.

## 5. Primary User Journey

1. An authenticated user opens a Notebook with no Sources.
2. The workspace restores the user's most recently active private Chat. If none exists, it creates one private Chat for the user.
3. The user enters a non-empty question and sends it.
4. The browser assigns the User Message a UUID and sends the command once. A transport retry reuses the same UUID.
5. The Control Plane atomically persists the User Message, a queued Agent Run, and a queued Agent Job.
6. The API returns `202 Accepted` with `message_id`, `run_id`, and `status: "queued"`.
7. The browser opens a per-Run SSE connection and shows a fixed, truthful activity state such as “正在生成回答…”. It does not display model chain-of-thought.
8. The Worker claims the Job, changes the Run to `running`, builds context from PostgreSQL, and makes one non-streaming Bifrost call.
9. On success, one transaction creates the Assistant Message, completes the Run, and succeeds the Job.
10. The Control Plane reloads that committed snapshot and sends it over SSE. The browser replaces the activity state with the complete answer.
11. Reloading the page restores the User and Assistant Messages from PostgreSQL.

If the model call fails after Bifrost exhausts its configured retries, the Run and Job become failed, no Assistant Message is created, the activity state ends, and the user sees a safe retryable failure message outside conversation history.

## 6. In Scope

### 6.1 Private Chat Foundation

- Create and list private Chats inside an authorized Notebook.
- Restore the most recently active Chat in the workspace.
- Create one Chat when the current user has none in that Notebook.
- Persist complete User and published Assistant Message history.
- Return Chat Messages in one centralized `(created_at, id)` order.
- Keep the schema capable of multiple private Chats per user and Notebook even though Chat switching, rename, and deletion UI are deferred.

### 6.2 Durable Admission

- Require a client-generated UUID as the User Message ID.
- Atomically create the User Message, queued Agent Run, and queued Agent Job.
- Return only after the transaction commits.
- Treat the User Message ID as the idempotency identity for the turn.
- Enforce at most one queued or running Agent Run per user across all Chats.
- Reject another distinct send with `409 Conflict` and create no rows.

### 6.3 Minimal AgentLoop

- Execute exactly one fixed pass:

  `LoadRun -> BuildContext -> InvokeModel -> PublishAnswer`

- Make ContextBuilder, AgentRunner, and Publisher explicit interfaces or components.
- Invoke one real configured model through Bifrost.
- Produce either one typed successful model result or one terminal normalized failure.
- Do not advertise tools and do not implement a `while tool_calls` loop.

### 6.4 Independent Job Delivery

- Activate the existing Worker process as the Agent Job consumer.
- Insert Job and `pg_notify` in the same admission transaction; PostgreSQL delivers the notification after commit.
- Use `LISTEN/NOTIFY` as a low-latency wake-up hint.
- Scan the indexed queued-Job set every five seconds on startup, reconnect, and as a fallback.
- Claim one Job atomically with `FOR UPDATE SKIP LOCKED`.

### 6.5 Run Projection Over SSE

- Return the command response immediately after durable admission.
- Expose an authenticated, per-Run SSE endpoint.
- Send the current durable Run snapshot when the stream opens or reconnects.
- Project `queued`, `running`, `completed`, and `failed`.
- Include the final persisted Assistant Message in the completed snapshot.
- Use heartbeat comments only as a connection-maintenance detail.

### 6.6 Browser Chat Experience

- Replace the Sprint 1 Chat placeholder with a functional composer and Message list.
- Use the already-installed `@assistant-ui/react` as the Chat presentation/runtime adapter; PostgreSQL Messages and Agent Runs remain authoritative.
- Render source-less Assistant Messages as “based on model knowledge,” without Citations or groundedness claims.
- Show fixed queued/running activity, not token-by-token output or simulated typing.
- Disable the active composer locally while the Run is queued/running; retain a draft rejected by a server-side `409`.
- Restore history and an incomplete Run snapshot after refresh.
- Preserve Simplified Chinese and English product strings and existing responsive panel behavior.

## 7. Explicitly Out Of Scope

- Source upload, parsing, chunking, embedding, retrieval, RAG, reranking, or Citations
- Web search, quick research, or automatic Source collection
- Provider tool-call iteration and bounded Agent Actions; these belong to Sprint 3
- MCP, executable or external tools, planners, critics, reflection, or subagents
- Token streaming, fake typewriter playback, partial-output storage, backpressure, or delta replay
- Interrupt or stop controls; these belong to Sprint 2B
- Leases, heartbeats, attempts, fencing, stale-Job recovery, or process-loss recovery; these belong to Sprint 2B
- Run Events, Model Call payload retention, Durable Agent Trace, Trace UI, new OpenTelemetry abstractions, or a reusable observability/audit layer; these belong to Sprint 4
- Redis, an external MQ, an outbox, or a separate persisted Context/Session blob
- Token-aware trimming, summarization, compaction, or long-term memory
- Multi-Chat switching UI, Chat rename, Chat delete, branching, editing, regeneration, or queued conversational turns
- Model picker, Provider settings UI, prompt editor, or Agent-level retries
- A general Workflow SDK or arbitrary DAG runtime

Existing Sprint 1 operational telemetry may remain in place. Sprint 2A does not expand it into the deferred observability product.

## 8. Architecture And Information Flow

```text
Browser
  |  POST User Message
  v
Control Plane ---- admission transaction ----> PostgreSQL
  |                                         Message + Run + Job
  |  202 queued                                  |
  |                                              | pg_notify(agent_jobs)
  |  GET per-Run SSE                             v
  |                                         Agent Worker
  |                                              |
  |                                        claim Job + Run running
  |                                              |
  |                                 ContextBuilder reads 20 Messages
  |                                              |
  |                                        Models Module
  |                                              |
  |                                   Bifrost /v1/chat/completions
  |                                              |
  |                                   publication transaction
  |                                   Assistant + completed/succeeded
  |                                              |
  |<---- shared listener + snapshot reload ------| pg_notify(agent_runs)
  |
  +---- SSE Run snapshot + final Assistant ----> Browser
```

Notifications never carry authoritative product content. Their payload is only an opaque Job wake-up or `run_id`. Consumers always claim or reload committed rows from PostgreSQL.

## 9. Module Responsibilities

| Component | Owns | Must not own |
| --- | --- | --- |
| Chat Module | Chats, Messages, Chat authorization, ordered history | Model protocol, Job delivery, Run transitions |
| Agent Module | Agent Runs, fixed AgentLoop, ContextBuilder, AgentRunner, Publisher | Provider SDK details, SSE sockets |
| Jobs Module | Agent Job admission, claiming, minimal Job transitions, notification wake-up | Product answer state, model prompts |
| Models Module | Provider-neutral model request/result and Bifrost HTTP adapter | Message persistence, retries beyond Bifrost |
| Control Plane | Authenticated REST commands/queries, shared Run listener, SSE fan-out | Executing model calls, treating SSE as storage |
| Worker | Claiming Jobs and invoking AgentLoop | Browser connections, private Chat APIs |
| Web Client | Composer, optimistic command identity, Run projection, durable-history rendering | Authoritative Run/Job state, chain-of-thought |

The AgentLoop depends on provider-neutral `ModelClient` behavior. No `aliyun`, DashScope, or OpenAI-compatible wire types appear in Chat or Agent domain interfaces.

## 10. Persistence Model

The exact SQL naming may follow repository module conventions, but the following fields and constraints are part of the Sprint contract.

### 10.1 `chat_chats`

| Field | Contract |
| --- | --- |
| `id` | Server-generated opaque Chat ID, primary key |
| `notebook_id` | Required Notebook reference |
| `creator_user_id` | Required creator; private ownership boundary |
| `title` | Stable placeholder title in Sprint 2A; title management UI is deferred |
| `created_at` | Database timestamp |
| `updated_at` | Updated when a Message is admitted or published |

Index Chats by `(creator_user_id, notebook_id, updated_at desc, id desc)`. Do not add a uniqueness constraint that limits one Chat per user and Notebook.

### 10.2 `chat_messages`

| Field | Contract |
| --- | --- |
| `id` | User Message: client UUID. Assistant Message: server-generated opaque ID |
| `chat_id` | Required Chat reference |
| `role` | `user` or `assistant` |
| `content` | Immutable non-empty text |
| `answer_mode` | `model_knowledge` for Sprint 2A Assistant Messages; null for User Messages |
| `created_at` | Database timestamp |

Messages are append-only in Sprint 2A. All history and ContextBuilder queries use `(created_at, id)` as the deterministic order. No Chat-local sequence, parent ID, mutable status, placeholder Assistant row, or error Message is added.

A submitted User Message is limited to 8,000 Unicode characters after preserving its text content; leading/trailing-only input is rejected. Output size is bounded by the configured model completion limit.

### 10.3 `agent_runs`

| Field | Contract |
| --- | --- |
| `id` | Server-generated opaque Run ID, primary key |
| `user_id` | User who requested the answer |
| `chat_id` | Private Chat used by the Run |
| `input_message_id` | Unique User Message reference |
| `output_message_id` | Unique nullable Assistant Message reference; set only at publication |
| `status` | `queued`, `running`, `completed`, or `failed` |
| `model` | Configured provider/model identifier, frozen at admission |
| `prompt_version` | Frozen system-prompt version, initially `agent-bare-v1` |
| `iteration_count` | `0` before model-loop invocation, `1` after the sole Fixed Agent Loop iteration; this is not a Job attempt count |
| `finish_reason` | Nullable normalized model finish reason |
| `prompt_tokens` | Nullable usage count from a successful response |
| `completion_tokens` | Nullable usage count from a successful response |
| `total_tokens` | Nullable usage count from a successful response |
| `error_code` | Nullable safe normalized terminal error code |
| `created_at` | Admission timestamp |
| `started_at` | Nullable Worker-claim timestamp |
| `finished_at` | Nullable terminal timestamp |
| `updated_at` | Latest state timestamp |

Required constraints:

- one Run for each `input_message_id`;
- at most one active Run per `user_id` through a partial unique index where status is `queued` or `running`;
- completed Run requires one output Message and no error;
- failed Run requires no output Message and one safe error code.

### 10.4 `agent_jobs`

| Field | Contract |
| --- | --- |
| `id` | Server-generated opaque Job ID, primary key |
| `kind` | Only `agent_run` in Sprint 2A |
| `run_id` | Unique Agent Run reference |
| `status` | `queued`, `running`, `succeeded`, or `failed` |
| `created_at` | Admission timestamp |
| `started_at` | Nullable claim timestamp |
| `finished_at` | Nullable terminal timestamp |
| `updated_at` | Latest state timestamp |

Index the claim path by queued status and creation order. There are intentionally no attempt, lease, heartbeat, retry, checkpoint, or payload columns in Sprint 2A.

## 11. Authorization And Database Roles

- Every browser route resolves the Principal from the opaque Session cookie and never accepts a user ID from the request body.
- A user may access a Chat only when they created it and still have access to the containing Notebook.
- Chat privacy remains independent of Notebook ownership: another future Member or Owner cannot read that user's Chat.
- Mutating browser requests require the existing same-origin CSRF token.
- `nano_app` RLS policies permit only the Principal's authorized Chats, Messages, Runs, and admission-related Job rows.
- `nano_worker` may read and transition registered Agent Jobs and the referenced authorized Run/Chat data needed for execution and publication. It has no browser Session semantics.
- The SSE endpoint authenticates the requesting user and verifies Run ownership before subscribing.
- A PostgreSQL notification contains no prompt, Message content, model response, credential, or user-readable error.

## 12. REST Contract

All responses use the existing JSON error envelope. Paths are versioned under `/api/v1`.

### 12.1 Chat Bootstrap

`GET /api/v1/notebooks/{notebook_id}/chats`

- Returns the current user's private Chats in most-recent order.
- The first UI slice uses the first Chat only.
- Returns `404` for a missing or inaccessible Notebook without revealing its existence.

`POST /api/v1/notebooks/{notebook_id}/chats`

- Requires CSRF.
- Requires the existing `Idempotency-Key` header. A transport retry with the same key returns the same Chat rather than creating another one.
- Creates one ordinary private Chat and returns `201 Created`; an idempotent replay returns the existing Chat.
- It does not promise singleton or ensure semantics.

`GET /api/v1/chats/{chat_id}`

- Returns the authorized Chat, all durable Messages in `(created_at, id)` order, and any currently active Run for that Chat.
- A bounded response limit may be added only with a matching history-loading UI; Sprint 2A must restore the complete acceptance conversation.

### 12.2 Submit A User Message

`POST /api/v1/chats/{chat_id}/messages`

Request:

```json
{
  "id": "client-generated-uuid",
  "content": "Explain KV cache"
}
```

Successful response, including an idempotent repeat:

```json
{
  "message_id": "client-generated-uuid",
  "run_id": "run_opaque-id",
  "status": "queued"
}
```

`status` is the Run's current durable state. It is `queued` on initial admission but may already be `running`, `completed`, or `failed` on an idempotent replay.

- Returns `202 Accepted` after the admission transaction commits.
- A repeat with the same Message ID, creator, Chat, role, and content returns the original Run and its current status without creating another Message, Run, Job, or answer.
- Reusing the same ID with different ownership, Chat, role, or content returns `409 Conflict` with `message_id_conflict`.
- A distinct Message ID while the user has an active Run returns `409 Conflict` with `active_run_conflict`; the transaction creates nothing.
- Empty, whitespace-only, malformed UUID, or over-limit content returns `400 Bad Request`.
- Duplicate resolution occurs before active-Run rejection so retrying the admitted command remains safe while its Run is active.

Admission serializes per user, validates authorization, and relies on database uniqueness as the final invariant. A concurrent conflict rolls back the complete transaction.

## 13. SSE Contract

`GET /api/v1/agent-runs/{run_id}/events`

- Uses the authenticated same-origin Session cookie.
- Responds as `text/event-stream`.
- Authorizes the Run, registers the in-process subscriber, and then reloads the durable snapshot, preventing a missed transition between snapshot and subscription.
- Emits a `run` event whose data is a complete current projection:

```json
{
  "run": {
    "id": "run_opaque-id",
    "status": "completed",
    "error_code": null
  },
  "message": {
    "id": "msg_opaque-id",
    "role": "assistant",
    "content": "...",
    "answer_mode": "model_knowledge",
    "created_at": "2026-07-14T12:00:00Z"
  }
}
```

- `message` is null for queued, running, and failed Runs.
- A terminal snapshot is sent once and the server may close the stream.
- Reconnection sends the latest snapshot; it does not replay historical events.
- No durable event ID, cursor, RunEvent row, Last-Event-ID behavior, or token delta is promised.
- Duplicate snapshots are valid; the frontend upserts by Run and Message ID.

The Control Plane owns one shared PostgreSQL listener rather than one database listener per browser. It receives `run_id`, reloads committed state under the subscriber's authorization context, and fans the snapshot to in-process subscribers.

## 14. Run And Job State Machines

### 14.1 Agent Run

```text
queued --Worker claim--> running --publication--> completed
                                  \--terminal error--> failed
```

No other transition is legal in Sprint 2A. `completed` and `failed` are terminal.

### 14.2 Agent Job

```text
queued --claim--> running --publication--> succeeded
                           \--terminal error--> failed
```

The frontend never reads Job status. Run and Job transition together in Sprint 2A because there is one Job per Run, but they remain distinct state owners so Sprint 2B can introduce multiple execution attempts without changing product Run identity.

If a Worker process dies after claim, the Job and Run may remain `running` in Sprint 2A. Detecting and recovering that condition is the explicit Sprint 2B problem, not hidden recovery behavior in this slice.

## 15. Worker And PostgreSQL Dispatch

1. Admission inserts a queued Job and calls `pg_notify('nano_agent_jobs', job_id)` before commit.
2. PostgreSQL delivers the notification only after commit.
3. The Worker listener treats it as a wake-up and repeatedly calls the same database claim function used by fallback scanning.
4. Claim uses a transaction, `FOR UPDATE SKIP LOCKED`, and a conditional queued-to-running update.
5. The same claim transaction moves the Run to `running` and notifies `nano_agent_runs` with the Run ID.
6. The Worker executes AgentLoop outside the claim transaction.
7. Publication or failure uses one short terminal transaction and notifies `nano_agent_runs` after the authoritative rows are updated.
8. On Worker startup, listener reconnect, and every five seconds, the Worker scans the indexed queued set.

The five-second scan is not browser polling. It is queue-delivery repair for lossy PostgreSQL notifications. At Sprint 2A scale, this avoids another operational dependency while keeping Job rows authoritative.

## 16. ContextBuilder Contract

ContextBuilder receives a Run ID and produces an in-memory provider-neutral request:

1. Load and validate the queued/running Run, input Message, private Chat, creator, and Notebook access.
2. Query the latest 20 durable `user` or published `assistant` Messages in descending `(created_at, id)` order.
3. Reverse that slice into chronological order.
4. Prepend the system prompt separately; it does not consume one of the 20 Message slots.
5. Exclude UI placeholders, Job state, Run errors, unpublished model output, and any failed turn without an Assistant Message.
6. Release the constructed request after the model call. Do not persist a duplicate Context row or Session blob.

The complete Chat remains in PostgreSQL. The 20-Message limit is selection, not retention, and means approximately ten complete turns rather than twenty turns.

## 17. System Prompt Contract

The initial prompt version is `agent-bare-v1`. Its exact text lives in one versioned server constant or embedded asset and must enforce these behaviors:

- Answer the user's question directly and in the user's language.
- Never imply grounding when no Sources are available; disclose the model-knowledge basis when it is relevant to the answer's reliability.
- Never invent Citations, claim to have read Notebook Sources, or claim to have searched the web.
- Do not block a useful answer merely because Sources are absent.
- Suggest adding relevant Sources only when they would materially improve accuracy, depth, recency, verification, or citation quality.
- Keep the Source suggestion contextual and brief rather than repeating a disclaimer on every turn.
- The Agent may help the user identify what kinds of Sources to collect, but must not promise that it can browse or ingest them in Sprint 2A.
- Do not expose hidden chain-of-thought. Provide concise reasoning or a summary when useful.

The frontend's `model_knowledge` label is based on persisted answer mode, not on parsing model prose.

## 18. Models Module And Bifrost Contract

The Models Module exposes a narrow provider-neutral interface conceptually equivalent to:

```go
type ChatRequest struct {
    Model    string
    Messages []ChatMessage
}

type ChatResult struct {
    Text             string
    FinishReason     string
    PromptTokens     *int
    CompletionTokens *int
    TotalTokens      *int
}

type ModelClient interface {
    Complete(ctx context.Context, request ChatRequest) (ChatResult, error)
}
```

The Bifrost adapter:

- calls `POST {gateway_base_url}/v1/chat/completions`;
- sends the OpenAI-compatible `messages` array with `stream: false`;
- uses one server-configured explicit model, locally `aliyun/qwen-flash`;
- accepts the first valid Assistant choice and normalized usage fields;
- rejects an empty choice, empty Assistant content, malformed JSON, non-2xx status, or timeout as a typed model error;
- closes and bounds response bodies;
- does not log or persist raw prompt/response bodies;
- performs no retry of its own.

Local Bifrost remains configured with a 60-second Provider request timeout and two bounded retries. The Worker call context must allow Bifrost's complete retry budget without creating an additional attempt loop. Exact outer deadline is configuration, with a local default of 210 seconds.

The default completion limit is 2,048 output tokens. Model selection and limits are server configuration, not browser input.

## 19. Publication And Failure

### 19.1 Successful Publication

Publisher performs one transaction that:

1. locks and reloads the running Run and its running Job;
2. revalidates private Chat ownership, current Notebook access, and that no output Message already exists;
3. creates one immutable Assistant Message with `answer_mode = 'model_knowledge'`;
4. sets Run output, usage, finish reason, iteration count, `completed`, and terminal timestamps;
5. sets Job `succeeded` and terminal timestamps;
6. updates Chat activity time;
7. calls `pg_notify('nano_agent_runs', run_id)`;
8. commits before the answer is projected to the browser.

Only this boundary creates an Assistant Message. Provider output in Worker memory is provisional until publication commits.

### 19.2 Terminal Model Failure

One transaction:

- verifies the Run and Job are running;
- sets Run `failed` with one safe code such as `model_timeout`, `model_unavailable`, or `model_invalid_response`;
- sets Job `failed`;
- sets terminal timestamps and iteration count;
- notifies the Run channel;
- creates no Assistant Message.

Raw Provider bodies, secrets, stack traces, and prompt content are not sent to the browser or stored as error text. The UI maps the safe code to localized recovery copy.

## 20. Frontend State Contract

- The Chat panel loads or creates the initial private Chat only after Notebook authorization succeeds.
- Durable Messages are rendered from the Chat query and keyed by Message ID.
- On send, the browser creates one UUID and keeps it with that draft until admission succeeds, conflicts definitively, or the user deliberately changes the command after a known rejection.
- After `202`, the User Message can appear immediately because it is already committed; the browser opens the Run SSE stream.
- `queued` displays a waiting activity and `running` displays a generating activity. Neither is inserted into the Message list.
- `completed` invalidates or patches the Chat query with the returned Assistant Message and removes activity.
- `failed` removes activity and displays a localized retryable error outside the Message list.
- `active_run_conflict` retains the unsent draft and tells the user to wait for the active Run.
- Network uncertainty after submission offers retry using the same UUID, preventing duplicate admission.
- Browser refresh first reloads durable Chat state. If an active Run is returned, the browser reopens its SSE endpoint.
- The composer must remain keyboard accessible, support multiline text, provide an accessible send name, and preserve the compact workspace layout.

## 21. Error Semantics

| Condition | HTTP/SSE result | Durable result |
| --- | --- | --- |
| Missing/expired Session | `401` | none |
| Missing CSRF on mutation | `403` | none |
| Inaccessible Notebook/Chat/Run | safe `404` | none |
| Invalid Message ID/content | `400 validation_failed` | none |
| Same Message ID, same command | `202`, original Run/current status | no new rows |
| Same Message ID, different command | `409 message_id_conflict` | no mutation |
| Different Message while user has active Run | `409 active_run_conflict` | no rows |
| Bifrost final failure | SSE `failed` snapshot | failed Run/Job, no Assistant |
| SSE disconnect | browser reconnects | no state change |
| Lost PostgreSQL notification | fallback scan/snapshot recovery | rows remain authoritative |
| Worker dies after claim | Run remains running | intentionally deferred to Sprint 2B |

## 22. Verification Strategy

Implementation follows test-first development. Each production behavior is introduced by a focused failing test, then the minimum implementation, then refactoring with the suite green.

### 22.1 Backend Unit Tests

- ContextBuilder selects the latest 20 Messages and restores chronological order.
- System prompt is separate and Message roles map correctly.
- AgentLoop calls each fixed stage once and has no tool iteration.
- Bifrost adapter sends non-streaming OpenAI-compatible input and normalizes a successful response.
- Bifrost adapter rejects non-2xx, malformed, empty, oversized, and timed-out responses.
- Run and Job state transition guards reject illegal transitions.

### 22.2 PostgreSQL Integration Tests

- Chat privacy and RLS isolate users.
- Admission atomically creates Message, Run, and Job.
- Duplicate same-ID command returns the original Run.
- Reused ID with different content conflicts.
- Concurrent distinct sends admit exactly one active Run and leave no orphan Message.
- Worker claim uses the queue index and cannot claim one Job twice concurrently.
- Successful publication creates exactly one Assistant Message and terminal states.
- Failure creates no Assistant Message and releases the active-Run slot.
- Message reads use deterministic `(created_at, id)` ordering.

### 22.3 Controlled End-To-End Integration

A controlled HTTP upstream implements the real Bifrost OpenAI-compatible protocol. The test starts the Control Plane and Worker against PostgreSQL, submits through REST, observes queued/running/completed through SSE, and verifies the final durable Message. A second scenario returns a terminal gateway failure and verifies failed/no Assistant behavior.

This proves Nano Notebook's Bifrost wire contract deterministically without spending Provider tokens or depending on public network availability.

### 22.4 Frontend Tests

- Chat bootstrap restores or creates a Chat.
- Sending uses one stable UUID and opens the per-Run SSE stream.
- Queued/running display fixed activity, not a Message.
- Completed renders one final Assistant Message.
- Failed shows localized non-Message error state.
- `409` retains the draft.
- Refresh with an active Run reconnects and refresh with a completed Run restores history.
- English and Simplified Chinese strings and accessible composer behavior remain covered.

### 22.5 Opt-In Live Smoke

A repository-owned local smoke command may call the real Compose Bifrost configured with `aliyun/qwen-flash` when `DASHSCOPE_API_KEY` is present. It is opt-in and is not a deterministic CI gate. It must never print the API key or raw credential-bearing configuration.

## 23. Definition Of Done

Sprint 2A is complete only when all of the following are demonstrated:

1. The Chat UI sends a text question without requiring a Source.
2. The API returns `202` with `message_id`, `run_id`, and queued status.
3. User Message, Agent Run, and Agent Job are committed atomically.
4. The independent Worker executes the fixed AgentLoop through Bifrost using the real OpenAI-compatible protocol.
5. Per-Run SSE projects queued and running while the UI shows a fixed activity state.
6. Success persists one complete Assistant Message, completes the Run, succeeds the Job, and displays the answer once.
7. Browser refresh restores the same Chat history and reconnects an active Run.
8. Retrying one Message UUID does not create a second answer; another send during an active Run returns `409` with no rows.
9. Final Bifrost failure produces a failed Run/Job and no Assistant Message.
10. Automated Go, frontend, PostgreSQL integration, controlled Bifrost-protocol, build, lint, and type checks pass.
11. The source-less product rule and Sprint 2A/2B/3 boundaries are reflected in the relevant product and architecture documentation.

## 24. Deferred Roadmap

### Sprint 2B — Interruption And Recovery

- User stop command
- idempotent Run-scoped REST cancellation and terminal `cancelled` SSE projection
- idempotent Run-scoped retry that creates a new Run and Job for the same input Message without a parent-Run column
- cooperative context cancellation
- terminal durable cancellation and prevention of late publication
- cancellation propagated to a running Worker by the existing heartbeat, without a separate cancellation listener
- Job lease generations, heartbeats, fencing, and bounded reclaim policy without a separate Attempt history table
- configurable defaults of a 30-second lease and 10-second heartbeat, renewed from PostgreSQL current time
- stale-running detection and whole-pass re-execution only after lease expiry; explicit terminal model failures are not re-executed by the Job Runtime
- at most three total Job attempts, then terminal `recovery_exhausted`
- no user-visible recovery state; the Run remains `running` while expired work is reclaimed
- direct transactional reclaim of expired `running` Jobs without an intermediate `queued` transition
- graceful Worker shutdown actively expires its current lease and wakes reclaim, with natural expiry as fallback
- reconciliation of unknown Publication commit outcomes before failure or lease recovery
- safe whole-pass re-execution without duplicate Assistant Messages; no Run Checkpoint or partial model-generation resume
- restart and fault-injection verification

### Sprint 3 — Checkpointed Multi-Step Agent Loop

- removal of the Sprint 2A Message `answer_mode`, API projection, and per-Message label; source-grounded truth will derive from Run evidence and Citations
- removal of single-call Run `iteration_count`, `finish_reason`, and token-usage columns; Model Call accounting moves to Sprint 4 Trace
- Provider tool-call support normalized into bounded, application-defined Agent Actions
- an internal startup Action Registry so later built-in Actions can register definitions and executors without changing Agent Controller orchestration
- a unified Provider-neutral Model Decision result containing either a Final Draft or ordered Action Proposal batch
- one real `aliyun/qwen-flash` through-Bifrost acceptance path, with automated adapter contracts for final, single-Action, ordered multi-Action, and invalid responses
- built-in typed `calculate` and `current_time` Actions: structured decimal arithmetic for calculation, and a fixed observed instant, IANA zone, and UTC offset for time; each Run pins the user's IANA time zone for `current_time` defaults
- Agent Controller iteration across model decisions, Action execution, and final answer publication
- durable Run Checkpoints for accepted model decisions and completed Action results
- an append-only, idempotently keyed Checkpoint sequence with no persisted step-running state
- stable Checkpoint identities and canonical payload hashes for uncertain-commit reconciliation
- explicit at-least-once model invocation when a response is lost before Checkpoint acceptance
- Provider-neutral Checkpoint payloads limited to recovery, excluding raw model and diagnostic data
- terminal Checkpoints retained internally with their Run and removed through the same parent lifecycle, without a separate Sprint 3 TTL
- ordered Action batches with sequential execution and per-Action-result recovery
- checkpointed typed Action domain errors that the next model decision may repair
- Run-pinned model, Action, elapsed-time, and result-size budgets with one Action-disabled final response
- configurable Sprint 3 defaults of four Action-capable decisions, one reserved final decision, eight Actions per Run, and four Actions per batch
- an admission-pinned ten-minute default Run deadline that survives queue delay and recovery
- idempotent on-demand Run expiry from queue scan, admission/Retry, and SSE paths without a dedicated sweeper
- recovery from the first incomplete step after lease loss or Worker process loss
- one Agent Job per Run across the full multi-step loop, without per-step child Jobs
- no Checkpoint resume after user Stop and no Checkpoint inheritance by a Retry Run
- heartbeat-based Stop propagation plus authority checks at every model, Action, Checkpoint, and publication boundary
- no new Action, Checkpoint, budget, or recovery projection in SSE or the browser; Trace UI remains Sprint 4
- cancellation, fencing, budgets, and terminal failure semantics at model and Action boundaries
- no reusable Action SDK, runtime plugins, MCP runtime, arbitrary external tools, or generic Workflow engine
- no Action catalog versioning or multi-version executor registry; Sprint 3 built-in Action contracts remain stable

### Sprint 4 — Reusable Observability And Audit SDK

- Run Events and Model Call records
- governed request/response payload capture
- Durable Agent Trace and audit access
- OpenTelemetry instrumentation seams and exporters
- retention, redaction, correlation, and Trace UI

These later Sprints must extend the Run, Job, ModelClient, and Publisher seams established here instead of replacing the Sprint 2A information flow.

Run Checkpoints are required in Sprint 3 before multi-step model/Action iteration. They persist accepted step results and resume from the first incomplete step; they are intentionally unnecessary for Sprint 2B's single-call Fixed Agent Loop and are distinct from Sprint 4 trace or audit storage.

Sprint 2B also evolves the Sprint 2A one-Run-per-input-Message constraint. Transport replay still returns the originally admitted Run, and infrastructure recovery still creates only a new attempt within that Run. An explicit user retry after a cancelled or failed Run creates a new Run and Job for the same immutable User Message; the input may have multiple failed or cancelled Runs but at most one active and one completed Run. Completed-answer regeneration remains outside scope.

Retry remains available only while that input is the latest unanswered User Message. Once the Chat advances, the historical Run cannot be retried; Sprint 2B does not implement branching. Context construction is anchored at the Run input Message rather than the current Chat head.

Sprint 2B evolves the Chat snapshot from one `active_run` to a `runs` projection containing only the newest Run for each User Message. This restores active execution, terminal failure/cancellation labels, and latest-message Retry after refresh without exposing superseded Runs or Job-runtime state.

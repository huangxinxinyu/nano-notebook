# Nano Notebook Sprint 5 PRD

## Document Status

- **Sprint:** Sprint 5
- **Status:** Ready for review
- **Date:** 2026-07-18
- **Theme:** Durable Agent Trace collection, batching, replay, and operator dashboard
- **Delivery boundary:** Sprint 5 turns the Sprint 4 in-process Durable Agent Trace into a complete Nano Notebook operator workflow: Agent processes durably journal observations, batch them to a standalone Collector, retain sensitive replay payloads separately, and expose an authorized Trace Tree, Timeline, Replay, filtering, and per-Trace analysis dashboard. It does not add Prometheus, Grafana, alerts, a customer-visible Reasoning Trace, or a general observability platform.

## 1. Decision

Sprint 5 delivers one Agent Trace debugging product with four explicit layers:

1. the Agent Module produces validated Trace records and application-normalized Replay Attachments;
2. a Durable Outbox and Batch Sender move those records to a standalone Collector with at-least-once delivery;
3. the Collector retains immutable Trace authority, encrypted Replay Payloads, and rebuildable query projections;
4. an operator-only Admin Trace API and React dashboard expose Trace search, Trace Tree, Timeline, Replay, and per-Trace token, cost, and latency analysis.

The batch network destination is the Collector API, never a PostgreSQL protocol endpoint. PostgreSQL is the first Collector Store implementation. Local development may place the Application and Observability databases in one PostgreSQL container under separate databases and credentials. Production runs the Observability database on a separate instance so ingestion and Dashboard queries cannot contend with product transactions.

Prometheus and Grafana remain the future platform-metrics surface. The Sprint 5 Dashboard explains individual Agent Runs from Durable Agent Trace facts; it does not become a replacement metrics platform.

## 2. Source Documents And Superseding Decisions

This PRD derives from:

- `docs/product-discovery/CONTEXT.md`
- `docs/product-discovery/REQUIREMENTS.md`
- `docs/product-discovery/TECHNICAL-HANDOFF.md`
- `docs/technical-architecture/CONTEXT.md`
- `docs/technical-architecture/ARCHITECTURE.md`
- `docs/technical-architecture/adr/0001-target-operating-profile.md`
- `docs/technical-architecture/adr/0004-modular-monolith-with-workers.md`
- `docs/technical-architecture/adr/0005-use-s3-api-for-blob-storage.md`
- `docs/technical-architecture/adr/0006-use-postgresql-as-system-of-record.md`
- `docs/technical-architecture/adr/0022-use-rest-direct-blob-upload-and-sse.md`
- `docs/technical-architecture/adr/0029-separate-operational-telemetry-from-durable-agent-traces.md`
- `docs/technical-architecture/adr/0031-revoke-access-before-asynchronous-purge.md`
- `docs/sprint/SPRINT-4-PRD.md`
- `docs/implementation/sprint-4-agent-observability-runbook.md`
- `docs/superpowers/specs/2026-07-17-go-agent-observability-sdk-design.md`

If this PRD conflicts with an approved architecture or product decision, the approved source wins except for these deliberate Sprint 5 evolutions:

1. **Collector custody:** Durable Agent Trace semantic ownership remains with the Agent Module, but complete record storage moves from the Application PostgreSQL tables to a Collector-owned store after a required local Outbox append.
2. **Required delivery boundary:** an Agent recording call succeeds after the validated record is durably journaled locally; it does not wait for the remote Collector. Collector delivery remains required and unsampled, with bounded backlog and explicit backpressure rather than silent loss.
3. **Replay content:** Trace records continue to exclude raw prompts, responses, and Action Results. Sprint 5 separately captures encrypted, application-normalized Replay Payloads. Raw Bifrost or Provider envelopes, credentials, and hidden model reasoning remain prohibited.
4. **Independent payload retention:** Trace metadata follows the parent Run lifecycle. Replay Payloads have a shorter configurable TTL with a seven-day default.
5. **Administrative projection:** Durable Agent Trace remains distinct from a user-visible Reasoning Trace, but Sprint 5 adds an internal Operator projection and Dashboard.

## 3. Sprint Goal

Give a trusted Nano Notebook Operator one coherent answer to:

> What did this Agent Run observably execute, in what order, for how long, with which normalized model and Action inputs and outputs, at what reported token and cost totals, and how did recovery or failure change the physical execution?

The normal data flow is:

```text
Agent instrumentation
  -> validate record and Replay Attachment
  -> append record metadata and attachment reference to Durable Outbox
  -> Batch Sender forms ordered Trace Chunks
  -> authenticated Collector HTTP ingestion
  -> immutable Trace records in Observability PostgreSQL
  -> encrypted Replay Payloads in S3-compatible storage
  -> asynchronous Trace/Span query projection
  -> internal Collector Query API
  -> authorized Control Plane Admin Trace API
  -> React Trace Explorer and Trace Detail workspace
```

Application PostgreSQL is never queried by the Dashboard for Trace content. The browser never connects directly to the Collector, either PostgreSQL database, or object storage.

## 4. Success Criteria

Sprint 5 is complete only when all of the following are true:

1. Every Sprint 4 Trace record accepted by the required destination is durably journaled in an Application-owned Outbox before the recording call returns success.
2. Agent admission, Checkpoint, Lease fencing, publication, cancellation, and terminal transactions retain their current correctness while recording into the Outbox.
3. A Batch Sender creates bounded, ordered Trace Chunks and sends them to an authenticated standalone Collector API.
4. Collector ingestion is at-least-once and idempotent: an identical resend succeeds without a logical duplicate, while the same identity with different canonical content is rejected.
5. Collector ACK is issued only after the immutable records and required Replay Payload references for that Trace Chunk are durably committed.
6. One invalid Trace Chunk does not prevent valid chunks in the same HTTP batch from committing.
7. Temporary Collector, network, or ACK failure leaves Outbox data intact and retries with bounded exponential backoff and jitter.
8. Outbox exhaustion fails new required recording explicitly and never drops old records or silently changes delivery to best-effort.
9. Application and Observability PostgreSQL use separate databases, credentials, migrations, connection pools, and production instances.
10. Large Replay Payload bytes are not stored in PostgreSQL. They are compressed, envelope-encrypted, staged durably, and stored in S3-compatible object storage.
11. The Collector retains immutable canonical records and independently rebuildable Trace, Span, Event, Link, and summary projections.
12. An Operator can list and cursor-page Trace summaries filtered by time, Chat, Run, Agent, model, and status.
13. An Operator can open one Trace Tree, expand each Span, inspect attributes, Events, Links, status, and duration, and distinguish missing terminal observations from failures.
14. A synchronized Timeline renders each Span interval, Attempt boundary, recovery relation, and unfinished work without inventing missing timestamps.
15. The Trace detail view reports per-Trace and per-step latency, input/output/reasoning tokens when reported, and known cost without treating unknown cost as zero.
16. A separately authorized Operator can explicitly load exact application-normalized Model Request, Model Decision, Action input, and Action Result Replay Payloads.
17. Replay loading is audited durably and never exposes Provider envelopes, credentials, hidden chain-of-thought, object-store credentials, or unredacted known secrets.
18. Trace metadata remains viewable when a Replay Payload is expired, purged, unavailable, or unauthorized.
19. Parent Run, Chat, or Notebook deletion immediately tombstones the Collector projection and asynchronously purges Trace records and Replay objects.
20. The existing Sprint 4 Durable Trace data migrates to the Collector without hash or record-count drift before the Application-local record store is retired.
21. Existing Agent behavior, recovery, cancellation, Retry, deadlines, Publication Barrier, messages, and best-effort OpenTelemetry behavior remain unchanged.
22. Contract, integration, crash-recovery, security, UI, end-to-end, race, static-analysis, and capacity gates prove the complete workflow.

## 5. Canonical Terms

- **Collector:** the standalone internal service that authenticates Trace batches, validates delivery invariants, persists immutable records and Replay references, and serves an internal query contract. It does not authorize Agent execution or user operations.
- **Collector Store:** the replaceable persistence boundary behind the Collector. Sprint 5 implements PostgreSQL metadata/records plus S3-compatible Replay object storage.
- **Durable Outbox:** Application-owned transport journal that makes Collector delivery independent of process and network availability. It is not the long-term Dashboard data source.
- **Batch Sender:** the Worker component that claims ready Outbox data, forms bounded ordered batches, sends them to the Collector, handles ACKs, retries, and backlog limits, and reclaims abandoned delivery leases.
- **Batch:** one authenticated HTTP ingestion envelope containing one or more independently committed Trace Chunks.
- **Trace Chunk:** a contiguous sequence range for exactly one Trace, plus the Replay Attachment references required by those records.
- **Delivery Cursor:** the highest contiguous Trace sequence durably acknowledged by the Collector and reflected in the local Outbox state.
- **Replay Attachment:** encrypted, compressed, application-normalized content durably staged by the producer before Collector delivery.
- **Replay Payload:** the retained Collector-owned object and metadata created from a Replay Attachment.
- **Trace Projection:** rebuildable `obs_traces`, `obs_spans`, `obs_events`, and `obs_links` rows derived from immutable Collector records for query performance.
- **Trace Explorer:** the operator-only list and filter surface for locating Agent Runs.
- **Trace Detail Workspace:** the synchronized Trace Tree, Timeline, Inspector, Replay, and per-Trace analysis surface.
- **Platform Operator:** an authenticated Nano Notebook User with an explicit platform capability independent of Notebook membership roles.

`Session` remains authentication vocabulary and is not a Trace filter entity. The Dashboard filters by Chat and Agent Run. `Tool Call` remains Provider vocabulary; the Dashboard presents Nano Notebook Agent Actions and Model Decisions.

## 6. Acceptance Journey

The primary acceptance journey combines batching, recovery, Replay, and UI behavior:

1. An authorized User creates an Agent Run whose root Trace record and first Replay metadata are durably appended to the Outbox.
2. Attempt 1 invokes a model. The normalized request is encrypted and staged before the Model Call begins, and the returned normalized Model Decision is staged before later work consumes it.
3. The model proposes an ordered Action batch. Action input and accepted result Replay Payloads are recorded separately from safe Trace metadata.
4. The Worker process stops after one Action physically completes but before its terminal record is acknowledged by the Collector.
5. The Outbox survives. A Sender retry resubmits any uncertain Trace Chunk, and the Collector reconciles an identical duplicate.
6. Lease expiry produces Attempt 2 and the existing `continues` and `retries` Links. The Agent Run completes without accepting duplicate logical Action Results.
7. The Collector has one immutable logical copy of every record, the correct contiguous sequence, and encrypted Replay objects for every captured Model and Action boundary.
8. The projector derives the Trace Tree, Timeline intervals, token totals, known cost, and attempt relationships.
9. A Platform Operator filters by Run or model, opens the Trace, sees the incomplete physical execution and the repeated Action under distinct Attempts, and correlates Tree selection with the Timeline.
10. A read-only Trace Operator sees metadata but cannot load Replay.
11. A Replay-authorized Operator explicitly loads a Model Call request and decision; the Control Plane records the access audit and returns a `no-store` response.
12. Expiring one Replay Payload later leaves the Trace Tree, Timeline, and statistics intact and displays an explicit `expired` Replay state.
13. Deleting the parent Notebook commits a purge command, makes the Trace immediately unavailable, and eventually removes Collector records and Replay objects.

## 7. Ownership And Component Boundaries

### 7.1 Agent Observability SDK

The existing SDK continues to own:

- Trace, Span, Event, Link, and Trace Context record contracts;
- record identity, canonical payload, bounds, and validation;
- required versus best-effort destination dispatch;
- semantic-convention versions and conformance tests.

The SDK does not import Outbox, HTTP, PostgreSQL, S3, Collector, Control Plane, or React types. The existing `Exporter` call sites remain valid.

### 7.2 Agent Module

The Agent Module continues to own:

- one Trace and root Span per Agent Run;
- Nano semantic conventions and record meaning;
- lease-fenced and transaction-coupled recording points;
- normalized Replay classes and capture policy;
- Trace identity references needed for Retry and cross-Trace Links;
- Run lifecycle and deletion commands.

It replaces the long-term Application PostgreSQL exporter with a required Outbox Exporter. It never treats Collector state as Agent execution authority.

### 7.3 Durable Outbox And Batch Sender

The Outbox and Sender own only transport concerns:

- durable append and local sequence/lifecycle validation;
- ready, leased, acknowledged, quarantined, and purged delivery states;
- batch count, byte, and delay thresholds;
- HTTP authentication, compression, retry, and backoff;
- ACK reconciliation and backlog limits;
- staging-object cleanup after ACK.

They do not build Dashboard projections, interpret Agent outcomes, authorize Operators, or expose browser APIs.

### 7.4 Collector Core

The Collector core owns:

- batch and Trace Chunk protocol decoding;
- service authentication and producer identity;
- schema/version negotiation;
- contiguous sequence, identity, hash, parent, and Link validation;
- independent per-Trace-Chunk transactions;
- immutable record and Replay reference persistence;
- ACK and typed rejection responses;
- query and projector extension interfaces.

The core does not read Application PostgreSQL, authorize Users, know Notebook roles, or infer Agent product state from logs.

### 7.5 Nano Trace Projection Adapter

The Nano projection adapter understands the allow-listed Nano Agent semantic conventions needed to derive:

- Run, Chat, Notebook, Agent, model, status, and time filter columns;
- Trace and Span start, terminal status, and duration;
- Attempt, Model Call, Agent Action, Event, and Link presentation;
- token, cost, and latency summaries;
- Replay Attachment relationship and display class.

It cannot mutate raw records. Projection rows may be dropped and rebuilt from the immutable store.

### 7.6 Control Plane Admin Trace Module

The Control Plane owns:

- Platform Operator capability checks;
- public Admin Trace REST response contracts;
- cursor validation and request bounds;
- Replay access audit and decryption authorization;
- safe error localization and `Cache-Control` behavior;
- mapping internal Collector results to browser-facing view models.

The browser never receives Collector service credentials, SQL details, object keys, encryption metadata, or presigned object URLs.

### 7.7 Web Client

The React client owns:

- Trace Explorer filters, cursor pagination, loading, empty, error, and stale states;
- the synchronized Trace Tree and Timeline;
- Inspector tabs and explicit Replay load interaction;
- accessible per-Trace token, cost, and latency charts;
- active-Trace polling while a Trace remains non-terminal.

It does not reconstruct canonical Trace lifecycle from raw records or cache Replay Payloads persistently.

## 8. Deployment Topology

### 8.1 Local Development

```text
native Control Plane + Worker
       |
       +--> Application database `nano` in local PostgreSQL container
       |      - product data
       |      - Trace identity references
       |      - Durable Outbox
       |
       +--> native or Compose Collector process over localhost HTTP
              |
              +--> Observability database `nano_observability`
              |      in the same local PostgreSQL container
              `--> MinIO Replay bucket
```

Application and Collector migrations, database users, and connection strings remain separate even when one local container hosts both databases.

### 8.2 Production

```text
Control Plane + Worker fleet
       |
       +--> managed Application PostgreSQL
       |      - product authority + Durable Outbox
       |
       `--> authenticated TLS Collector API
              |
              +--> Collector replicas
              +--> separate Observability PostgreSQL instance
              `--> S3 Replay bucket
```

Changing PostgreSQL instance, adding a query replica, partitioning records, or replacing the Collector Store does not change SDK calls, the batch protocol, or the Admin Trace API.

Sprint 5 does not introduce Kafka, a managed queue, Kubernetes, ClickHouse, or a second buffering system. Operational evidence must justify those later changes.

## 9. Producer Data Model

Application PostgreSQL retains minimal Trace identity and transport state.

### 9.1 `agent_trace_refs`

| Column | Meaning |
| --- | --- |
| `run_id` | One-to-one parent Agent Run |
| `trace_id` | Stable Trace identity |
| `root_span_id` | Stable root Span for Retry Links |
| `schema_version` | Trace record schema |
| `semantic_convention_version` | Nano semantic version |
| `next_sequence_no` | Next local sequence assigned under lock |
| `terminal_observed` | Whether the local producer accepted a root terminal record |
| `collector_cursor` | Highest contiguous sequence acknowledged by Collector |
| `created_at` / `terminal_at` | Lifecycle timestamps |

The reference survives Outbox cleanup for as long as the Run exists so explicit Retry never needs a synchronous Collector lookup.

### 9.2 `agentobs_outbox_records`

| Column | Meaning |
| --- | --- |
| `trace_id`, `sequence_no` | Ordered Trace identity |
| `identity_key` | Stable idempotency identity |
| `record_kind` | Span start/end, Event, or Link |
| `canonical_payload` | Bounded canonical SDK record bytes |
| `payload_sha256` | Reconciliation hash |
| `attachment_ids` | Required Replay Attachments for this record |
| `delivery_state` | `ready`, `leased`, `acknowledged`, or `quarantined` |
| `lease_token`, `lease_expires_at` | Sender recovery authority |
| `attempt_count`, `next_attempt_at` | Retry scheduling |
| `last_error_code` | Bounded safe diagnostic |
| `created_at`, `acknowledged_at` | Transport timestamps |

Unique constraints cover `(trace_id, sequence_no)` and `(trace_id, identity_key)`. The Outbox append applies the same lifecycle, hash, parentage, Link, and per-Trace limits as the Sprint 4 durable exporter before returning success.

### 9.3 `agentobs_outbox_attachments`

PostgreSQL stores only attachment metadata:

- opaque `attachment_id` and owning Trace/record identity;
- payload class and schema version;
- staging object key, ciphertext size, ciphertext SHA-256, encryption key ID, and compression;
- ready, acknowledged, quarantined, and cleanup state;
- expiry policy and safe error code.

Ciphertext bytes are staged in a producer-owned S3-compatible prefix before the referencing Outbox record commits. A failed database commit may leave an unreferenced staging object; a bounded sweeper removes those orphans. PostgreSQL never stores Prompt, Response, or Action payload bytes.

### 9.4 `agentobs_outbox_commands`

Lifecycle commands that are not Trace records use a separate ordered transport table:

- opaque command identity and version;
- command kind, initially `purge_trace`;
- Trace and parent Run identity;
- ready, leased, acknowledged, or quarantined delivery state;
- lease, retry, safe error, and timestamp fields matching record delivery.

Parent deletion commits `purge_trace` in the same Application transaction as the product deletion tombstone. A purge command has priority over unsent records for the same Trace. The Sender stops ordinary delivery for that Trace, removes its unacknowledged local record and staging content after the purge ACK, and retains only the minimum Trace reference needed to reject reuse.

## 10. Required Outbox Recording Semantics

The required recording path preserves the Sprint 4 rules:

1. validate the SDK record and normalized Replay metadata before append;
2. validate current Run and, where applicable, Lease authority;
3. allocate one contiguous `sequence_no` under the Trace reference lock;
4. reconcile a duplicate `identity_key` by canonical hash;
5. commit the Outbox record in the owning domain transaction whenever the observation describes that transaction;
6. return success after the local commit, independently of Collector availability;
7. never use Collector delivery as Run, Checkpoint, Lease, cancellation, or publication authority.

Required start records commit before the Model Call or Agent Action starts. Required Model Decision and Action Result Replay Attachments commit before that result is accepted or used by later Agent work. A deterministic record or Replay validation failure remains `agent_trace_invalid`. Transient local persistence failure follows existing Lease recovery and exhaustion behavior.

Best-effort OpenTelemetry remains a separate SDK destination. Collector delivery success cannot compensate for OpenTelemetry failure, and OpenTelemetry success cannot compensate for local Outbox failure.

## 11. Batch Formation And Sender Runtime

### 11.1 Three Bounded Thresholds

The Sender forms a batch when any configured threshold is reached:

| Threshold | Sprint 5 default |
| --- | ---: |
| Records | 128 |
| Encoded record bytes | 512 KiB |
| Oldest ready-record delay | 250 ms |

The values are application configuration, not protocol constants. A terminal Trace record makes its Trace Chunk immediately eligible without weakening durability if shutdown interrupts delivery.

### 11.2 Trace Chunk Rules

- One Trace Chunk contains records from exactly one Trace.
- Records are ordered by contiguous `sequence_no`.
- `first_sequence` must equal the local Collector cursor plus one unless the chunk is a full or partial identical resend.
- A Batch may contain several Trace Chunks up to its count and byte limits.
- Cross-Trace Link dependencies are delivered before the referencing chunk.
- Replay Attachments are uploaded and acknowledged before a Trace Chunk that requires their references is eligible for commit.
- Multiple Sender instances may claim Outbox work with expiring leases, but only one active lease advances one Trace at a time.

### 11.3 Retry And Reclaim

- timeout, connection error, `429`, and `5xx` retain the same records and retry with exponential backoff and jitter;
- an ACK lost after Collector commit causes an identical resend and idempotent ACK;
- expired Sender leases are reclaimed without changing record identities or sequence;
- typed retryable Collector errors include sequence gaps, unresolved dependencies, and temporarily unavailable storage;
- typed permanent errors include unsupported schema, invalid canonical payload, and identity/hash conflict;
- permanent errors quarantine only the affected Trace and emit a bounded high-priority diagnostic;
- quarantined data is never automatically skipped or marked delivered.

### 11.4 Backpressure

Backlog is bounded by both row count and staged ciphertext bytes. The initial configurable hard defaults are 100,000 unacknowledged records and 1 GiB of staged Replay ciphertext per producer deployment. Crossing either hard limit makes new required recordings fail explicitly. The Sender does not delete oldest data, sample traces, or downgrade delivery.

Warnings begin before the hard limit and are emitted through structured logs and existing Operational Telemetry. Prometheus metrics and alerts are deferred.

## 12. Collector Ingestion Protocol

### 12.1 Service Boundary

The Collector exposes internal versioned HTTP endpoints:

```text
POST /internal/agent-observability/v1/attachments
POST /internal/agent-observability/v1/batches
POST /internal/agent-observability/v1/purges
GET  /internal/agent-observability/v1/health
```

Production requires TLS and a scoped service credential. Local development uses a repository-owned non-secret local credential. The Collector authenticates a configured `producer_id`; it never accepts producer or tenant identity from browser input.

### 12.2 Attachment Upload

An Attachment request carries:

- `attachment_id`, Trace and record identity;
- payload class and schema version;
- compression, ciphertext length, ciphertext SHA-256, and encryption key ID;
- opaque ciphertext bytes under a strict request limit.

The Collector writes the object idempotently to its Replay bucket and persists metadata. The same identity and hash returns success. A conflicting hash returns a permanent identity conflict. The Collector never decrypts the object.

### 12.3 Batch Envelope

One Batch contains:

- protocol version;
- opaque `batch_id`;
- authenticated `producer_id`;
- creation time and compression metadata;
- one or more Trace Chunks.

Each Trace Chunk contains:

- `trace_id`, schema and semantic-convention versions;
- `first_sequence` and `last_sequence`;
- canonical ordered records with identity and hash;
- required Replay Attachment IDs.

### 12.4 Commit And ACK

The Collector processes Trace Chunks independently:

1. decode and bound the Batch before allocation;
2. validate each chunk's versions, contiguous sequence, canonical hashes, identity, parentage, Link dependencies, and attachment references;
3. lock the Collector Trace envelope;
4. reconcile any already committed prefix;
5. append the new immutable suffix and update the committed high-watermark in one PostgreSQL transaction;
6. enqueue projection work in that transaction;
7. return the committed high-watermark or a typed rejection for each chunk.

The HTTP request may return a mixed result: valid Trace Chunks commit while invalid chunks reject. A Batch transport ID is diagnostic; Trace identity, sequence, identity key, and canonical hash define logical idempotency.

### 12.5 Purge Command

The purge endpoint accepts one or more idempotent versioned `purge_trace` commands. The Collector commits a tombstone before returning ACK. An identical command or a purge for an already tombstoned Trace returns success. Later Attachment or Trace Chunk delivery for that Trace is rejected as permanently tombstoned and cannot recreate data.

## 13. Replay Capture And Governance

### 13.1 Captured Classes

Sprint 5 captures only application-normalized payloads:

- `model_request`: ordered system, user, assistant, and accepted Action Result messages sent through the Models boundary;
- `model_decision`: exactly one normalized Final Draft or ordered Action Proposal batch returned by the Models boundary;
- `action_input`: canonical typed Agent Action input accepted for execution;
- `action_result`: canonical typed success or expected domain error accepted by the Controller.

The Replay view may label these as Prompt, Response/Decision, Action Input, and Action Result. It does not claim to reproduce Provider-internal retries, routing, or hidden reasoning.

### 13.2 Prohibited Content

Replay capture never stores:

- raw Bifrost or Provider request/response envelopes;
- Provider request IDs or private gateway fields unless separately approved as safe metadata;
- authorization headers, cookies, session tokens, Lease Tokens, API keys, credentials, or infrastructure secrets;
- hidden chain-of-thought or a claim to expose model cognition;
- arbitrary process environment or logs.

Known structured secret fields are removed before serialization. User-authored content is intentionally retained for Replay and is treated as sensitive; it is not described as secret-redacted merely because credentials were removed.

### 13.3 Encoding And Encryption

Replay Payloads are:

1. encoded under a versioned class-specific schema;
2. bounded before allocation and compression;
3. compressed;
4. envelope-encrypted at the producer;
5. integrity-protected by ciphertext SHA-256;
6. staged durably before the referencing Outbox commit.

The Collector stores opaque ciphertext and cannot decrypt it. Local development uses a repository-owned development key provider. Production uses a configured KMS-backed key provider. Object keys are opaque and are never sent to the browser.

Initial configurable bounds are 2 MiB ciphertext per Attachment, 32 Replay Attachments per Trace, and 16 MiB Replay ciphertext per Trace. Bounds must remain above the product's accepted Model and Action context limits. Oversize content fails before the corresponding Model or Action boundary rather than producing an unmarked partial Replay.

### 13.4 Retention

- Trace metadata and immutable records follow the parent Run, Chat, and Notebook lifecycle.
- Replay Payloads expire after seven days by default, configurable only by deployment policy.
- Expiry deletes ciphertext and retains a non-content reference state of `expired` so the UI can explain absence.
- Parent deletion overrides TTL, tombstones access immediately, and schedules object removal.
- Sprint 5 adds no per-user retention controls or legal-hold product.

## 14. Collector Storage Model

### 14.1 Immutable Authority

`obs_traces` stores one Collector envelope per Trace:

- Trace, Run, Chat, and Notebook identity;
- schema and semantic-convention versions;
- root Span identity;
- committed and projected high-watermarks;
- first and last observed timestamps;
- tombstone and purge state;
- producer identity and ingestion timestamps.

`obs_trace_records` stores the immutable canonical sequence:

- Trace and sequence identity;
- stable identity key and record kind;
- Span, parent, and Link target identities;
- name and observed timestamp;
- canonical safe payload and SHA-256;
- ingestion batch and timestamp.

Constraints enforce unique Trace sequence, unique Trace identity key, identical resend reconciliation, Span lifecycle shape, same-Trace parent resolution, and typed Link resolution.

`obs_payload_refs` stores only Replay metadata, object identity, ciphertext integrity, class, key ID, retention state, and timestamps.

### 14.2 Rebuildable Projections

The asynchronous projector owns:

- `obs_trace_summaries` for filters, status, duration, model set, token totals, known cost, Attempt count, and projection freshness;
- `obs_spans` for parent-child Tree nodes, start/end observations, duration, status, safe attributes, and Replay references;
- `obs_events` for ordered instantaneous facts;
- `obs_links` for retry, continuation, and causal relationships.

Collector ACK does not wait for projection success. Projection work has its own durable queue and retry state. `projected_sequence < committed_sequence` is an explicit lag state returned by the Query API. Projection tables may be truncated and deterministically rebuilt from raw records without changing Trace authority.

### 14.3 Index Policy

Indexes are limited to actual query dimensions:

- descending observed start time plus Trace identity for cursor paging;
- Run, Chat, and Notebook identity;
- Agent name, model name, terminal status, and active/terminal state;
- Trace/Span parent and Link targets;
- projection queue readiness and Replay expiry.

Sprint 5 does not add a generic GIN index over arbitrary JSON attributes, arbitrary full-text search, or an observability query language.

## 15. Collector Query Contract

The Collector Query API is internal and accepts only Control Plane service authentication.

### 15.1 Trace List

The list query supports bounded filters:

- `started_after` and `started_before`;
- exact or prefix-safe Chat, Run, and Trace identity lookup;
- exact Agent name, model name, and status;
- active versus terminal Trace;
- opaque cursor and bounded page size.

It returns typed summaries, a next cursor, and projection freshness. It never returns Replay content or arbitrary attributes.

### 15.2 Trace Detail

One detail response contains:

- Trace summary and committed/projected high-watermarks;
- ordered Span nodes with parent identity, timestamps, status, duration, safe summary attributes, and Replay availability;
- Events and Links;
- per-Trace and per-Model-Call token, known-cost, and latency analysis;
- explicit active, incomplete, projection-lagged, tombstoned, or invalid state.

The API does not return raw canonical record blobs to the browser-facing Control Plane contract.

### 15.3 Replay Retrieval

Replay retrieval addresses one Trace, Span, and Replay reference. The Collector verifies the reference belongs to a non-tombstoned Trace and returns opaque ciphertext plus encoding metadata to the authorized Control Plane. The Collector does not decrypt content or decide User capability.

## 16. Platform Operator Authorization

Sprint 5 introduces explicit platform capabilities independent of Notebook roles:

- `platform.trace.read`: list and read safe Trace summaries, Tree, Timeline, attributes, Events, Links, and analysis;
- `platform.trace.replay`: additionally request sensitive Replay Payloads.

An authenticated User needs `platform.trace.read` to enter any `/admin/traces` route. Replay requires both capabilities. Notebook Owner, Editor, or Viewer role alone grants neither capability.

Capabilities are stored as durable grants to User identities and managed through a repository-owned CLI or bootstrap command. Sprint 5 adds no browser UI for granting platform capabilities and no organizations, teams, or enterprise administration model.

The Control Plane authenticates the browser session, authorizes the platform capability, calls the internal Collector using a service credential, and shapes the response. The Collector never trusts a browser-supplied Operator identity.

### 16.1 Replay Access Audit

Before returning Replay content, the Control Plane durably records:

- Operator User identity;
- Trace, Span, and Replay reference identity;
- Replay class;
- request timestamp and outcome;
- safe denial or failure code when applicable.

The audit record contains no Replay content, object key, ciphertext, or credential. Successful Replay responses set `Cache-Control: no-store` and are never written to application logs.

## 17. Admin Trace REST API

The browser-facing Control Plane exposes:

```text
GET /api/admin/traces
GET /api/admin/traces/{trace_id}
GET /api/admin/traces/{trace_id}/replay/{replay_id}
```

The existing authenticated identity response adds safe platform capability names so the Web Client can hide or show Admin navigation. Server authorization remains authoritative when navigation is hidden.

List and detail responses use explicit versioned JSON view models. Cursor, page-size, time-range, and identifier bounds are validated by the Control Plane. Internal Collector failures map to stable safe errors such as:

- `trace_not_found`;
- `trace_projection_pending`;
- `trace_temporarily_unavailable`;
- `replay_forbidden`;
- `replay_expired`;
- `replay_unavailable`.

Active Trace detail is polled at a bounded interval and polling stops after a terminal state. Sprint 5 adds no Trace SSE or WebSocket channel.

## 18. Dashboard Information Architecture

### 18.1 Trace Explorer

Route: `/admin/traces`

The page provides:

- time-range filter;
- Chat, Run, or Trace identity search;
- Agent, model, status, and active/terminal filters;
- cursor-paged results ordered by newest observed start;
- started time, Run/Chat identity, status, Agent, model, total latency, token total, and known/unknown cost columns;
- projection-lag and incomplete indicators;
- loading, empty, forbidden, unavailable, and retry states.

The Explorer contains no fleet-wide time-series charts. Platform trends belong to the future Prometheus/Grafana scope.

### 18.2 Trace Detail Workspace

Route: `/admin/traces/{trace_id}`

The header displays Run, Chat, Trace, Agent, model set, status, start/end observations, total duration, Attempt count, token totals, and known cost.

The main workspace contains:

1. a horizontal Timeline aligned to the root Trace time range;
2. an expandable Trace Tree with status and duration per Span;
3. a synchronized Inspector for the selected Span;
4. per-step latency, token, and cost analysis.

Selecting a Tree node highlights the matching Timeline interval. Selecting a Timeline interval selects and reveals the matching Tree node. Unclosed Spans extend to the last observed Trace time with an explicit unfinished style and no fabricated terminal status.

### 18.3 Timeline

The Timeline:

- preserves parent-child indentation and Attempt boundaries;
- positions start and end using observed timestamps;
- distinguishes completed, error, cancelled, and unfinished Spans;
- makes `continues`, `retries`, and cross-Trace Links navigable;
- supports bounded zoom/reset for long Traces;
- never infers missing duration from Checkpoints or current time after a Trace is terminal.

### 18.4 Inspector And Replay

Inspector tabs are:

- `Overview`: name, kind, status, timestamps, duration, safe error kind, token/cost summary;
- `Replay`: explicit on-demand Model or Action Replay load;
- `Attributes`: bounded safe semantic attributes;
- `Events & Links`: ordered Events and causal Links.

Replay is not prefetched. The explicit load action explains that sensitive user content will be accessed. The UI differentiates available, loading, forbidden, expired, purged, unavailable, and corrupt Payload states without hiding the underlying Span.

### 18.5 Per-Trace Analysis

Charts remain scoped to the selected Trace:

- total versus Model versus Action versus other observed latency;
- input, output, cached, and reasoning tokens by Model Call when reported;
- known cost by Model Call and Provider-reported currency/source;
- unknown usage or cost displayed as unknown, never zero.

Charts include accessible text/table equivalents and do not require a general charting or metrics backend.

## 19. Projection And UI Consistency

- `committed_sequence == projected_sequence` means the current Collector projection includes every accepted raw record.
- `projected_sequence < committed_sequence` means projection is stale; the Dashboard labels it and may poll.
- an active Trace with equal watermarks is complete only through its latest observed record, not terminal;
- a started Span without a terminal record remains unfinished after process loss;
- a terminal root Span does not allow the projector to fabricate child terminal records;
- projection failure never rolls back or deletes immutable records;
- projection rebuild produces byte-equivalent typed view models for the same schema version;
- invalid or unsupported historical records remain diagnosable and do not silently disappear from counts.

## 20. Failure Semantics

### 20.1 Producer And Sender Failure

- local Outbox append failure is a required recording failure and follows existing Agent recovery rules;
- Collector unavailability does not roll back already committed Agent state;
- Sender process loss leaves leased rows reclaimable after expiry;
- ACK loss causes identical resend;
- hard backlog exhaustion prevents new required Agent observations;
- a permanent Collector rejection quarantines one Trace and surfaces a bounded diagnostic;
- no failure path deletes unacknowledged Outbox records.

### 20.2 Collector And Store Failure

- request decoding failure commits nothing;
- one invalid Trace Chunk does not poison valid chunks;
- object write failure does not acknowledge the Attachment;
- database failure before commit returns retryable failure;
- commit success followed by response loss reconciles on resend;
- object success followed by metadata failure may leave an orphan object, removed by a bounded sweeper;
- raw-record commit success followed by projection failure leaves the committed cursor ahead of the projected cursor and retries projection;
- Collector shutdown stops admission, drains in-flight chunk transactions within a deadline, and never sends speculative ACKs.

### 20.3 Query And Replay Failure

- a lagging projection returns current projection plus explicit freshness, or a stable pending response when no safe projection exists;
- unavailable Replay never makes safe Trace metadata unavailable;
- a decryption, integrity, or schema failure returns `replay_unavailable`, records a safe audit outcome, and never returns partial plaintext;
- forbidden access returns no existence-sensitive Replay detail;
- tombstoned Trace access returns unavailable immediately even while physical purge remains pending.

## 21. Deletion, Expiry, And Purge

Parent deletion writes an Agent Observability purge command in the same Application transaction that establishes the product deletion tombstone. The Batch Sender delivers purge commands through an authenticated Collector control contract.

Collector purge follows this order:

1. idempotently persist a Trace tombstone;
2. make Query and Replay APIs reject access;
3. remove Replay objects and projection rows asynchronously;
4. remove immutable Trace records after dependent cleanup;
5. retain only the minimum non-content tombstone needed to reject replayed delivery and repeated purge.

Replay TTL expiry uses the same object cleanup machinery but does not tombstone the Trace. It changes only the Replay reference to `expired`.

Application Outbox content is eligible for cleanup only when:

- every record through the terminal sequence is acknowledged;
- every referenced Attachment is acknowledged or explicitly purged;
- no Sender lease is active;
- the stable `agent_trace_refs` identity mapping remains.

## 22. Operational Telemetry Boundary

Durable Agent Trace and Operational Telemetry remain separate:

| Concern | Durable Collector Path | Operational Telemetry |
| --- | --- | --- |
| Purpose | exact Agent Run reconstruction and Replay | service health and performance diagnosis |
| Delivery | required local journal, at-least-once remote | best-effort, sampleable |
| Storage | Collector PostgreSQL + S3 | future metrics/log/trace backend |
| User content | encrypted Replay Store under explicit access | prohibited |
| Dashboard | Sprint 5 Agent Trace Dashboard | future Grafana dashboards |
| Authority | observed Agent execution history | never Agent execution authority |

Sprint 5 emits structured logs and existing OpenTelemetry spans for Collector HTTP, batch delay, retry, projection lag, and purge operations. It does not add Prometheus exporters, scrape configuration, Grafana dashboards, alerts, or SLOs.

Future Prometheus metrics may derive aggregate counts and histograms from Collector operations or Trace projections. They must not become the source for Trace Tree, Replay, or exact per-Run history.

## 23. Migration From Sprint 4

The target operating profile permits a maintenance window. Sprint 5 uses one explicit cutover instead of indefinite dual write.

### 23.1 Cutover Sequence

1. deploy the Collector databases, object bucket, service credential, schema, and health checks;
2. add Application Trace references, Durable Outbox, Sender leases, and Replay staging support while the Sprint 4 exporter remains active;
3. stop Agent admission and drain active Agent Jobs for a bounded maintenance window;
4. backfill each existing `agent_traces` envelope and ordered `agent_trace_records` sequence as a sealed migration Trace Chunk;
5. compare Trace count, record count, identity keys, sequence ranges, and canonical hashes between Application and Collector stores;
6. create or verify stable `agent_trace_refs` for every existing Run;
7. switch the required SDK destination to the Outbox Exporter and enable Collector batching;
8. run one new Agent acceptance journey and verify Collector/query/UI consistency;
9. retire the Application-local Trace record read path and revoke application access to the old record tables;
10. remove old Trace record tables only after the migration verifier and full rollback window pass.

### 23.2 Compatibility

- Sprint 4 record schema and semantic-convention versions remain readable by the Collector;
- migration does not reconstruct missing records, Replay Payloads, or terminal observations;
- historical Sprint 4 Traces show `Replay not captured` rather than a fabricated Payload state;
- active pre-cutover Runs are not split between storage paths because admission is paused and Jobs drain before switching;
- no Dashboard endpoint falls back to querying the legacy Application Trace tables.

## 24. Capacity And Performance

The design targets the architecture's approximately 100 registered Users and 10 concurrent Agent or Source-processing Jobs without assuming hyperscale infrastructure.

### 24.1 Required Capacity Gate

Under 10 concurrent Agent Jobs, each producing up to the existing 256-record and 1 MiB safe Trace-record limits:

- no record or required Replay Attachment is lost;
- Collector logical duplicates remain zero under injected ACK loss;
- the Outbox drains after Collector recovery without manual repair;
- Application Agent admission, Checkpoint, and publication p95 latency regresses by no more than 10 percent from the Sprint 4 required exporter baseline;
- Collector ingestion and Dashboard queries run against a separate Observability PostgreSQL instance in the production-shaped test profile.

With 100,000 seeded Trace summaries and bounded detail records:

- Trace list query p95 is below 500 ms at the Control Plane boundary;
- one maximum-sized Trace detail query p95 is below 1 second;
- cursor pagination remains stable under concurrent new ingestion;
- Replay bytes are fetched only on explicit request and do not affect list/detail latency.

### 24.2 PostgreSQL Policy

- Collector inserts one Trace Chunk in a bounded transaction using batch-capable PostgreSQL operations;
- Dashboard reads typed projection columns, not arbitrary JSON scans;
- indexes match only supported filters and projector work queues;
- Replay bytes remain outside PostgreSQL;
- connection pools are bounded independently for ingestion, projection, and query paths;
- table partitioning and read replicas are allowed later without changing contracts but are not required before measurements justify them.

## 25. Verification

### 25.1 SDK And Outbox Contracts

- existing Agent Observability SDK conformance tests continue to pass;
- identical required record replay reconciles;
- conflicting identity/hash fails;
- sequence, lifecycle, parent, Link, per-record, and per-Trace bounds match Sprint 4;
- domain-coupled observations commit or roll back with their owning transactions;
- staged Attachment without Outbox commit is cleaned as an orphan;
- Outbox commit never references a missing staging object.

### 25.2 Batch Sender

- record-count, encoded-byte, and oldest-delay thresholds each flush a batch;
- chunks preserve per-Trace contiguous order;
- multiple Traces share a Batch without sharing transaction fate;
- delivery leases prevent simultaneous advancement of one Trace and recover after Sender loss;
- timeout, `429`, `5xx`, and ACK loss retry the same identities;
- hard row and staged-byte bounds fail closed;
- shutdown and Force Flush preserve all unacknowledged data.

### 25.3 Collector Contract

- service authentication and request bounds reject unauthorized or oversized traffic before persistence;
- identical Attachment and Trace Chunk resend is idempotent;
- identity/hash conflict is permanent and isolated;
- sequence gap and missing dependency are retryable;
- one invalid chunk does not block valid chunks;
- mixed batch ACKs report exact committed high-watermarks;
- unsupported versions remain explicit;
- shutdown never acknowledges uncommitted data.

### 25.4 PostgreSQL And S3 Integration

Fault injection covers:

- before and after Attachment object write;
- before and after Attachment metadata commit;
- before and after raw record commit;
- after commit but before ACK response;
- before local cursor advance;
- during projection update;
- during Replay expiry and parent purge.

Recovery proves no logical duplicates, no missing acknowledged records, bounded orphan cleanup, rebuildable projection, and immediate tombstone enforcement.

### 25.5 Security And Governance

- ordinary authenticated User, Notebook Owner, Editor, and Viewer cannot access Admin Trace APIs without platform grants;
- `platform.trace.read` cannot load Replay;
- `platform.trace.replay` access is durably audited;
- Replay responses use `no-store` and never enter logs;
- object keys, ciphertext, credentials, Lease Tokens, Provider envelopes, and hidden reasoning never enter browser-safe Trace responses;
- deletion and expiry revoke access under race with concurrent query;
- tampered ciphertext, digest, and schema fail without partial plaintext.

### 25.6 Query And Projection

- projector produces the expected Tree for completed, failed, cancelled, recovered, retried, and unfinished Runs;
- identical raw records rebuild identical view models;
- committed/projected watermark lag is visible;
- filters use Chat, Run, Agent, model, status, and time correctly;
- cursor pagination has no duplicates or skips under concurrent ingest;
- unknown token or cost values remain unknown;
- malformed historical data is diagnosable rather than silently omitted.

### 25.7 Web Client

- Platform Operator navigation and route authorization states;
- Trace Explorer filter, cursor, loading, empty, forbidden, stale, and unavailable behavior;
- Tree expand/collapse and status representation;
- Tree/Timeline bidirectional selection;
- unfinished Span rendering without fabricated end;
- Inspector tabs, Events, Links, and cross-Trace navigation;
- explicit Replay loading and forbidden/expired/unavailable states;
- token, cost, and latency charts with accessible text equivalents;
- active polling stops after terminal state;
- browser refresh preserves URL-addressable Trace state without persisting Replay content.

### 25.8 End To End And Regression

- the deterministic process-loss acceptance journey reaches the expected Dashboard state;
- the real configured Qwen multi-Action journey produces complete Tree, Timeline, Replay, and analysis data;
- Stop, deadline, recovery exhaustion, Retry, and parent deletion produce the specified states;
- `scripts/test-go` and relevant race/vet gates pass;
- Web unit tests, type-check, lint, build, and Playwright tests pass;
- existing Sprint 1 through Sprint 4 regression journeys remain green;
- the capacity gate in Section 24 passes against a production-shaped split-database profile.

## 26. Explicitly Out Of Scope

- Prometheus exporters, scrape configuration, PromQL, Grafana dashboards, or alerts
- platform-wide time-series charts in the Sprint 5 Dashboard
- a customer-visible Reasoning Trace or Notebook-member execution history
- presenting Replay as hidden chain-of-thought or exact Provider-internal execution
- raw Bifrost or Provider request/response storage
- arbitrary logs, infrastructure metrics, or OpenTelemetry backend replacement
- public Collector ingestion, customer API keys, multi-tenancy product, or untrusted producers
- Python, TypeScript, or additional SDKs
- Kafka, NATS, Redis streams, ClickHouse, Elasticsearch, or a general event bus
- Kubernetes, multi-region replication, or an HA/SLA promise
- arbitrary Trace query language, full-text Prompt search, or JSON attribute search
- live Trace SSE, WebSockets, alerts, anomaly detection, or incident management
- enterprise administration UI, organizations, teams, billing, legal hold, or user-configurable retention
- changing Agent Run, Job, Checkpoint, Lease, Publication Barrier, or cancellation authority
- using Trace or Replay records to resume Agent execution
- fabricating historical Sprint 4 Replay data

## 27. Delivery Sequence

Implementation preserves independently verifiable seams in this order:

1. Collector protocol types, Store interfaces, conformance fixtures, and separate configuration;
2. Collector PostgreSQL schema, immutable ingestion, idempotent ACK contract, and query health;
3. producer Trace references, Durable Outbox schema, migration of Sprint 4 exporter validation, and transaction tests;
4. Batch Sender leases, thresholds, retry/backoff, cursor reconciliation, and backpressure;
5. Replay class codecs, producer encryption/staging, Collector opaque object persistence, expiry, and purge;
6. Nano Trace projector, typed summary/Tree/Timeline/analysis view models, lag, and rebuild;
7. Platform Operator grants, CLI/bootstrap, internal Collector query client, Admin Trace APIs, and Replay audit/decryption;
8. Trace Explorer UI with filters and cursor paging;
9. Trace Detail Tree, synchronized Timeline, Inspector, Replay, and per-Trace analysis;
10. Sprint 4 backfill verifier, maintenance cutover, and legacy storage retirement;
11. failure-injection, deletion, security, browser, real-model, regression, and capacity gates;
12. production-shaped runbook covering separate databases, S3, credentials, encryption keys, migration, health, backlog, projection rebuild, and recovery.

Each step must leave existing Agent correctness tests green or deliberately replace them with stronger equivalent coverage. No step may introduce a second Dashboard data path, synchronous Agent dependence on Collector availability, silent Trace loss, plaintext Replay storage, or coupling between future Prometheus/Grafana metrics and Durable Agent Trace authority.

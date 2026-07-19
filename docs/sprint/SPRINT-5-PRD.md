# Nano Notebook Sprint 5 PRD

## Document Status

- **Sprint:** Sprint 5
- **Status:** Approved for implementation
- **Date:** 2026-07-19
- **Theme:** Isolated Agent Trace collection, batching, Replay, and operator Dashboard
- **Delivery boundary:** Agent processes batch diagnostic Trace records directly to a
  standalone Collector. Collector owns the Trace database and Replay objects. The
  Application database retains only Agent recovery authority, a lightweight Trace
  identity anchor, low-volume purge intent, and access audit.

## 1. Decision

Sprint 5 delivers one operator-only Agent Trace debugging product:

1. the Agent Module produces validated Trace records and normalized Replay payloads;
2. a bounded per-process memory exporter batches Trace records to an authenticated
   standalone Collector API;
3. Collector stores immutable raw authority and projections in a dedicated
   Observability PostgreSQL instance and encrypted Replay in S3-compatible storage;
4. Control Plane exposes authorized Admin Trace APIs backed only by Collector Query;
5. the React Dashboard exposes Trace Tree, Timeline, Replay, filters, and per-Trace
   Token, Cost, and Latency analysis.

The full Trace stream is diagnostic and must not be written to Application
PostgreSQL. It may lose a bounded unsent tail during abrupt process failure or a
prolonged Collector outage. Runs, Jobs, leases, Checkpoints, publication, and
cancellation remain durable product authority and are never reconstructed from Trace.

Kafka, SQS, NATS, Redis, and a local disk spool are not introduced. Prometheus and
Grafana remain future platform-metrics scope.

## 2. Superseding Decisions

This PRD supersedes the earlier Application-owned full Trace Outbox design and its
100,000-record capacity mechanism. The approved detailed design is
`docs/superpowers/specs/2026-07-19-direct-trace-delivery-design.md`.

The following decisions are authoritative for Sprint 5:

1. **Product authority:** Application PostgreSQL stores only data required to resume,
   fence, publish, cancel, delete, or audit product work.
2. **Diagnostic delivery:** Trace delivery is best effort with bounded memory,
   authenticated batching, retry while resident, and explicit incomplete states.
3. **Collector authority:** Dashboard Trace content comes exclusively from Collector.
4. **Physical isolation:** local and production Observability PostgreSQL run in a
   process/instance independent from Application PostgreSQL.
5. **Replay custody:** normalized Replay is encrypted before leaving the producer;
   plaintext, raw Provider envelopes, credentials, and hidden reasoning are prohibited.
6. **Deletion authority:** low-volume purge intent remains durably journaled because
   losing deletion is a governance failure, not an acceptable diagnostic gap.
7. **KMS deferral:** Sprint 5 preserves `KeyProvider` and a development provider;
   production Replay remains disabled until production key custody is implemented.

## 3. Sprint Goal

Give a trusted Operator one coherent answer to:

> What did this Agent Run observably execute, in what order, for how long, with which
> normalized model and Action inputs and outputs, at what reported Token and Cost
> totals, and how did recovery or failure change the physical execution?

Normal data flow:

```text
Agent instrumentation
  -> transaction-local Trace buffer where required
  -> bounded process memory queue
  -> authenticated gzip HTTP batches
  -> standalone Collector
  -> Observability PostgreSQL raw authority + projections
  -> encrypted Replay objects in S3-compatible storage
  -> Collector Query API
  -> authorized Control Plane Admin API
  -> React Trace Explorer and Detail workspace
```

Application PostgreSQL is never queried by Dashboard for Trace content. Browser never
connects directly to Collector, PostgreSQL, or object storage.

## 4. Success Criteria

Sprint 5 is complete only when:

1. Agent admission, Checkpoint, lease fencing, publication, cancellation, deadline,
   Stop, Retry, and recovery correctness remain unchanged.
2. Emitting any number of full Trace records creates no Application PostgreSQL Trace
   content rows and does not make product success depend on Collector availability.
3. Transaction-coupled observations are offered only after product commit; rollback
   discards them.
4. A bounded memory exporter flushes on record count, encoded bytes, or maximum delay.
5. Overflow retains the queued prefix, drops new diagnostic records, and emits bounded
   structured diagnostics without failing product work.
6. Retryable transport failures and uncertain ACKs resend the same identities while
   the batch remains in memory.
7. Collector ingestion is idempotent by `(trace_id, identity_key, canonical_hash)`;
   identical resend succeeds and conflicting content is rejected in isolation.
8. Collector assigns monotonic per-Trace storage sequence; producers do not coordinate
   global Trace sequence through Application PostgreSQL.
9. Collector ACK occurs only after raw records and required Replay references commit.
10. One invalid record or Trace does not prevent unrelated valid records from committing.
11. Abrupt producer loss, overflow, and flush timeout produce an explicit incomplete
    Trace rather than fabricated observations.
12. Local and production Application/Observability PostgreSQL use separate services or
    instances, credentials, databases, migrations, pools, and volumes.
13. Replay bytes remain outside PostgreSQL, encrypted and compressed in S3-compatible
    custody, with seven-day default retention.
14. Operator can list and cursor-page Trace summaries filtered by time, Chat/Run/Trace,
    Agent, model, and status.
15. Operator can expand Trace Tree Spans and inspect status, duration, attributes,
    Events, Links, and incomplete structure.
16. Timeline synchronizes with Tree selection and renders observed duration without
    inventing missing terminal timestamps.
17. Per-Trace analysis reports Token, Cost, and Latency, preserving unknown values.
18. Replay-authorized Operator can explicitly load normalized Prompt, Response, Tool
    input, and Tool result records; every attempt is audited and returned `no-store`.
19. Trace metadata remains usable when Replay is forbidden, expired, corrupt, purged,
    or unavailable.
20. Parent deletion durably records purge intent, immediately revokes Query access,
    and eventually removes Collector raw/projection/object content.
21. Sprint 4 Trace history migrates without identity, record-count, or canonical-hash
    drift before legacy Application Trace storage is retired.
22. Go, race, security, integration, capacity, Web, accessibility, browser, and Sprint
    1-4 regression gates prove the workflow.

## 5. Canonical Terms

- **Trace Identity Anchor:** one lightweight Application row mapping a Run to stable
  Trace/root Span identity; it contains no Trace delivery state or record content.
- **Transaction Trace Buffer:** memory owned by one product transaction; discarded on
  rollback and offered to the exporter only after commit.
- **Memory Batch Exporter:** bounded per-process queue, batch former, HTTP sender,
  retry loop, `ForceFlush`, and graceful shutdown implementation.
- **Batch:** authenticated HTTP envelope containing records from one or more Traces.
- **Collector:** standalone internal ingest/query service and only owner of Trace store
  credentials.
- **Collector Sequence:** monotonic per-Trace sequence assigned after Collector
  identity reconciliation; never an Application delivery cursor.
- **Replay Attachment:** encrypted normalized payload staged by producer and referenced
  by one Trace record.
- **Trace Projection:** rebuildable Tree, Timeline, Event, Link, Replay reference, and
  summary rows derived from Collector raw authority.
- **Incomplete Trace:** a Trace with an observed delivery gap, unresolved structure, or
  missing terminal observation; not equivalent to a failed Agent Run.
- **Purge Command Outbox:** low-volume durable control queue for deletion only.
- **Platform Operator:** authenticated User with explicit Trace platform capability,
  independent of Notebook role.

`Session` remains authentication vocabulary. Trace filters use Chat and Agent Run.
Dashboard labels normalized Agent Actions as Tool calls where helpful but does not claim
to expose Provider-internal execution.

## 6. Ownership And Boundaries

### 6.1 Agent Observability SDK

Owns record types, identity keys, canonical hashing, limits, semantic conventions,
Tracer APIs, and conformance tests. It remains independent of PostgreSQL, HTTP,
Collector, S3, Control Plane, and React.

### 6.2 Agent Module

Owns Run-to-Trace identity, deterministic cross-transaction Span identity, observation
meaning, normalized Replay classes, transaction buffers, and product lifecycle hooks.
Collector state is never Agent execution authority.

### 6.3 Memory Batch Exporter

Owns queue bounds, batching thresholds, gzip HTTP, service authentication, retry,
uncertain ACK reconciliation, diagnostics, `ForceFlush`, and shutdown. It owns no SQL
schema, Dashboard projection, Operator authorization, or disk persistence.

### 6.4 Collector

Owns protocol validation, identity reconciliation, sequence assignment, tombstones,
immutable raw storage, Replay object custody, projections, internal Query API, and
typed ACK/rejection results. It does not read Application PostgreSQL.

### 6.5 Control Plane And Web

Control Plane owns User authentication, platform capabilities, Replay audit/decryption,
safe public API models, and Collector Query credentials. Web owns Trace Explorer,
Tree, Timeline, Inspector, Replay interaction, charts, and accessible state. Neither
receives Observability database credentials.

## 7. Producer Persistence Model

Application PostgreSQL retains:

### 7.1 `agent_trace_refs`

| Column | Meaning |
|---|---|
| `trace_id`, `run_id` | Stable one-to-one identity |
| `chat_id`, `notebook_id` | Correlation and purge scope |
| `root_span_id` | Stable root parent identity |
| `agent_name` | Producer semantic identity |
| `schema_version` | Record schema |
| `semantic_convention_version` | Nano convention schema |
| `created_at` | Anchor creation time |

It has no record sequence, Collector cursor, Sender lease, retry, quarantine, capacity,
or staged Replay byte counter.

### 7.2 `agentobs_outbox_commands`

One immutable idempotent command per product deletion, with retry/lease/ACK metadata.
This is the only remaining durable Agent-observability transport table in Application
PostgreSQL. Its volume follows deletions, not Trace records.

### 7.3 Retired Runtime Tables

Normal runtime code must not read or write:

- `agentobs_outbox_records`;
- producer Replay staging metadata tables;
- full Trace capacity/counter/slot tables;
- Trace delivery cursor, retry, lease, and quarantine columns.

Restoring a pre-cutover backup requires the previous release in an isolated
environment; current runtime code has no compatibility reader for retired tables.

## 8. Transaction Trace Buffer

Observations created inside product transactions use a buffer:

1. validate record and canonical content in memory;
2. add it to a transaction-local ordered buffer;
3. execute the authoritative Application transaction;
4. discard on rollback;
5. after successful commit, offer buffered records to the process exporter;
6. report enqueue failure diagnostically without changing product result.

A crash between steps 3 and 5 can lose the observation. This is accepted and must not
be hidden by reconstructing a synthetic success record.

Span IDs that must survive transaction/process boundaries derive deterministically
from Trace ID plus semantic identity key. This removes historical Trace-record lookups
from Application PostgreSQL.

## 9. Memory Batching And Delivery

Default limits:

| Limit | Default |
|---|---:|
| Pending records | 10,000 |
| Pending encoded bytes | 32 MiB |
| Batch records | 128 |
| Batch encoded bytes | 512 KiB |
| Maximum delay | 250 ms |
| HTTP request body | 2 MiB |
| Shutdown flush deadline | 10 s |

Rules:

- enqueue is non-blocking after validation;
- FIFO preserves queued prefixes; overflow drops new records;
- one batch may contain multiple Traces without shared transaction fate;
- retryable failures use bounded exponential backoff and jitter;
- `401/403` is configuration-fatal and not hot-looped;
- `429`, `5xx`, timeout, connection loss, and uncertain ACK retry in memory;
- `ForceFlush` waits for all records accepted before its call or returns context error;
- shutdown rejects new enqueue, flushes to deadline, then reports remaining loss;
- no background component writes a local disk spool or business database Outbox.

## 10. Collector Ingestion Protocol

Each Batch carries protocol version, unique Batch ID, producer instance ID, creation
time, and independently reconcilable Trace groups. Each record carries Trace descriptor,
identity key, canonical payload/hash, and optional Replay Attachment descriptor.

For each Trace under a Collector-owned serialization boundary:

1. reject an existing tombstone;
2. reconcile immutable Trace descriptor;
3. treat same identity/hash as duplicate success;
4. reject same identity/different hash as permanent conflict;
5. validate supported record and semantic schema;
6. assign new records monotonically increasing Collector sequences;
7. persist raw records and Replay metadata;
8. commit before ACK;
9. enqueue asynchronous projection.

Out-of-order semantic dependencies are retained as explicit unresolved structure and
may converge when later records arrive. Missing root/parent/terminal never causes the
projector to fabricate data. Tombstones always prevent resurrection.

## 11. Replay Capture And Governance

Captured application-normalized classes:

- Model Request: selected system instructions, conversation messages, model/options;
- Model Decision: assistant content, normalized usage, ordered Action proposals;
- Action Input: validated Action name and arguments;
- Action Result: accepted bounded result returned to the model.

Prohibited:

- raw Bifrost or Provider envelopes;
- authorization headers, API keys, cookies, DSNs, and credentials;
- hidden model reasoning or chain-of-thought;
- arbitrary process logs and environment variables;
- unbounded binary content.

Producer serializes a versioned allow-list, redacts known secrets, compresses, envelope
encrypts, and writes ciphertext to a staging object. No Replay bytes or staging metadata
enter Application PostgreSQL. Collector verifies and takes permanent S3 custody before
ACK. Orphan staging objects expire through bounded sweeping.

Default Replay TTL is seven days. Trace metadata follows parent lifecycle. Production
Replay stays disabled until a production `KeyProvider` and operational policy exist.

## 12. Collector Storage And Deployment

Collector PostgreSQL stores immutable Trace descriptors/records/tombstones, Replay
references, projection work, and typed query projections. Replay bytes remain in object
storage. Projection can be rebuilt from raw authority.

### 12.1 Local

```text
application-postgres:55432
  database nano, volume application-postgres-data

observability-postgres:55433
  database nano_observability, volume observability-postgres-data
```

They are distinct containers/processes with separate credentials, migrations, pools,
health checks, and volumes. Stopping Observability PostgreSQL must not consume or block
the Application pool.

### 12.2 Production

Collector uses a dedicated Observability PostgreSQL instance and S3 bucket. Control
Plane and Worker know only Collector HTTP endpoints/tokens. Replacing the Collector
Store later does not change Agent or Dashboard contracts.

## 13. Query, Authorization, And Public API

Internal Collector Query supports bounded cursor list, one typed Trace detail, and one
opaque Replay retrieval. Supported filters are time, Chat, Run, Trace, Agent, model,
status, and active/terminal state.

Control Plane exposes:

```text
GET /api/admin/traces
GET /api/admin/traces/{trace_id}
GET /api/admin/traces/{trace_id}/replay/{replay_id}
```

`platform.trace.read` authorizes metadata. `platform.trace.replay` separately authorizes
Replay. Notebook Owner/Editor/Viewer roles do not imply either capability.

Every Replay attempt writes Operator identity, Trace/Span/Replay identity, class, time,
outcome, and safe failure code to Application audit. Response is `Cache-Control:
no-store`; logs contain no content, object key, key material, or ciphertext.

## 14. Dashboard

Route `/admin/traces` provides cursor paging and filters with loading, empty, forbidden,
unavailable, stale, and incomplete states.

Route `/admin/traces/{trace_id}` provides:

- summary header with Run, Chat, Agent, models, status, time, Attempts, Token, Cost,
  and Latency;
- expandable Trace Tree and Span status/duration;
- synchronized Timeline with Attempt boundaries and unfinished intervals;
- Inspector Overview, Attributes, Events & Links, and explicit Replay tabs;
- normalized Prompt, Response, Tool input, and Tool result Replay;
- per-step Token, Cost, and Latency charts with accessible table/text equivalents.

Replay is never prefetched or persistently cached. Unknown cost/usage remains unknown,
not zero. Dashboard contains no fleet-wide time-series platform metrics.

## 15. Failure Semantics

### 15.1 Producer

- product commit succeeds independently from Trace enqueue/delivery;
- rollback emits no transaction-coupled record;
- queue overflow, shutdown deadline, or abrupt loss can create incomplete Trace;
- retry while memory-resident is at-least-once;
- permanent validation errors are surfaced before enqueue;
- permanent Collector identity conflict affects only conflicting identity;
- no diagnostic delivery failure blocks Agent recovery.

### 15.2 Collector

- invalid request envelope commits nothing;
- invalid Trace group does not poison unrelated groups;
- object/metadata/raw failure before commit returns retryable failure;
- response loss after commit reconciles on resend;
- projection failure leaves raw authority committed and retries;
- shutdown drains in-flight transactions and never speculatively ACKs.

### 15.3 Query And Replay

- projection lag is explicit;
- missing observations are explicit incomplete state;
- Replay failure never hides safe Trace metadata;
- integrity/decryption/schema failure returns no partial plaintext;
- tombstoned Trace is unavailable before asynchronous physical purge.

## 16. Deletion, Expiry, And Purge

Parent deletion commits product tombstone and one purge command in the same Application
transaction. The low-volume command Sender delivers it until Collector ACKs.

Collector:

1. persists idempotent Trace tombstone;
2. immediately rejects Query and Replay;
3. asynchronously removes Replay objects and projections;
4. removes raw Trace records after dependent cleanup;
5. retains minimum non-content tombstone needed to prevent resurrection.

Replay TTL expiry removes only Replay custody and preserves Trace metadata.

## 17. Operational Telemetry Boundary

| Concern | Agent Trace product | Operational telemetry |
|---|---|---|
| Purpose | per-Run debugging and Replay | service health/performance |
| Delivery | bounded best-effort memory batch | best-effort/sampleable |
| Storage | Collector PostgreSQL + S3 | future metrics/log/trace backend |
| User content | encrypted explicit Replay | prohibited |
| UI | Sprint 5 Dashboard | future Grafana |
| Authority | observed diagnostic history | never execution authority |

Sprint 5 emits structured logs and OpenTelemetry spans for queue depth/drop, batching,
HTTP/retry, ingestion, projection lag, Replay, and purge. It does not add Prometheus
exporters, scrape configuration, Grafana dashboards, alerts, or SLOs.

## 18. Migration And Cutover

One maintenance cutover, no indefinite dual write:

1. deploy identity-based Collector ingestion and tolerant projection;
2. deploy independent Observability PostgreSQL and object buckets;
3. stop Agent admission and drain active Jobs;
4. deliver existing Outbox backlog and verify Collector identity/count/hash authority;
5. migrate remaining Sprint 4 historical Trace records;
6. switch Control Plane/Worker to memory exporter;
7. retain only lightweight Trace anchors, purge commands, and Replay access audit;
8. remove full Trace Outbox, staging metadata, capacity, cursor, lease, retry, and
   quarantine runtime schema;
9. verify Application writes do not scale with emitted Trace record count;
10. resume admission and execute the acceptance journey.

Historical Sprint 4 Replay remains `not captured`. Dashboard never falls back to legacy
Application Trace tables.

## 19. Capacity And Performance

Target: approximately 100 registered Users and 10 concurrent Agent/Source Jobs.

Under 10 concurrent Jobs producing 2,540 total Trace records and normalized Replay:

- Application PostgreSQL contains zero full Trace/replay staging content rows;
- queue never exceeds 10,000 records or 32 MiB;
- product transactions continue when Collector/Observability PostgreSQL is stopped;
- after recovery, memory-resident records ingest idempotently under ACK loss;
- a simulated process loss produces only documented incomplete diagnostic state;
- admission, Checkpoint, and publication p95 no longer grows with Trace record count.

With 100,000 Collector summaries and a maximum 256-Span detail:

- list p95 below 500 ms at Control Plane boundary;
- detail p95 below 1 second;
- cursor paging stable under new ingest;
- Replay bytes fetched only on explicit request.

## 20. Verification

### 20.1 Producer And Database Isolation

- transaction buffer commit/rollback tests;
- deterministic Span identity across transaction/process restart;
- count/byte/delay batching RED/GREEN tests;
- overflow, retry, uncertain ACK, ForceFlush, shutdown, and race tests;
- SQL assertion that full Trace count does not affect Application writes;
- two-container Compose lifecycle and independent failure test.

### 20.2 Collector

- auth/request bounds;
- duplicate identity/hash idempotency and conflict isolation;
- concurrent producer monotonic sequence;
- out-of-order dependency/incomplete/convergence projection;
- mixed Batch results and commit-before-ACK;
- Replay custody and orphan cleanup;
- tombstone/query race and no resurrection;
- projection rebuild equivalence.

### 20.3 Security And Dashboard

- platform capability matrix and durable Replay audit;
- no-store/no-secret/no-hidden-reasoning checks;
- deletion/expiry concurrent access revocation;
- Trace Explorer filters/cursor/states;
- Tree/Timeline synchronization and incomplete rendering;
- Inspector, Replay, Token/Cost/Latency accessible views;
- active polling termination and URL state;
- Playwright acceptance in supported viewports.

### 20.4 Regression

- real configured model multi-Action journey when credentials are available;
- deterministic process-loss/recovery journey;
- Stop, deadline, Retry, recovery exhaustion, and deletion states;
- `scripts/test-go`, targeted `-race`, vet, format, and builds;
- Web unit, typecheck, lint, build, accessibility, and browser gates;
- Sprint 1-4 regression journeys.

## 21. Explicitly Out Of Scope

- Kafka, SQS, NATS, Redis Streams, or another message bus
- local disk spool and zero-loss diagnostic guarantee
- ClickHouse, Elasticsearch, arbitrary query language, or full-text Prompt search
- Prometheus/Grafana, fleet-wide time-series charts, alerts, and SLOs
- public Collector ingestion, customer API keys, or untrusted producer multi-tenancy
- customer-visible Reasoning Trace or hidden chain-of-thought
- raw Provider/Bifrost payload storage
- Python/TypeScript/additional tracing SDKs
- Kubernetes, multi-region replication, or HA/SLA promise
- production KMS adapter and enabling production Replay
- using Trace/Replay to resume Agent execution

## 22. Delivery Sequence

1. freeze this PRD and direct-delivery design;
2. add Collector identity-based protocol and server-assigned sequence through TDD;
3. add bounded memory batch exporter through TDD;
4. refactor Agent Trace identity and transaction buffers without changing product
   authority;
5. remove full Trace/replay staging runtime writes from Application PostgreSQL;
6. retain and isolate low-volume purge command delivery;
7. split local Observability PostgreSQL into its own service/volume;
8. migrate/drain legacy Trace authority and update runbook;
9. rerun Collector Query and existing Dashboard acceptance;
10. execute isolation, loss, capacity, security, race, real-model, Web, and Sprint 1-4
    regression gates;
11. audit every success criterion against evidence before completion.

Every step is an atomic commit and leaves the repository coherent. No step may add a
second Dashboard data path, synchronous product dependence on Collector, plaintext
Replay storage, or coupling between future Prometheus/Grafana and Trace authority.

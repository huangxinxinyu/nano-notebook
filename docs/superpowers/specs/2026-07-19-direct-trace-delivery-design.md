# Direct Agent Trace Delivery Design

**Date:** 2026-07-19

**Status:** Approved for implementation

**Scope:** Replace the Application PostgreSQL full Trace Outbox with bounded
in-memory batching to the standalone Collector while preserving the existing
Dashboard and Collector-owned query store.

## 1. Decision

Nano Notebook will deliver Agent Trace observations directly from each producer
process to the Collector through a bounded in-memory batch exporter.

```text
Control Plane / Worker
  ├── Application PostgreSQL
  │     └── Run, Job, lease, Checkpoint, publication, cancellation,
  │         Trace identity anchor, and low-volume purge intent
  └── bounded in-memory Trace exporter
        └── authenticated batched HTTP
              └── Collector
                    ├── dedicated Observability PostgreSQL
                    └── S3-compatible Replay object storage

Dashboard
  └── Control Plane Admin API
        └── Collector Query API
              └── Observability PostgreSQL / Replay object storage
```

Kafka, SQS, NATS, Redis, a disk spool, and a full Trace Outbox are outside this
Sprint. The Dashboard read path remains unchanged.

## 2. Product Authority And Loss Contract

Application PostgreSQL remains authoritative for work that the Agent must
recover or resume:

- Runs and Jobs;
- lease and attempt state;
- durable Checkpoints and Action anchors;
- publication and cancellation state;
- the stable Trace ID and root Span ID associated with a Run;
- low-volume deletion/purge intent whose loss could violate data governance;
- Operator Replay access audit.

Full Trace records are diagnostic data, not a correctness prerequisite. A Trace
write must never roll back, delay, or reject a valid product transaction. The
following loss is explicitly accepted:

- abrupt process termination may lose records that have not reached Collector;
- a full in-memory queue drops newly produced diagnostic records;
- shutdown flush expiry may leave an incomplete Trace;
- a network outage longer than the in-memory retention window may create gaps.

The Collector and Dashboard expose such Traces as incomplete. They do not
fabricate missing steps or infer hidden reasoning from product state.

Durable Checkpoints are unaffected. Agent recovery uses Application PostgreSQL,
not the Trace history.

## 3. Producer Components

### 3.1 Trace Identity Anchor

`agent_trace_refs` becomes a small business-adjacent identity table. It retains
only stable envelope fields needed to correlate a Run with Collector data:

- `trace_id`, `run_id`, `chat_id`, and `notebook_id`;
- `root_span_id`, Agent name, schema version, and semantic convention version;
- creation time and optional terminal time.

Delivery cursors, retry counters, leases, sequence counters, capacity state, and
quarantine state are removed. The table contains one row per Run rather than one
row per observation.

Span IDs used across transactions are derived deterministically from the Trace
ID and semantic identity key. This removes the need to read historical Trace
records from Application PostgreSQL merely to recover a parent or Retry Link.

### 3.2 Transaction Trace Buffer

Code that emits observations while a product transaction is open writes them to
a transaction-local memory buffer. It does not perform Trace SQL writes and does
not call Collector before the transaction outcome is known.

- On rollback, the buffer is discarded.
- After a successful commit, records are offered to the process exporter.
- Failure to enqueue is reported as a structured diagnostic and does not change
  the committed product result.
- A crash after product commit but before enqueue may lose those records under
  the accepted loss contract.

This preserves truthful observation boundaries without recreating distributed
transaction coupling.

### 3.3 Bounded Batch Exporter

Each Control Plane and Worker process owns one exporter instance. The default
limits are:

| Limit | Default |
|---|---:|
| Pending records | 10,000 |
| Pending encoded bytes | 32 MiB |
| Batch records | 128 |
| Batch encoded bytes | 512 KiB |
| Maximum batch delay | 250 ms |
| HTTP request body | 2 MiB |
| Graceful shutdown flush | 10 s |

Enqueue is bounded and non-blocking. When either pending limit is exhausted, the
new record is dropped and one rate-limited structured diagnostic reports the
drop count and reason. Existing queued prefixes are retained, making incomplete
tails visible instead of evicting roots unpredictably.

The exporter drains FIFO records into multi-Trace batches. It gzip-compresses the
existing authenticated Collector HTTP payload, retries retryable transport and
server failures with bounded exponential backoff and jitter, and resends after
an uncertain ACK. Collector idempotency makes resend safe.

`ForceFlush` waits for records accepted before the call. `Shutdown` stops new
enqueue, flushes within the caller deadline, then reports the number of records
that could not be delivered. Neither operation persists a local spool.

### 3.4 Replay Payloads

Replay remains application-normalized and encrypted before leaving the producer.
Ciphertext is staged in the existing S3-compatible staging bucket; only its
descriptor enters the memory batch. Replay staging metadata is not written to
Application PostgreSQL.

Collector verifies the descriptor, takes permanent object custody, and stores
the reference in Observability PostgreSQL. If the producer dies before delivery,
the existing age-based orphan sweep removes the unreferenced staging object.
KMS integration remains deferred; production Replay remains disabled until a
production `KeyProvider` and policy are supplied.

## 4. Collector Ingestion Contract

The direct-delivery protocol identifies a record by `(trace_id, identity_key)`
and canonical hash. Client-owned globally contiguous sequence numbers are no
longer required because independent processes cannot safely coordinate them
without a database or broker.

For each Trace, Collector serializes ingestion and:

1. creates or reconciles the immutable Trace descriptor;
2. treats an existing identity with the same canonical hash as an idempotent
   resend;
3. rejects an existing identity with a different hash as an identity conflict;
4. assigns a monotonically increasing Collector sequence to each new identity;
5. commits records and Replay metadata before acknowledging the batch;
6. projects Tree, Timeline, Replay references, Token, Cost, and Latency from
   Collector-owned raw authority.

Records may arrive after their parent or terminal peer, and a process crash may
leave a Trace without a root or terminal record. Projection therefore tolerates
missing dependencies, marks unresolved structure explicitly, and converges when
later records arrive. A tombstone always wins over later ingestion.

Batch IDs remain unique delivery-attempt identifiers. Producer IDs identify the
process instance for diagnostics, not ordering authority.

## 5. Required Durable Control Path

Deletion differs from diagnostic observation: losing a purge request could
retain data beyond product authority. Sprint 5 therefore keeps a small durable
control-command Outbox in Application PostgreSQL only for Trace purge intent.

The table contains one command per deletion event, not one row per Trace record.
The existing sender retries commands until Collector acknowledges its durable
tombstone. This low-frequency control path is isolated from Trace batching and
does not participate in Agent admission, Checkpoint, or publication latency.

## 6. Storage And Deployment Isolation

The Collector store is PostgreSQL in Sprint 5, but it is not the Application
database.

Local Compose runs two PostgreSQL services with separate processes, ports,
volumes, users, databases, migrations, and connection budgets:

```text
application-postgres     nano                 product authority
observability-postgres   nano_observability   Collector raw/projection data
```

Production uses a dedicated Observability PostgreSQL instance. Collector is the
only writer and query service with database credentials. Control Plane and the
browser have no Observability DSN.

If measured Trace scale later justifies ClickHouse or another store, the
Collector Store implementation may change behind the ingestion and query APIs;
the Agent producer and Dashboard contracts remain stable.

## 7. Dashboard Boundary

The implemented Dashboard remains the Sprint 5 Trace product:

- Trace Explorer and Session/Agent/model filters;
- expandable Trace Tree and Span Inspector;
- synchronized Timeline;
- explicit Prompt, Response, and Tool Replay;
- per-Trace Token, Cost, and Latency analysis.

Its read path remains:

```text
Browser -> Control Plane Admin API -> Collector Query API
        -> Observability PostgreSQL / Replay object storage
```

It never queries Application PostgreSQL Trace content, producer memory queues,
or the purge-command Outbox. Prometheus and Grafana remain future platform
metrics scope.

## 8. Migration And Cutover

Cutover is one-way and has no indefinite dual write:

1. deploy Collector identity-based ingestion and tolerant projection;
2. deploy the independent local Observability PostgreSQL service;
3. stop new Agent admission for a maintenance window;
4. drain the existing full Trace Outbox and verify Collector raw authority;
5. switch Control Plane and Worker to the bounded memory exporter;
6. retain only the lightweight Trace identity anchor and purge commands;
7. remove full Trace, Replay staging-metadata, capacity, lease, cursor, and
   quarantine tables/triggers from Application PostgreSQL;
8. resume admission and verify product PG writes no longer scale with Trace
   record count.

Legacy migration utilities may remain for restoration of pre-cutover backups,
but normal runtime code cannot read or write the retired tables.

## 9. Error Handling And Diagnostics

- Validation and canonicalization errors are programming/data-contract errors
  and are surfaced to the caller before enqueue.
- Queue capacity, Collector unavailability, retry exhaustion, and shutdown loss
  are best-effort delivery diagnostics; they do not fail product work.
- HTTP `401/403` is non-retryable until configuration changes and produces a
  high-severity structured log.
- HTTP `429`, `5xx`, timeouts, connection loss, and uncertain ACKs are retryable
  while the batch remains in memory.
- Collector identity conflict quarantines only the conflicting record in
  Collector-owned storage/diagnostics; unrelated records continue.
- Purge-command failure remains durable and retryable in Application PostgreSQL.

Sprint 5 emits structured logs and OpenTelemetry spans for queue depth, dropped
records, batch delay, request outcome, retry, Collector projection lag, and
purge. Prometheus exporters, Grafana dashboards, alert rules, and SLOs remain out
of scope.

## 10. Verification

### 10.1 Producer

- a product transaction writes no full Trace row;
- rollback discards its transaction buffer;
- commit publishes its buffered records without making the product result depend
  on delivery;
- pending record and byte limits never exceed configured bounds;
- overflow retains the queued prefix and reports exact drop counts;
- count, byte, and delay thresholds each trigger a batch;
- retry, uncertain ACK, `ForceFlush`, shutdown deadline, and concurrent enqueue
  are race-tested;
- hard process loss is documented and projects an incomplete Trace after restart.

### 10.2 Collector And Dashboard

- duplicate identity and hash is idempotent;
- conflicting hash is rejected without corrupting the Trace;
- concurrent producers receive Collector-owned monotonic sequences;
- out-of-order/missing parents are explicit and later converge;
- tombstones prevent resurrection;
- Replay object custody, expiry, audit, and purge remain secure;
- Dashboard Tree, Timeline, Replay, filters, and analysis work exclusively through
  Collector Query.

### 10.3 Isolation And Capacity

- local Compose proves separate PostgreSQL processes and volumes;
- stopping Observability PostgreSQL does not consume or block the Application
  connection pool and product work continues;
- a ten-Job, 2,540-record profile produces no Application Trace-content rows and
  stays within the configured in-memory bound;
- Application admission, Checkpoint, and publication latency no longer scales
  with the number of emitted Trace records;
- 100,000 Collector summaries and a 256-Span detail retain the existing query
  latency gates;
- all Sprint 1-4 regression, Go race, Web, accessibility, and browser gates pass.

## 11. Deferred Evolution

The following require measured need and a separate decision:

- Kafka, SQS, NATS, Redis Streams, or another durable transport;
- a local disk spool;
- ClickHouse or another Collector Store;
- Prometheus/Grafana platform metrics;
- production KMS integration and Replay enablement;
- customer-visible Reasoning Trace.

# Application Outbox Database And Capacity Design

**Date:** 2026-07-19

**Status:** Superseded by `2026-07-19-direct-trace-delivery-design.md`

**Scope:** Sprint 5 Durable Outbox placement and the exact 100,000-record backlog bound

> This design records an earlier decision and is retained for history. The user
> subsequently changed the product requirement: full Trace data is diagnostic,
> may lose a bounded in-memory tail on process failure, and must not be journaled
> in the core Application PostgreSQL database. The replacement design removes the
> full Trace Outbox and its 100,000-record capacity mechanism.

## 1. Decision

Keep the Durable Outbox in the Application PostgreSQL database, in Agent-observability-owned tables. Keep the Collector's immutable Trace store and query projections in a separate Observability PostgreSQL database and a separate production instance.

Replace the current singleton record counter with reusable capacity-slot rows. The default deployment owns exactly 100,000 slots. Every unacknowledged Outbox record transactionally owns one distinct slot; deleting the record releases that slot. This retains the exact hard limit without making unrelated Agent transactions update one shared counter row.

## 2. Why The Outbox Stays With Product Authority

A required Trace observation often describes a product transaction. Admission creates the Run, Job, Trace reference, root Span, and admission Event together. Checkpoint, lease, cancellation, publication, and terminal observations likewise belong to the transaction that changes the authoritative product state.

Keeping the Outbox in the Application database provides one PostgreSQL commit boundary:

```text
Application transaction
  ├── product Run / Job / Message / Checkpoint change
  └── required Trace record in Durable Outbox
```

The transaction either commits both facts or neither fact. A separate Outbox database would introduce a dual-write outcome in which product state and required Trace history disagree. A direct Kafka or SQS write has the same atomicity problem and would still require a local Outbox or database-log CDC.

This placement does not make the Application database the Trace query store. Outbox records are temporary transport custody. The Batch Sender sends them to the Collector, and acknowledged terminal records are removed. The Dashboard reads only the Collector Query API.

## 3. Deployment Boundary

### 3.1 Local Development

One PostgreSQL container may host two databases:

```text
PostgreSQL container
  ├── nano                 Application data + Durable Outbox
  └── nano_observability   Collector Trace store + query projections
```

The databases retain separate credentials, migrations, and connection pools even when they share a container.

### 3.2 Production

```text
Control Plane + Worker fleet
  └── managed Application PostgreSQL
        ├── product authority
        └── temporary Durable Outbox

Collector replicas
  └── separate Observability PostgreSQL instance
        ├── immutable Trace records
        └── rebuildable Dashboard projections
```

Collector ingestion and Dashboard queries cannot consume the Application pool or contend directly with product tables. Moving or replacing the Collector Store does not change Agent transactions or the batch HTTP protocol.

## 4. Alternatives Rejected

### 4.1 Separate Outbox Database

This provides physical isolation but loses the single transaction that makes required recording reliable. Distributed transactions would add a larger correctness and operations burden than the workload justifies.

### 4.2 Kafka Or Managed Queue In Sprint 5

An external broker can improve transport scale later, but it does not atomically join a PostgreSQL product transaction. The Application would still need an Outbox or CDC bridge. Sprint 5 has no measured need for the additional service and failure modes.

### 4.3 One Global Counter Row

The existing trigger updates one `current_records` value for every insert. PostgreSQL retains that row lock until the owning product transaction commits. Ten independent Traces therefore serialize admission, Checkpoint, and publication. The measured p95 regression exceeds the Sprint 5 limit even though PostgreSQL, the product tables, and Collector are otherwise healthy.

### 4.4 Fixed Quota Buckets

Buckets reduce collisions but create skew, fallback probing, lock-order, and limit-redistribution problems. A full bucket can reject early while other buckets remain free unless the transaction searches and locks additional buckets. That is unnecessary complexity for an exact global bound.

## 5. Exact Reusable-Slot Model

### 5.1 Tables

The record limit and Replay-byte limit remain owner-controlled configuration. Record occupancy moves out of the singleton configuration row.

```text
agentobs_outbox_limits
  singleton
  max_records = 100000
  max_staged_ciphertext_bytes = 1 GiB
  current_staged_ciphertext_bytes

agentobs_outbox_record_slots
  slot_id
  occupied
  updated_at

agentobs_outbox_records
  trace_id
  sequence_no
  capacity_slot_id  UNIQUE -> agentobs_outbox_record_slots.slot_id
  ...canonical record fields...
```

The default migration creates slots `1..100000`. One committed Outbox record has exactly one occupied slot and one slot can belong to at most one record.

An exact read-only capacity status projects:

- configured maximum;
- committed occupied slots;
- remaining committed slots;
- staged Replay ciphertext usage.

Status is derived from slot occupancy. Recording transactions never update a shared record-count row.

### 5.2 Reservation

The existing record-validation trigger runs first. A capacity trigger then:

1. reads `max_records` without modifying the limit row;
2. chooses a free slot within that range using an indexed search and `FOR UPDATE SKIP LOCKED`;
3. marks only that slot occupied;
4. writes its identity into `NEW.capacity_slot_id`;
5. raises SQLSTATE `54000` with the existing bounded error when no slot can be reserved.

The search begins at a deterministic hash of Trace and sequence identity and wraps once. Concurrent Traces therefore start at different parts of the slot index. Locked slots represent in-flight reservations and are skipped; they count against concurrent admission until their transaction commits or rolls back.

If later validation, product work, or commit fails, PostgreSQL rolls back both the record and its slot update. There is no leak-repair window.

### 5.3 Release

Deleting an acknowledged or purged Outbox record marks its own slot free in the same transaction. Cascading row deletes execute the same release trigger. Test and maintenance truncation has an explicit statement trigger that frees every slot.

The Sender still deletes only records allowed by the existing ACK, terminal, attachment, and purge rules. Capacity allocation does not weaken delivery or cleanup semantics.

### 5.4 Exact Limit Semantics

At most 100,000 committed or transactionally reserved record slots can exist. The 100,001st simultaneous reservation fails explicitly. The system never samples, deletes old records, or falls back to best-effort recording.

Slot allocation makes all 100,000 positions usable. It does not conservatively reject because one Trace or quota bucket is full.

## 6. Configuration And Migration

The migration runs before Application traffic:

1. preserve the configured record and Replay-byte limits;
2. create the slot table and seed the configured range;
3. add a nullable `capacity_slot_id` to existing records;
4. fail migration if existing backlog exceeds the configured maximum;
5. assign one distinct slot to every existing record;
6. add the unique foreign key and `NOT NULL` constraint;
7. replace the record counter triggers with slot reservation/release triggers;
8. expose the derived capacity status and validate record/slot agreement.

An owner-only limit-change function is the supported configuration path. Increasing the limit creates additional slots before publishing the new maximum. Decreasing it is accepted only when all occupied slots fit inside the requested range; otherwise the operator drains backlog and retries. Application and Worker roles cannot mutate limits or slots directly.

The 100,000 default remains a deployment backlog guard, not an HTTP batch size. Sender batch defaults remain 128 records, 512 KiB, or 250 ms.

## 7. Failure And Operational Semantics

- Collector or network failure retains Outbox records and their slots.
- Capacity exhaustion rejects the entire owning product transaction; product state cannot commit without its required observation.
- ACK uncertainty retains the slot until idempotent resend and terminal cleanup complete.
- A process crash cannot orphan a committed slot because record and occupancy changes share one database transaction.
- Operational status reports committed usage. Structured warnings begin before the hard limit; Prometheus and Grafana remain deferred.
- Replay ciphertext still has its separate exact 1 GiB ledger. Record transactions no longer touch that ledger, so Replay accounting cannot serialize ordinary admission, Checkpoint, or publication.

## 8. Verification

### 8.1 Correctness

- With a small configured limit, exactly that many concurrent reservations succeed and the next fails with `54000`.
- Rollback frees an in-flight reservation.
- ACK cleanup, purge cascade, and truncation release the correct slots.
- Reusing a released slot does not reuse Trace identity or sequence identity.
- Existing backlog migrates one-to-one without record or hash drift.
- Capacity status equals the committed Outbox record count after insertion, retry, ACK, purge, and restart.

### 8.2 Concurrency And Performance

- Ten concurrent near-limit Agent Jobs retain all 2,540 unique records, 40 Replay Attachments, lost-ACK recovery, idempotent Collector ingestion, and complete Outbox drain.
- Matched Sprint 4 and Sprint 5 admission, Checkpoint, and publication p95 values satisfy the PRD's maximum 10% regression.
- PostgreSQL lock inspection shows no transaction-scale shared record-counter lock.
- Race, package, migration, and production-shaped split-database gates remain green.

## 9. Evolution

If measured Application PostgreSQL pressure later exceeds this design, the next step is partitioning or CDC-backed delivery, followed by an external broker when justified. The transactional enqueue boundary remains local unless a replacement provides equivalent atomicity. Collector storage and Dashboard queries already sit behind independent APIs, so their scaling path does not require changing product transactions.

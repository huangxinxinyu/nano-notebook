# Direct Agent Trace Delivery Implementation Plan

**Goal:** Remove full Trace and Replay staging writes from Application PostgreSQL,
deliver bounded in-memory batches directly to Collector, and run Collector storage on
an independent PostgreSQL service without changing Dashboard Query contracts.

**Branch:** `main` (explicit user instruction)

**Method:** strict RED/GREEN/REFACTOR, one coherent behavior per commit. Existing
untracked capacity tests are not staged until rewritten for the new contract.

## Slice 1: Collector-Assigned Sequence Contract

**Files:**

- `internal/collector/protocol.go`
- `internal/collector/protocol_json.go`
- `internal/collector/protocol_json_test.go`
- `internal/collector/ingestor_test.go`
- `internal/collector/memory_store.go`
- `internal/collector/postgres_store.go`
- `internal/collector/postgres_store_integration_test.go`

**RED:** Add a direct Trace group containing records without producer sequence. Prove
Collector assigns `1..N`, identical identity/hash resend is a no-op, a different hash
conflicts, and two producers appending distinct identities receive one monotonic stream.

**GREEN:** Add protocol v2/direct sequence authority. Reconcile identity under the
existing per-Trace memory mutex/PostgreSQL advisory lock, assign sequence only to new
records, and return the Collector high watermark. Keep protocol v1 for sealed Sprint 4
migration until cutover completes.

**Verify:** focused Collector unit/integration tests and JSON strictness.

## Slice 2: Incomplete And Late Dependency Projection

**Files:**

- `internal/collector/memory_store.go`
- `internal/collector/projection.go`
- `internal/collector/projection_test.go`
- `internal/collector/postgres_store_integration_test.go`

**RED:** Accept a Trace prefix with missing terminal, retain an explicit incomplete
state, and converge when a later parent/terminal identity arrives. Prove tombstone still
wins and conflicts never mutate raw authority.

**GREEN:** Separate immutable record validity from projection completeness. Preserve
strict envelope/hash validation while representing unresolved structural dependencies
as diagnostics instead of fabricating nodes.

**Verify:** projection lifecycle matrix, tombstone tests, PostgreSQL integration.

## Slice 3: Bounded Memory Batch Exporter

**Files:**

- `internal/agentbatch/exporter.go`
- `internal/agentbatch/exporter_test.go`
- `internal/agentbatch/http.go`
- `internal/agentbatch/http_test.go`

**RED:** Test record/byte bounds, FIFO drop-new overflow, count/byte/delay flush,
multi-Trace grouping, gzip/auth, retryable HTTP, uncertain ACK idempotency, fatal auth,
ForceFlush barrier, shutdown deadline, and concurrent enqueue under `-race`.

**GREEN:** Implement one bounded queue/worker goroutine with immutable envelopes and a
small HTTP client. Pending counters include the in-flight batch so configured bounds are
never exceeded. Diagnostics are rate-limited and contain counts/identities only.

**Verify:** package tests plus targeted race test.

## Slice 4: Deterministic Trace Context And Transaction Buffer

**Files:**

- `internal/agent/trace_delivery.go`
- `internal/agent/trace_delivery_test.go`
- `internal/agent/admission_trace.go`
- `internal/agent/transaction_trace.go`
- `internal/agent/run_trace_admission.go`
- `internal/agent/run_trace_lifecycle.go`
- `internal/agent/checkpoint_trace.go`

**RED:** Prove stable Span ID for a semantic identity across process instances; product
rollback emits nothing; successful commit publishes exactly the buffered identities;
publish failure cannot change committed product result.

**GREEN:** Introduce `TraceSink`, immutable Trace envelope, context-scoped transaction
buffer, deterministic Span identity, and post-commit publish helper. Keep one lightweight
`agent_trace_refs` row created atomically at Run admission.

**Verify:** Agent unit tests and focused Application PostgreSQL transaction tests.

## Slice 5: Control Plane And Worker Cutover

**Files:**

- `internal/app/server.go`
- `internal/jobs/queue.go`
- `internal/agent/postgres_runtime.go`
- `internal/agent/postgres_checkpoint_store.go`
- `internal/agent/attempt_trace_recorder.go`
- `cmd/control-plane/main.go`
- `cmd/worker/main.go`
- associated command and integration tests

**RED:** Start Collector-unavailable Control Plane/Worker and prove admission,
Checkpoint, publication, cancellation, and recovery still succeed while delivery emits
safe diagnostics. Assert no full Trace row is inserted.

**GREEN:** Construct one memory exporter per process, attach transaction buffers at
outer transaction boundaries, and send non-transaction attempt/model/action observations
directly. Graceful process shutdown flushes within 10 seconds.

**Verify:** lifecycle, checkpoint, publication, cancellation, restart, and command config
tests; targeted race.

## Slice 6: Replay Without Application Metadata

**Files:**

- `internal/agent/replay_stager.go`
- `internal/replay/*`
- `internal/agentbatch/*`
- `internal/collector/*replay*`
- Replay integration/security tests

**RED:** Stage normalized encrypted Replay to object storage, enqueue its descriptor
without an Application metadata row, bind it by record identity in Collector, and sweep
an object left by producer loss.

**GREEN:** Make the stager return an immutable attachment descriptor held by the Trace
buffer/exporter. Collector resolves identity to its assigned sequence before committing
the payload reference. Keep current encryption, expiry, audit, and permanent custody.

**Verify:** four Replay classes, tamper tests, expiry, audit, orphan and purge gates.

## Slice 7: Retire Full Trace Runtime Schema

**Files:**

- `internal/app/db.go`
- Application migration and role tests
- `internal/agentoutbox/*`
- migration verifier and CLI

**RED:** Migrate a populated pre-cutover database, drain/verify Collector authority,
then assert normal roles/code cannot write full Trace/staging tables. Purge command
delivery remains durable and idempotent.

**GREEN:** Reduce `agent_trace_refs` to anchor fields, retain a purge-only table/sender,
remove record capacity/cursor/lease/retry/quarantine/staging runtime schema, and isolate
legacy readers behind the maintenance migration command.

**Verify:** migration forward/idempotency, role privileges, purge, no-runtime-reference
search, and Application package tests.

## Slice 8: Independent Local Observability PostgreSQL

**Files:**

- `infra/compose/compose.yaml`
- `scripts/bootstrap`
- `scripts/start`, `scripts/stop`, `scripts/reset`, `scripts/health`
- `scripts/migrate`, `scripts/test-go`, service/capacity scripts
- readiness and lifecycle tests

**RED:** Compose/config test requires two PostgreSQL services, different ports, volumes,
credentials, and health checks. Stopping Observability PostgreSQL must leave Application
health and a product transaction green.

**GREEN:** Add `observability-postgres` with its own volume and move every Collector/test
DSN and initialization path. Remove role/database creation from Application PostgreSQL.

**Verify:** Compose config, migration, native lifecycle, Collector service smoke, and
independent-failure integration.

## Slice 9: Operations, Capacity, And Final Acceptance

**Files:**

- `docs/implementation/sprint-5-agent-trace-operations-runbook.md`
- `internal/app/sprint5_capacity_integration_test.go`
- `scripts/test-sprint5-capacity`
- `.planning/2026-07-18-sprint-5-agent-trace-operations/acceptance-map.md`

Rewrite old Outbox backlog/capacity operations as memory queue/incomplete diagnostics,
dedicated Observability PostgreSQL recovery, and purge-command operations. Replace the
old zero-loss 2,540-record assertion with bounded resident delivery plus explicit
process-loss behavior. Preserve the existing 100,000-summary Query performance gate.

Final evidence:

1. focused RED/GREEN commands recorded per slice;
2. `scripts/test-go` and targeted `go test -race`;
3. Web unit/typecheck/lint/build and Playwright;
4. Collector-unavailable product journey;
5. separate PostgreSQL process/volume evidence;
6. Replay governance and deletion races;
7. 10-Job producer bound and 100,000-summary Query capacity;
8. every revised PRD success criterion mapped to evidence.

The goal is complete only when every blocking criterion passes and the worktree contains
no unintended files. `.superpowers/` remains excluded.

# Backend Engineering

Reusable backend engineering standards for coding agents. Use this with
`AGENTS.md` when a goal touches APIs, storage, background work, consistency,
security, observability, or production behavior.

## Core Principles

- Durable product state must have a clear source of truth.
- Cache, queues, locks, and coordination systems are operational layers, not
  substitutes for durable invariants.
- State transitions must be explicit, validated, and observable.
- Cross-process calls need timeouts, cancellation, retry policy, and structured
  error handling.
- Background jobs, event consumers, webhooks, and retryable mutation paths should
  be idempotent.
- Assume duplicate work, delayed messages, partial failure, stale reads, and
  out-of-order observations.
- Prefer boring, inspectable designs over clever distributed protocols.

## Domain Boundaries

- Keep product concepts distinct even when implementation would be shorter if
  they were merged.
- Keep vendor and provider terms behind integration boundaries.
- Core services should speak product language, not database table names or
  third-party API shapes.
- Add abstraction only when there is a clear boundary or a known second
  implementation.

## API Contracts

- Design APIs around stable resources and standard operations.
- Use explicit custom actions only when CRUD semantics do not fit.
- Keep request and response shapes stable once used by clients.
- Use consistent field names and stable error shapes with machine-readable codes.
- Include request IDs or trace IDs in responses and logs.
- Use cursor pagination for lists that can grow.
- Make filters and sort fields explicit allowlists.

Mutation APIs should be retry-safe:

- Prefer caller-supplied IDs or idempotency keys for create operations.
- Repeated requests with the same idempotency key and same parameters should
  return the same semantic result.
- Repeated requests with the same idempotency key and different parameters
  should fail clearly.
- Store enough response metadata to answer retries consistently.

Long-running APIs should return a durable resource the client can poll or
subscribe to.

## Idempotency

Use one or more durable mechanisms:

- unique constraints,
- idempotency-key records,
- processed-event records,
- compare-and-swap state transitions,
- leases with fencing tokens,
- upserts when duplicate creation is expected.

Side effects and idempotency records should commit atomically when they share a
correctness boundary. If they cannot, use an outbox or make the downstream side
effect independently idempotent.

Never rely on in-memory idempotency for durable behavior.

## State Machines And Workers

State machines are correctness boundaries:

- define allowed transitions in code,
- reject invalid transitions,
- record who or what caused the transition,
- persist timestamps for important lifecycle points,
- keep coarse product state stable and put detailed progress in events.

Workers should:

- claim work with a lease or equivalent ownership record,
- renew ownership explicitly for long-running work,
- treat expired ownership as ambiguous,
- reload authoritative state before recovery actions,
- make cleanup safe to repeat.

## Concurrency And Backpressure

- Every concurrent task needs an owner and a shutdown path.
- Pass cancellation context through request, worker, database, cache, and
  external calls.
- Bound worker pools, queues, buffers, fanout, page sizes, request bodies, and
  external calls.
- Avoid fire-and-forget work unless it is durably recorded and recoverable.
- Do not hold locks while making network calls.
- Prefer database constraints and transactional updates over distributed locks
  for durable correctness.

## Timeouts, Retries, And Fallbacks

- All cross-process calls need timeouts.
- Retry only idempotent or read-only operations.
- Retry at one layer in the call stack.
- Use capped exponential backoff with jitter.
- Bound retry duration by the caller's deadline.
- Do not retry validation errors, authorization errors, or deterministic
  conflicts.
- Log final failure with attempt count and error class.

Fallback paths must meet the same correctness bar as primary paths. They must
not hide data loss, stale state, or permission failures.

## Database And Transactions

Schema design starts from access patterns:

- document expected read and write paths before adding tables,
- use stable opaque IDs for externally visible identifiers,
- use foreign keys, unique constraints, and check constraints for invariants,
- add indexes that match real query paths,
- avoid redundant indexes and unbounded scans on growing tables.

Migrations should be production-minded:

- split risky changes into expand, migrate, and contract phases,
- backfill large tables in batches,
- avoid long transactions and table-wide locks,
- keep destructive changes separate and documented.

Transactions should be small and purposeful:

- put writes for one invariant in the same transaction,
- keep slow network calls outside transactions,
- use row locks only for named invariants,
- include expected current state in state transition updates.

## Events And Consistency

- Use append-only events for traceability when state changes need an audit trail.
- Events should have stable types, structured payloads, and enough metadata to
  debug without live external systems.
- Use an outbox when a database write must cause an external message or side
  effect.
- Consumers must tolerate duplicate, delayed, and out-of-order delivery.
- Do not claim exactly-once behavior across process boundaries.

## Cache Consistency

- Cache is an optimization, not the source of truth.
- Cached values must be reconstructable from durable state or an external source
  of truth.
- Define key shape, TTL, invalidation trigger, and allowed staleness.
- Prevent older cache fills from overwriting newer invalidations.
- Provide a bypass or rebuild path for operational recovery.
- Observe hit rate, miss rate, stale reads, invalidation lag, rebuild failures,
  and cache latency.

## Observability

Every important operation needs correlation:

- request ID,
- actor or tenant ID,
- resource ID,
- job or operation ID,
- external provider request ID when available.

Use structured logs for state transitions, worker claims, retry exhaustion,
idempotency hits and mismatches, webhook handling, cache invalidation, and
external provider operations.

Metrics should cover request rate, latency, errors, queue depth, worker lag,
state duration, retry counts, lease expiration, database latency, cache latency,
and idempotency mismatches.

## Security And Data Safety

- Treat user content, prompts, traces, patches, logs, and artifacts as user data.
- Never log secrets, access tokens, environment variables, or authorization
  headers.
- Redact credentials before persistence.
- Enforce authorization before resource access.
- Prefer deny-by-default permission checks.
- Make destructive actions explicit and audited.
- Store enough audit context to answer who did what and which credentials or
  provider were used.

## Backend Change Checklist

Before committing a backend change, answer the relevant questions:

- What durable invariant does this change introduce or rely on?
- Is the operation safe to retry?
- What prevents duplicate side effects?
- What happens if the process crashes between the database write and the
  external side effect?
- What happens if the external side effect succeeds but the process times out?
- Which constraints enforce correctness?
- Which query paths need indexes or query-plan review?
- Does this migration lock, rewrite, or scan a table that can grow large?
- What is the maximum concurrency and how is it bounded?
- What is the allowed consistency window?
- If cache is wrong, how is it detected and rebuilt?
- Are events safe under duplicate, delayed, or out-of-order delivery?
- What logs, metrics, or traces prove the system is healthy?

# Sprint 5 Agent Trace Operations Runbook

Sprint 5 sends diagnostic Agent Trace records directly from bounded process memory to
Collector. It does not place full Trace or Replay staging traffic on Application
PostgreSQL. The Dashboard reads Collector projections through the Control Plane Admin
API. Prometheus and Grafana remain future platform-metrics work and are not Trace
authority.

## Responsibility And Data Boundaries

```text
Control Plane / Worker
  -> Application PostgreSQL (product authority)
       Run, Job, Lease, Checkpoint, lightweight Trace anchor,
       durable purge intent, Replay access audit
  -> bounded process memory (diagnostic records)
       10,000 records / 32 MiB
       -> gzip HTTP Batch: 128 records / 512 KiB / 250 ms
  -> producer Replay staging bucket (ciphertext + manifest)
       -> Collector
            -> independent Observability PostgreSQL
                 immutable records, tombstones, projections, Replay references
            -> Collector Replay bucket

Browser -> Control Plane Admin API -> Collector Query API
```

- Product commit never depends on Collector, Observability PostgreSQL, or the memory
  exporter. Transaction Trace buffers publish only after product commit and disappear
  on rollback.
- Hard process loss may lose the bounded unsent diagnostic tail. Collector represents
  missing roots, parents, or terminals as incomplete; it never invents data.
- Network loss, `429`, `5xx`, timeout, and uncertain ACK retry the same Batch ID while
  resident. Authentication, invalid protocol, and permanent Collector rejection drop
  the affected diagnostic Batch and increment drop diagnostics.
- Deletion is different from diagnostics: purge intent stays durable in Application
  PostgreSQL and a purge-only sender retries it until Collector ACKs.
- Collector owns sequence assignment. Producer processes coordinate by stable record
  identity and canonical hash, not through Application PostgreSQL.
- Replay ciphertext and its producer manifest are object-store data. No Prompt,
  Response, Tool payload, ciphertext, or staging descriptor is written to Application
  PostgreSQL by the Sprint 5 runtime.

The old `agentobs_outbox_records` and `agentobs_replay_staging` schema remains only for
Sprint 4 migration/rollback compatibility in this release. Control Plane and Worker
production wiring does not construct the legacy full-Trace sender or PostgreSQL Replay
stager. Remove the compatibility schema only after historical migration verification
and the rollback window close.

## Local Physical Isolation

Local development uses two PostgreSQL processes, not two databases in one process:

| Service | Port | Volume | Owner |
|---|---:|---|---|
| `postgres` | 55432 | `postgres-data` | Application |
| `observability-postgres` | 55433 | `observability-postgres-data` | Collector |

They have independent CPU/memory/I/O/failure domains at the container level. Stopping
`observability-postgres` must leave Application health and product transactions green.
Production must use independently capacity-managed PostgreSQL services as well.

Start and verify:

```sh
scripts/start
scripts/health
```

Default DSNs:

```text
NANO_DATABASE_URL=postgres://nano:nano@localhost:55432/nano?sslmode=disable
NANO_COLLECTOR_DATABASE_URL=postgres://nano_observability:nano-observability@localhost:55433/nano_observability?sslmode=disable
```

Collector uses separate ingestion, projection, and query pools. Default maxima are 16,
4, and 8 connections. Budget replicas and maintenance headroom against the
Observability service only; do not increase the Application pool to address Collector
lag.

## Required Configuration

- `NANO_COLLECTOR_URL`: internal TLS Collector base URL.
- `NANO_COLLECTOR_SERVICE_TOKEN`: ingestion and purge credential used by producers.
- `NANO_COLLECTOR_QUERY_TOKEN`: distinct Control Plane query credential.
- `NANO_COLLECTOR_PRODUCER_ID`: Worker producer identity.
- `NANO_CONTROL_PLANE_PRODUCER_ID`: Control Plane producer identity.
- `NANO_COLLECTOR_PRODUCER_ID_PREFIX`: Collector allow-list prefix for process producers.
- `NANO_OUTBOX_MAX_RECORDS`: compatibility name for direct Batch record limit; default
  128.
- `NANO_OUTBOX_MAX_ENCODED_BYTES`: compatibility name for direct Batch byte limit;
  default 512 KiB.
- `NANO_OUTBOX_MAX_DELAY`: direct Batch maximum delay; default 250 ms.
- `NANO_OUTBOX_HTTP_TIMEOUT`: Collector request timeout; default 10 seconds.

The per-process pending bound is fixed at 10,000 records and 32 MiB in Sprint 5. The
remaining `NANO_OUTBOX_*` lease/backoff settings configure only the low-volume durable
purge sender and will be renamed when the migration compatibility layer is removed.

Worker staging storage:

- `NANO_REPLAY_STAGING_S3_ENDPOINT`
- `NANO_REPLAY_STAGING_S3_ACCESS_KEY_ID`
- `NANO_REPLAY_STAGING_S3_SECRET_ACCESS_KEY`
- `NANO_REPLAY_STAGING_S3_BUCKET`
- `NANO_REPLAY_STAGING_S3_REGION`
- `NANO_REPLAY_STAGING_S3_USE_TLS`

Collector needs read access to staging and write/delete access to its independent Replay
bucket through the corresponding `NANO_REPLAY_S3_*` variables. Browser and Control
Plane receive no object-store credentials.

## KMS Is Deferred

The repository development `KeyProvider` and static 32-byte KEK are local/test only.
Production Replay remains blocked until a reviewed cloud or Vault KMS provider supplies
least-privilege wrap/unwrap identities, rotation, revocation, recovery, and audit. There
is no approved static-KEK production fallback. Metadata-only production mode is a
separate future decision.

## Health And Failure Semantics

- Control Plane readiness checks Application PostgreSQL only. Collector failure must
  not make admission, cancellation, or product reads unhealthy.
- Worker readiness checks Application PostgreSQL. Collector outage increases the
  bounded in-memory queue and may eventually drop new diagnostics; Jobs and Checkpoints
  continue.
- Collector readiness checks Observability PostgreSQL and both object stores.
- Dashboard list/detail/replay may be unavailable while Collector is unavailable; this
  has no product-authority meaning.

Expected incident behavior:

| Failure | Product effect | Trace effect | Operator action |
|---|---|---|---|
| Collector/network unavailable | none | memory retry, then bounded overflow | recover Collector; inspect queue/drop logs |
| Worker hard crash | normal Job recovery | unsent tail may be incomplete | restart Worker; do not fabricate records |
| Observability PostgreSQL down | none | Collector cannot ACK/query | recover only Observability service |
| Application PostgreSQL down | product unavailable | producers cannot recover work | recover Application service |
| Invalid/auth Batch | none | permanent diagnostic drop | fix token/protocol before restart |
| Purge delivery failure | deletion access tombstoned after Collector receives it; command remains durable before then | purge retries | recover route/token; never delete command manually |

Memory exporter diagnostics must contain counts, Batch/record identities, and error
class only. Never log Prompt, Response, Tool input/result, wrapped keys, nonces, tokens,
or service credentials.

## Collector Operations

Projection lag:

```sql
select count(*) filter (where projected_sequence < committed_sequence) as lagged_traces,
       coalesce(sum(committed_sequence - projected_sequence), 0) as unprojected_records
from obs_traces;
```

Incomplete Traces:

```sql
select trace_id, run_id, active, projected_sequence
from obs_trace_summaries
where active = true
order by started_at_unix_nano desc
limit 100;
```

Durable purge commands in Application PostgreSQL:

```sql
select delivery_state, count(*), max(attempt_count), min(next_attempt_at)
from agentobs_outbox_commands
group by delivery_state
order by delivery_state;
```

Rebuild projection data without changing raw authority:

```sh
NANO_COLLECTOR_DATABASE_URL="$OBSERVABILITY_DATABASE_URL" \
go run ./cmd/collector-rebuild '<trace-id>'

NANO_COLLECTOR_DATABASE_URL="$OBSERVABILITY_DATABASE_URL" \
go run ./cmd/collector-rebuild all
```

## Migration And Rollback

1. Back up Application PostgreSQL, Observability PostgreSQL, and both Replay buckets.
2. Freeze new admission and drain active Sprint 4 Jobs.
3. Migrate historical Sprint 4 records to Collector with the existing maintenance
   path.
4. Run `cmd/trace-migration-verify` against independent repeatable-read snapshots.
5. Deploy Collector, then direct-delivery Worker and Control Plane.
6. Verify one product journey and Dashboard Tree, Timeline, Inspector, Replay, and
   token/cost/latency analysis.
7. Re-enable admission.

Do not point the new runtime back at the legacy Outbox as an outage workaround. Rollback
means deploying the previous complete release while its compatibility schema and backup
remain available.

## Verification Gates

```sh
scripts/test-go
scripts/test-web
scripts/test-sprint5-capacity
```

The capacity gate proves:

- 10 concurrent producers / 2,540 records stay below 10,000 records and 32 MiB;
- commit-before-ACK retries idempotently with zero diagnostic drops while resident;
- 100,000 Trace summaries list below 500 ms p95 at the Control Plane boundary;
- a 256-Span detail loads below one second p95.

The dedicated failure-domain check stops `observability-postgres`, verifies Application
PostgreSQL remains ready, and runs direct admission successfully before restarting the
Observability service.

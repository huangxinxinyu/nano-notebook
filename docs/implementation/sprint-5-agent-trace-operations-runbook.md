# Sprint 5 Agent Trace Operations Runbook

Sprint 5 moves Durable Agent Trace records through an Application-owned Outbox to a standalone Collector and exposes the Collector projection through an operator-only Dashboard. This runbook covers deployment, migration, health, backlog diagnosis, projection rebuild, Replay custody, and recovery.

The Trace path describes execution; it never authorizes Agent admission, Checkpoint, Lease, cancellation, or publication. Prometheus and Grafana remain out of scope for this Sprint.

## Responsibility And Data Boundaries

```text
Control Plane + Worker
  -> Application PostgreSQL
       product authority, Trace references, Durable Outbox, Replay staging metadata
  -> producer Replay staging bucket
  -> authenticated Collector HTTP
       -> Observability PostgreSQL
            immutable Trace authority, query projections, Replay metadata
       -> Collector Replay bucket

Browser -> Control Plane Admin API -> Collector Query API
```

- The Worker is the only Batch producer. It never opens the Observability database.
- The Collector is the only owner of immutable Trace records and projections after ACK. It never opens the Application database.
- The Control Plane queries the Collector over HTTP. The Dashboard never queries either database or object storage directly.
- Producer staging and Collector Replay custody use different buckets. PostgreSQL stores metadata and encrypted-key material, never Prompt or Response ciphertext bytes.
- Future Prometheus/Grafana data must remain operational telemetry; it must not become Durable Agent Trace authority.

Production uses a separate Application PostgreSQL instance and Observability PostgreSQL instance. Local development may use one PostgreSQL container, but it still uses separate databases, roles, migrations, and pools.

## Production Replay Blocker: KMS Is Deferred

Sprint 5 includes the `KeyProvider` boundary and a repository-owned development provider. The current Worker and Control Plane must receive the same `NANO_REPLAY_KEY_ID` and 32-byte development KEK through `NANO_REPLAY_KEK_BASE64`; this is for local and test deployments only.

Do not deploy the current Replay key configuration to production. Production Replay remains blocked until a reviewed cloud or Vault KMS provider supplies:

- producer envelope-key wrapping and Control Plane unwrapping behind `KeyProvider`;
- key policy and least-privilege identities;
- rotation, revocation, recovery, and audit procedures;
- a migration plan for ciphertext wrapped under an old key version.

There is no approved fallback from KMS to a static production KEK. If production must launch before the KMS work, the current binaries need an explicit metadata-only mode before deployment; do not treat the development provider as that mode.

## Required Configuration

Keep secrets in the deployment secret store. Do not commit them, place them in browser configuration, print them in diagnostics, or reuse the repository's local defaults outside local development.

### Shared Service Identity

- `NANO_COLLECTOR_URL`: Collector base URL used by Worker and Control Plane. Production requires authenticated TLS routing.
- `NANO_COLLECTOR_ADDR`, `NANO_WORKER_ADDR`, and `NANO_CONTROL_PLANE_ADDR`: internal listen addresses owned by the deployment runtime.
- `NANO_COLLECTOR_PRODUCER_ID`: stable producer identity; Worker and Collector values must match exactly.
- `NANO_COLLECTOR_SERVICE_TOKEN`: Batch and purge credential shared only by Worker and Collector.
- `NANO_COLLECTOR_QUERY_TOKEN`: query credential shared only by Control Plane and Collector. It must differ from the service token.

Changing the producer ID after data exists causes valid retries to be rejected. Rotate either token by deploying both holders together; a mismatched service token stops delivery but leaves Outbox records durable.

### Databases

- Control Plane and Worker: `NANO_DATABASE_URL` for the Application database.
- Collector and maintenance commands: `NANO_COLLECTOR_DATABASE_URL` for the Observability database.
- Collector ingestion pool: `NANO_COLLECTOR_DATABASE_MAX_CONNS` and `NANO_COLLECTOR_DATABASE_MIN_CONNS` (defaults `16` and `2`).
- Collector projection pool: `NANO_COLLECTOR_PROJECTION_DATABASE_MAX_CONNS` and `NANO_COLLECTOR_PROJECTION_DATABASE_MIN_CONNS` (defaults `4` and `1`).
- Collector query pool: `NANO_COLLECTOR_QUERY_DATABASE_MAX_CONNS` and `NANO_COLLECTOR_QUERY_DATABASE_MIN_CONNS` (defaults `8` and `1`).

One Collector replica therefore reserves up to 28 Observability connections with defaults. Budget `replicas × (ingestion + projection + query)` plus migrations, verification, backup, and operator headroom below PostgreSQL `max_connections`. Do not raise application pool concurrency to compensate for Collector lag; the databases are independent failure and capacity domains.

### Batch Sender

- `NANO_OUTBOX_MAX_RECORDS` (default `128`)
- `NANO_OUTBOX_MAX_ENCODED_BYTES` (default `524288`)
- `NANO_OUTBOX_MAX_TRACES` (default `16`)
- `NANO_OUTBOX_LEASE_DURATION` (default `30s`)
- `NANO_OUTBOX_POLL_INTERVAL` (default `100ms`)
- `NANO_OUTBOX_MAX_DELAY` (default `250ms`)
- `NANO_OUTBOX_HTTP_TIMEOUT` (default `10s`)
- `NANO_OUTBOX_BASE_BACKOFF` (default `1s`)
- `NANO_OUTBOX_MAX_BACKOFF` (default `1m`)
- `NANO_COLLECTOR_MAX_BODY_BYTES` on Collector (default `2097152`)

The sender enforces record, encoded-byte, and Trace-count thresholds independently. Keep the Collector body limit above the largest permitted encoded Batch. Outbox recording remains required and bounded; exhaustion fails explicitly and never discards an older record.

### S3-Compatible Replay Storage

The Worker uses:

- `NANO_REPLAY_STAGING_S3_ENDPOINT`
- `NANO_REPLAY_STAGING_S3_ACCESS_KEY_ID`
- `NANO_REPLAY_STAGING_S3_SECRET_ACCESS_KEY`
- `NANO_REPLAY_STAGING_S3_BUCKET`
- `NANO_REPLAY_STAGING_S3_REGION`
- `NANO_REPLAY_STAGING_S3_USE_TLS`

The Collector uses the same staging endpoint, bucket, region, and TLS setting to take custody, but may receive a separate read-scoped staging credential through the same environment-variable names in its own process. It also requires `NANO_REPLAY_S3_ENDPOINT`, `NANO_REPLAY_S3_ACCESS_KEY_ID`, `NANO_REPLAY_S3_SECRET_ACCESS_KEY`, `NANO_REPLAY_S3_BUCKET`, `NANO_REPLAY_S3_REGION`, and `NANO_REPLAY_S3_USE_TLS` for its final Replay bucket. S3 endpoints must omit the URL scheme; `USE_TLS` selects transport security. Both buckets must exist before the processes start because readiness checks do not create them.

Use independent credentials and least privilege in production-shaped environments:

- Worker: bucket readiness, put/list/delete in producer staging only;
- Collector: bucket readiness/read in producer staging and readiness/list/read/write/delete in Collector Replay custody;
- Control Plane and browser: no object-store credentials.

## Local Start And Verification

Start the complete local stack:

```sh
scripts/start
```

In another shell, verify all local services:

```sh
scripts/health
```

The local Collector is also available as a Compose service under the `collector` profile. The normal `scripts/start` path runs native Control Plane, Worker, and Collector processes against the local containers.

Grant an existing trusted User Trace access:

```sh
go run ./cmd/platform-grant grant operator@example.com platform.trace.read
go run ./cmd/platform-grant grant operator@example.com platform.trace.replay
```

`platform.trace.read` does not imply Replay access. Notebook Owner, Editor, and Viewer roles grant neither capability. Open `/admin/traces` only after signing in as the granted User.

Run the repository gate before a deployment candidate is promoted:

```sh
scripts/test-go
scripts/test-web
```

## Maintenance Cutover From Sprint 4

The cross-database verifier takes independent repeatable-read snapshots. Admission and delivery must remain frozen while it runs; otherwise two correct databases can describe different moments and produce misleading evidence.

1. Provision the separate Observability database role/database, both Replay buckets, scoped credentials, TLS route, and connection budgets.
2. Deploy and health-check the Collector without enabling new Agent admission.
3. Pause Agent admission and let active Jobs reach a terminal or explicitly controlled state. Stop the old Worker after the drain.
4. Back up the Application database, Observability database, and Replay buckets. Record the exact recovery points.
5. Apply both schemas with the intended production DSNs:

   ```sh
   NANO_DATABASE_URL="$APPLICATION_DATABASE_URL" \
   NANO_COLLECTOR_DATABASE_URL="$OBSERVABILITY_DATABASE_URL" \
   go run ./cmd/migrate
   ```

   Application migration takes an advisory lock, converts every Sprint 4 `agent_traces`/`agent_trace_records` sequence into the Outbox, recomputes canonical hashes, and rolls back that Application transaction on count, sequence, payload-hash, or canonical-hash drift. Re-running it is idempotent.

6. Start the new Worker with admission still paused so the Batch Sender drains the migrated Outbox. Wait until no legacy Trace has a pending cursor and no migrated Trace is quarantined.
7. Stop the Worker again, keeping admission frozen, then run the read-only cross-database verifier:

   ```sh
   NANO_DATABASE_URL="$APPLICATION_DATABASE_URL" \
   NANO_COLLECTOR_DATABASE_URL="$OBSERVABILITY_DATABASE_URL" \
   go run ./cmd/trace-migration-verify
   ```

   The command compares every Sprint 4 envelope, record count, sequence, identity key, and recomputed canonical hash with Collector raw authority. It ignores additional native Sprint 5 Traces. Any error blocks cutover.

8. Start Collector, Worker, Control Plane, and Web from the same release. Run one accepted Agent journey and verify Trace list, Tree, Timeline, Inspector, and per-Trace token/cost/latency analysis. Replay verification remains local/test-only until KMS is implemented.
9. Re-enable Agent admission.

The Dashboard has no legacy fallback. Keep `agent_traces` and `agent_trace_records` read-only through the rollback window. Remove them only in a separately reviewed migration after the verifier is green, backups are recoverable, and rollback no longer requires the old authority.

## Health Semantics

Collector readiness:

```sh
curl -fsS "$NANO_COLLECTOR_URL/internal/agent-observability/v1/health"
```

HTTP 200 means all three Collector PostgreSQL pools and both S3 buckets responded within the readiness window. It does not mean Outbox, projection, expiry, or purge queues are empty.

Worker `/health/live` proves the process serves HTTP. Worker `/health/ready` currently proves only that the Application database responds. The Worker checks its staging bucket during startup, but its ready endpoint does not continuously prove Collector or S3 reachability; use backlog and error evidence below.

Control Plane `/health/ready` proves its Application database is reachable. A healthy Control Plane does not imply Collector query health.

## Backlog And Failure Diagnosis

Run database queries with a dedicated read-only operator role. Application and Worker runtime roles intentionally cannot inspect internal capacity tables directly.

### Application Outbox

Summarize logical delivery state:

```sql
select delivery_state,
       count(*) as traces,
       coalesce(sum((next_sequence - 1) - collector_cursor), 0) as pending_records,
       min(created_at) filter (where collector_cursor < next_sequence - 1) as oldest_pending,
       max(attempt_count) as max_attempts
from agent_trace_refs
group by delivery_state
order by delivery_state;
```

Inspect failures without reading Prompt or Response content:

```sql
select trace_id, run_id, delivery_state, collector_cursor,
       next_sequence - 1 as produced_sequence,
       attempt_count, last_error_code, next_attempt_at, quarantined_at
from agent_trace_refs
where delivery_state in ('ready', 'leased', 'quarantined', 'purging')
   or collector_cursor < next_sequence - 1
order by updated_at
limit 100;
```

Check physical pressure:

```sql
select count(*) as outbox_records,
       coalesce(sum(encoded_bytes), 0) as encoded_bytes
from agentobs_outbox_records;

select state, count(*) as attachments,
       coalesce(sum(ciphertext_bytes), 0) as ciphertext_bytes,
       min(expires_at) as earliest_expiry
from agentobs_replay_staging
group by state
order by state;

select delivery_state, count(*) as purge_commands,
       max(attempt_count) as max_attempts,
       min(next_attempt_at) as next_attempt
from agentobs_outbox_commands
group by delivery_state
order by delivery_state;
```

### Collector Projection And Purge

```sql
select count(*) as traces,
       count(*) filter (where tombstoned_at is not null) as tombstoned,
       count(*) filter (where projected_sequence < committed_sequence) as lagged,
       coalesce(sum(committed_sequence - projected_sequence), 0) as unprojected_records
from obs_traces;

select trace_id, target_sequence, attempt_count, last_error_code,
       available_at, lease_expires_at, updated_at
from obs_projection_queue
order by updated_at
limit 100;

select stage, count(*) as traces, max(attempt_count) as max_attempts,
       min(available_at) as next_attempt
from obs_purge_queue
group by stage
order by stage;

select state, count(*) as attachments,
       coalesce(sum(ciphertext_bytes), 0) as ciphertext_bytes,
       min(expires_at) filter (where state = 'available') as earliest_expiry
from obs_payload_refs
group by state
order by state;
```

Interpret common states as follows:

- `ready` with rising attempts: Collector/network/service-token failure; records remain durable and retry with backoff.
- expired `leased`: another Sender reclaims it. Do not clear leases manually.
- `quarantined`: Collector rejected an invariant such as identity or canonical-content conflict. Preserve evidence and fix the producer/schema issue; do not change the cursor or set the row back to `ready` by hand.
- projection queue error: raw Collector ACK authority remains valid. Fix projection compatibility, then rebuild.
- purge queue error: access is already tombstoned; object/content deletion continues asynchronously after recovery.

## Projection Rebuild

Rebuild one Trace synchronously after fixing projection code or historical data compatibility:

```sh
NANO_COLLECTOR_DATABASE_URL="$OBSERVABILITY_DATABASE_URL" \
go run ./cmd/collector-rebuild '<trace-id>'
```

Enqueue every non-tombstoned Trace for rebuild:

```sh
NANO_COLLECTOR_DATABASE_URL="$OBSERVABILITY_DATABASE_URL" \
go run ./cmd/collector-rebuild all
```

The `all` command only enqueues work. Keep the Collector running and wait for `obs_projection_queue` to drain. Rebuild replaces disposable summaries, Spans, Events, and Links from immutable raw records; it never changes Agent execution or Replay ciphertext.

## Recovery Rules

- Collector/network unavailable before ACK: leave the Outbox intact. Restoring connectivity is sufficient; resend is idempotent.
- ACK lost after Collector commit: do not advance the cursor manually. The Sender resends the same identities and Collector returns the committed cursor without duplicates.
- Worker process loss: its Agent Job lease and Outbox delivery lease recover independently. Restart the Worker; expired leases are reclaimed.
- Collector process loss: restart it against the same Observability database and Replay bucket. Projection, expiry, and purge maintenance resume from durable queues.
- Staging S3 unavailable: Worker startup fails if readiness cannot be established. A failed staging write must fail before the corresponding Model or Action boundary; never record an attachment reference to bytes that were not durably staged.
- Collector Replay S3 unavailable: Collector readiness fails and attachment-bearing chunks remain retryable. Metadata-only chunks may not be used to bypass required Replay custody.
- Projection failure: the Dashboard may report pending/stale projection while immutable records remain safe. Fix the projector and use `collector-rebuild`.
- Replay expiry: ciphertext is deleted and metadata becomes `expired`; Trace Tree/Timeline remain available. Do not recreate expired content from logs.
- Parent deletion: the Collector tombstone revokes query access before asynchronous object/raw-content purge. Never remove the tombstone to recover a deleted Trace.

For database recovery, stop admission and Worker delivery first. Restore the Observability database and Replay bucket to a mutually consistent recovery point. Never restore Collector behind an Application cursor whose acknowledged Outbox records have already been removed; those native Sprint 5 records may no longer exist anywhere else. Restore from a sufficiently recent Collector backup/WAL position or escalate instead of fabricating records. Restoring Application state behind Collector is safer because retained Outbox identities can be resent idempotently, but it still requires the cross-store and product-authority checks before resuming admission.

Do not repair incidents by editing canonical hashes, deleting Outbox rows, advancing cursors, clearing tombstones, or copying plaintext Replay into PostgreSQL. Preserve the failed state, collect bounded identifiers/error codes, and recover through the owning component.

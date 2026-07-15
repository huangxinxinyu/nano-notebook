# Sprint 2B Agent Recovery Runbook

## Runtime Contract

Sprint 2B keeps the fixed, source-less model call from Sprint 2A and adds durable execution ownership:

```text
queued Job
  -> claim attempt 1 + 30s lease
  -> heartbeat every 10s
  -> publish/fail with the current lease token

expired running Job
  -> direct reclaim + new token + attempt 2 or 3
  -> attempt 3 expiry -> failed(recovery_exhausted)
```

The Run stays `running` while a Job is reclaimed. The browser sees no Attempt or lease details. Stop is authoritative when its PostgreSQL transaction commits; the old model request may keep consuming resources until its next heartbeat, but its token can no longer publish.

## Upgrade Order

Do not perform a rolling upgrade from a Sprint 2A Worker to Sprint 2B. The old Worker does not present a Lease Token and cannot participate in fencing.

Use this order:

1. stop Sprint 2A Workers and wait for their model calls to end;
2. run `cmd/migrate` with the Sprint 2B binary;
3. start Sprint 2B Workers;
4. start or update the Control Plane and Web client.

The migration preserves terminal Jobs and turns a legacy unleased `running` Run/Job pair back into `queued` attempt `0`. A Sprint 2B Worker then claims it with lease authority. The migration is idempotent and its integration test exercises a populated Sprint 2A schema, not only an empty database.

## Shutdown

SIGINT or SIGTERM stops new claims, cancels the active model context, conditionally expires the current token's lease, and notifies `nano_agent_jobs`. The process waits for that release path before closing PostgreSQL. If PostgreSQL is unavailable, the attempt remains fenced by its deadline and becomes reclaimable after natural expiry.

## Inspect Durable State

```bash
docker compose -f infra/compose/compose.yaml exec postgres \
  psql -U nano -d nano -c \
  "select id, status, input_message_id, output_message_id, error_code from agent_runs order by created_at desc limit 10;"

docker compose -f infra/compose/compose.yaml exec postgres \
  psql -U nano -d nano -c \
  "select id, run_id, status, attempt_no, lease_token, lease_expires_at from agent_jobs order by created_at desc limit 10;"
```

Expected invariants:

- `queued`: attempt `0`, no token, no deadline;
- `running`: attempt `1..3`, one token, one deadline;
- terminal Job: no token and no deadline;
- `cancelled` Run: no output Message and no error code;
- `failed` Run: no output Message and a safe error code;
- `completed` Run: exactly one output Message.

## Verification

Run:

```bash
scripts/test-go
scripts/test-web
```

The suites cover lease renewal/reclaim/fencing/exhaustion, shutdown release, Stop/publication ordering, publication acknowledgement reconciliation, Retry identity and idempotency, context cutoff, populated Sprint 2A migration, SSE restoration, and frontend Stop/Retry behavior.

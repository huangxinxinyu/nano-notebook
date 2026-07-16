# Sprint 3 Checkpointed AgentLoop Runbook

## Local topology

Sprint 3 runs entirely in the local repository stack:

```text
Browser -> Control Plane -> PostgreSQL Run/Job
                         -> Worker Agent Controller
                         -> local Bifrost -> aliyun/qwen-flash
```

The Controller discovers the built-in `calculate` and `current_time` Actions from its immutable startup Registry. Accepted Proposal batches, individual Action Results, and the Final Draft are append-only PostgreSQL checkpoints. They are internal recovery authority and are not exposed by REST, SSE, or the browser.

## Deterministic verification

The Go test command starts or reuses the local OrbStack Compose PostgreSQL and isolates destructive tests in `nano_test`:

```bash
scripts/test-go
scripts/test-web
```

`NANO_TEST_DATABASE_URL` is only a local connection setting. It defaults to:

```text
postgres://nano:nano@localhost:55432/nano_test?sslmode=disable
```

Do not point it at the development database `nano`, because integration tests rebuild the target schema.

## Opt-in real Qwen smoke

Copy the untracked Compose environment template and set the credential locally:

```bash
cp infra/compose/.env.example infra/compose/.env
```

Set `DASHSCOPE_API_KEY` in `infra/compose/.env`, then run:

```bash
scripts/test-sprint3-qwen
```

The command starts local PostgreSQL and Bifrost, calls the configured `aliyun/qwen-flash`, and requires the real Controller to accept both `current_time` and `calculate` before publishing exactly one Assistant Message. It is intentionally excluded from deterministic CI and spends Provider tokens. The command checks only that a credential is configured; it never prints the key, Bifrost configuration, or raw Provider payloads.

## Expected durable result

A successful multi-step turn leaves:

- one User Message and one immutable Assistant Message;
- one completed Agent Run and one succeeded Agent Job;
- one or more Proposal and Action Result checkpoints followed by one Final Draft checkpoint;
- no Action, checkpoint, budget, Lease, or reasoning fields in the browser projection.

After Worker loss or Lease reclaim, the next Worker loads the same Run and Job, validates the checkpoint prefix, and resumes the first missing Action, model decision, or publication boundary. Stop remains terminal; Retry creates a new Run, Job, deadline, budgets, and empty checkpoint sequence.

# Sprint 2A Agent Loop Local Runbook

## What This Slice Runs

Sprint 2A is a source-less, model-knowledge Chat backed by the production-shaped Agent path:

```text
Browser
  -> Control Plane admission transaction (User Message + Agent Run + Agent Job)
  -> PostgreSQL Job notification / five-second fallback scan
  -> independent Worker
  -> fixed Agent Loop (load -> context -> Bifrost -> publish)
  -> PostgreSQL Run notification
  -> Control Plane snapshot reload
  -> per-Run SSE
  -> Browser
```

Messages and Runs are durable PostgreSQL authority. Notifications are wake-up hints. The browser does not poll and Bifrost does not stream tokens in this slice.

## Prerequisites

- Go 1.24 or newer
- Node.js 22 or newer and npm
- Docker with Compose
- An Alibaba Cloud Model Studio DashScope API key for a live answer

Automated tests use a controlled local HTTP upstream and do not require Provider credentials.

## Configure The Live Model

Copy the local Compose environment template and edit only the untracked copy:

```bash
cp infra/compose/.env.example infra/compose/.env
```

Set `DASHSCOPE_API_KEY` in `infra/compose/.env`. Bifrost reads it inside the container. Do not commit the key or paste it into logs.

The default route is:

```text
NANO_CHAT_MODEL=aliyun/qwen-flash
NANO_BIFROST_URL=http://127.0.0.1:56666
```

Override either variable in the shell before `scripts/start` when testing another local Bifrost route. The Agent Loop remains provider-neutral.

## Start

```bash
scripts/reset
scripts/bootstrap
scripts/start
```

Open `http://localhost:5173`, register, create a Notebook, and type a question in Chat. An empty Notebook automatically creates one private Chat. Expected UI transitions are:

```text
Waiting to start… -> Generating answer… -> complete Assistant Message
```

The completed answer is labeled `Based on model knowledge` (or `基于模型知识`). The activity text is product status, not model chain-of-thought.

## Verify

Run deterministic gates:

```bash
scripts/test-go
scripts/test-web
```

The Go suite starts PostgreSQL and covers atomic admission, idempotent replay, concurrency, RLS isolation, queue claim, latest-20 context selection, controlled Bifrost success/failure, transactional publication, shared Run notifications, and SSE reconnect. The frontend suite covers Chat bootstrap, stable Message UUID retry, queued/running/completed/failed projection, refresh recovery, localization, accessibility, lint, type-check, and production build.

For a live smoke, first confirm Bifrost and both Go processes are ready:

```bash
scripts/health
```

Then submit one question through the browser. This spends Provider tokens and is intentionally not a deterministic CI gate.

## Inspect Durable State

Use local development credentials only:

```bash
docker compose -f infra/compose/compose.yaml exec postgres \
  psql -U nano -d nano -c \
  "select id, status, input_message_id, output_message_id, error_code from agent_runs order by created_at desc limit 5;"

docker compose -f infra/compose/compose.yaml exec postgres \
  psql -U nano -d nano -c \
  "select id, run_id, status, started_at, finished_at from agent_jobs order by created_at desc limit 5;"

docker compose -f infra/compose/compose.yaml exec postgres \
  psql -U nano -d nano -c \
  "select id, role, answer_mode, left(content, 100) from chat_messages order by created_at desc limit 10;"
```

Successful execution leaves one User Message, one completed Run, one succeeded Job, and one `model_knowledge` Assistant Message. Terminal model failure leaves the User Message plus failed Run/Job and no Assistant Message.

## Failure Guide

- Chat stays `queued`: check Worker logs and PostgreSQL readiness. A lost notification should still be found by the five-second scan.
- Run becomes `model_unavailable`: check Bifrost readiness and the provider key/configuration. Agent Loop does not add another retry layer over Bifrost.
- Run becomes `model_timeout`: the per-Run deadline elapsed; no partial Assistant Message is published.
- A second send returns `active_run_conflict`: wait for the current Run to become terminal. Sprint 2A does not queue conversational turns.
- Browser connection drops: reload the Chat. The snapshot restores durable Messages and reconnects the active Run by ID.

## Deliberate Exclusions

- Sprint 2A: no RAG, Source ingestion, retrieval, MCP, tool calls, token deltas, interruption, process-loss recovery, or durable Trace.
- Sprint 2B: cooperative interruption, late-publication prevention, leases, heartbeats, fencing, and safe recovery after process loss.
- Sprint 3: bounded multi-step Agent Action execution, Run Checkpoints, and recovery from the first incomplete step.
- Sprint 4: reusable observability/audit SDK, Run Events, Model Call payload governance, Durable Agent Trace, and Trace UI.

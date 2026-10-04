# Nano Notebook

[English](README.md) | [中文](README.zh-CN.md)

A source-grounded research workspace for individual researchers and deep learners. Collect trusted material in a bounded Notebook, ask multi-step questions across your Sources, and verify every important conclusion against original evidence — not model knowledge.

Nano Notebook focuses on rigorous evidence-grounded research, not general-purpose chat.

## Features

- **Multi-format Source ingestion** — upload PDF, DOCX, PPTX, TXT, Markdown, HTML, YouTube transcripts, audio, and images. Each Source is processed through a durable pipeline with typed Evidence Coverage.
- **Private, source-grounded Chat** — every answer is strictly grounded in your selected Notebook Sources. Inline Citations link key claims to their supporting evidence.
- **Studio outputs** — generate a shared Notebook artifact from a chosen set of Sources: a Report, a Flashcards deck, a Mind Map, or a Data Table. Grounding is enforced structurally, not just prompted for: every section, card, node, or row must cite Source IDs drawn from an immutable, pinned Evidence Set, and generation is rejected if a cited Source has since changed or fallen out of that set. Unlike Chat, a Studio Output is a shared product visible to every Notebook Member, generated through the same durable Agent/job pipeline and streamed to the client over SSE as it progresses.
- **Durable Research Agent** — multi-step, read-only Agent runs that survive page reloads. Stop, retry, or navigate away without losing work. Durability is checkpoint-based: the runtime persists a checkpoint only after an Action Proposal, a completed Action Result, or a Final Draft is accepted, identified by a stable key and a canonical-JSON content hash. Checkpoint appends are idempotent, so a crash between "committed" and "acknowledged" is reconciled by re-comparing hashes instead of re-inserting, and a resumed Attempt simply continues from the first unaccepted step.
- **Runtime research subagents** — the main researcher chooses tasks at runtime with `spawn_agent`. Children inherit its pinned model, tools, skills, evidence scope, shared budget, and deadline, with independent transcripts, TODOs, and workspace files. The parent continues working and collects terminal results with `list_agents` or `wait_agent`; waiting releases the worker. Results are persisted in PostgreSQL and returned as tool results. Children cannot spawn descendants or message each other.
- **Sandboxed Python for Research** — the researcher can call `run_python` for calculations and table analysis. Each call runs in a fresh, network-isolated E2B microVM that holds no Nano credentials and receives only the named workspace files. Text files the program writes to `output/` come back as checkpointed `data/` workspace files, so crash replay converges on the same state. The tool is hidden unless `NANO_E2B_API_KEY` is set.
- **Prompt Catalog & versioning** — every model-facing instruction is an immutable `PromptVersion` (identity + version + contract + content, content-addressed by SHA-256). Once a version is admitted for an Agent Run, the database rejects any attempt to mutate or delete it, and the exact version used is pinned on the run — so a run's behavior stays reproducible and auditable even as prompts evolve. Current prompts live in `internal/promptcatalog/prompts`; retired versions, including the old Source Discovery planner, live in `internal/promptcatalog/archive/prompts` and remain resolvable for pinned runs.
- **Notebook sharing with roles** — invite Viewers and Editors with Enterprise-style permissions. Sources are shared; every Member's Chat history stays private.
- **Grounded answers with passage-level Citations** — factual claims and synthesized conclusions require inline Citations that open the original Source at the relevant passage.

## Tech stack

| Layer | Technology |
| --- | --- |
| Backend | Go, modular monolith with PostgreSQL, MinIO, Qdrant |
| Frontend | React 19, TypeScript, Vite, Tailwind CSS 4, shadcn/ui |
| Model gateway | Bifrost (OpenAI / Gemini / Qwen) |
| Observability | Durable Agent Trace (custom collector), OpenTelemetry (Traces), Prometheus + Grafana (Metrics) |

## Observability

Nano ships three observability surfaces, covering three different questions: "what exactly happened in this one Agent Run" (Durable Agent Trace), "what happened in this one request across services" (OpenTelemetry traces), and "is the system healthy overall" (metrics).

### Durable Agent Trace

Every Agent Run gets exactly one Trace with exactly one root Trace Span, reconstructing every execution attempt — Model calls, Actions (tool/retrieval calls), Job attempts, and Agent executions all nest underneath it as child spans. Unlike ordinary tracing, this trace is **mandatory and never sampled or expired**: it's the durable record the product itself depends on, not just a debugging aid.

- **Exporter** — `internal/agentobs/otelbridge` bridges the Agent runtime's internal span/event records into the OpenTelemetry SDK and ships them via OTLP to Jaeger, for general-purpose, sampled request tracing alongside the rest of the stack.
- **Collector** — a separate, purpose-built `nano-collector` service (`cmd/collector`, `internal/collector`) ingests the same runtime records over HTTP into a dedicated `nano_observability` Postgres database, isolated from the application's operational database. This is the store the in-app Trace Dashboard reads from, and it's what makes the Durable Research Agent's "resume after reload" behavior independently inspectable after the fact.
- **Trace Dashboard** — an in-app, permission-gated view (`platform.trace.read`) with an Overview, a **Trace Tree** (a collapsible call tree of nested spans — Model call / Action / Job attempt / Agent execution — with per-span status and duration), a Timeline, an Inspector, Attributes, Events & Links, and an audited Replay view for sensitive span content (`platform.trace.replay`).

### OpenTelemetry traces & Metrics (Prometheus + Grafana)

- **Traces** (OpenTelemetry, via the exporter above) — a full per-request replay of what happened inside one Agent Run: model calls, tool calls, retrieval steps, and decisions.
- **Metrics** (Prometheus + Grafana) — aggregate operational health across the whole system, so a regression is visible on a dashboard instead of discovered from a user report. This covers:
  - **Task success rate** — completed vs. failed vs. cancelled, broken down by task type
  - **Error causes** — every failure is classified into a typed reason (model, tool, retrieval, storage, etc.), not just a generic error count
  - **Latency** — request time broken into stages (queue wait, model call, tool execution, retrieval, end-to-end), so a slow request can be traced to the stage responsible
  - **Memory/goroutine leak detection** — runtime gauges and alerts on the process's live heap and goroutine count, so a leak is caught by an alert instead of an OOM crash

Both Compose stacks (local and prod) provision Prometheus and Grafana as part of the standard topology — no separate setup step. In local development, `make start` brings them up alongside everything else, and the dashboard is available at `http://localhost:53000`.

## Getting started

### Prerequisites

- Go 1.25+
- Node.js 22+
- Docker

### Setup

```bash
# Bootstrap dependencies (PostgreSQL, Kafka, ClickHouse, MinIO, Qdrant, Bifrost, etc.)
make bootstrap

# Run database migrations and seed data
make migrate
make seed

# Start the Control Plane, Workers, and web dev server
make start
```

Additional commands:

```bash
make stop       # Stop all services
make reset      # Reset to a clean state
make health     # Check service health
make test-go    # Run Go tests
make test-web   # Run frontend tests
```

## Project structure

```
cmd/            Application entry points (Control Plane, Workers, Collector, etc.)
internal/       Go modules — Agent, Chat, Source, Retrieval, Notebook, Studio, etc.
web/            React + TypeScript SPA
docs/           Product discovery, technical architecture, and sprint runbooks
infra/          Infrastructure definitions
evals/          Evaluation cases and harness
scripts/        Bootstrap, migration, and operational scripts
```

## Documentation

- [Product requirements](docs/product-discovery/REQUIREMENTS.md)
- [Product discovery & decisions](docs/product-discovery/DISCOVERY.md)
- [Technical architecture](docs/technical-architecture/ARCHITECTURE.md)
- [Architecture glossary](docs/technical-architecture/CONTEXT.md)
- [Sprint runbooks](docs/implementation/)
- [Third-party notices](docs/third-party-notices.md)

## License

MIT © 2026 Xinyu Huang — see [LICENSE](LICENSE).

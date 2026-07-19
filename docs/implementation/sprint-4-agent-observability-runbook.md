# Sprint 4 Agent Observability Runbook

Sprint 4 installs an in-process Go Agent Observability SDK and retains one internal Durable Agent Trace for every newly admitted Agent Run. It adds no browser or public Trace API.

## Runtime Shape

- `internal/agentobs` contains the reusable record model, Trace Context, SDK Runtime, semantic conventions, instrumentation primitive, Memory Exporter, OpenTelemetry bridge, conformance suite, and independent example consumer.
- `internal/agent` owns Nano-specific conventions, the PostgreSQL exporter/loader, Lease-fenced Attempt recording, Model/Action adapters, and explicit lifecycle Events.
- `agent_traces` stores the one-to-one Run/Trace envelope. `agent_trace_records` stores immutable, contiguous Span start/end, Event, and Link records.
- Run Checkpoints remain execution authority. Trace records describe observed execution and never authorize publication or recovery.

The Worker installs PostgreSQL as required delivery and OpenTelemetry as best effort. A PostgreSQL recording error stops current progress; an OpenTelemetry bridge error only emits a bounded diagnostic.

## Local Verification

Run the complete Go and PostgreSQL gate:

```sh
scripts/test-go
```

Run the SDK concurrency and static-analysis gates:

```sh
go test -race ./internal/agentobs/... ./internal/agent ./internal/models -count=1
go vet ./internal/agentobs/... ./internal/agent ./internal/models
```

When the untracked `infra/compose/.env` contains `DASHSCOPE_API_KEY`, run the real Qwen journey:

```sh
scripts/test-sprint3-qwen
```

That smoke test now also validates Model Call and Action Spans plus the completed root Span.

## Operational Telemetry

The existing `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` or `OTEL_EXPORTER_OTLP_ENDPOINT` configuration remains authoritative. The Agent bridge uses the installed OpenTelemetry provider and never replaces the durable PostgreSQL Trace.

Operational spans may be sampled or dropped. Do not use Jaeger or another OTLP backend to decide whether a Run completed or a Checkpoint was accepted.

## Internal Diagnosis

This PostgreSQL loader was retired by Sprint 5. Use the Control Plane Admin API backed
by Collector Query for Trace diagnosis; Application PostgreSQL contains only the stable
Run-to-Trace identity anchor.

Interpret an unclosed Model or Action Span literally: the system durably observed its start but not a terminal outcome. Do not repair it by fabricating an error. A later physical execution has a distinct Span; reclaim and repeated work use `continues` and `retries` Links.

## Migration And Retention

- Historical terminal Runs receive no reconstructed Trace.
- Active pre-Sprint-4 Runs receive an `agent.execution` root and `nano.migration.adopted` Event. A legacy running Job returns to the queue so the new Worker creates a real Attempt Span.
- Trace rows cascade with their parent Run and have no independent TTL.
- Retry creates a new Run and Trace with a cross-Trace `retried_from` Link when the source Trace exists.

Never store raw prompts, responses, Action Results, Provider call IDs, headers, credentials, or hidden reasoning in Trace attributes. Unknown model cost is represented by `agent.cost.known=false`, never by a fabricated zero.

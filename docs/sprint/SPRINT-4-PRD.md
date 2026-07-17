# Nano Notebook Sprint 4 PRD

## Document Status

- **Sprint:** Sprint 4
- **Status:** Ready for review
- **Date:** 2026-07-17
- **Theme:** Reusable Go Agent Observability SDK and complete Durable Agent Trace
- **Delivery boundary:** Sprint 4 instruments the Sprint 3 checkpointed Agent runtime through a reusable Go SDK and persists one complete internal Durable Agent Trace per Agent Run. It adds no user-facing Trace, admin dashboard, standalone Collector, or multi-language SDK.

## 1. Decision

Sprint 4 delivers two inseparable outcomes:

1. a layered Go Agent Observability SDK that records Trace, Span, Event, and Link data through replaceable exporters;
2. a Nano Notebook Durable Agent Trace built with that SDK and retained for the lifecycle of its parent Agent Run.

The SDK is the instrumentation mechanism. The Durable Agent Trace is the mandatory Agent-owned result. Operational Telemetry may consume the same recording model but remains sampleable, expirable, and non-authoritative.

The Sprint proves reuse through Nano Notebook as the complete production consumer and a minimal independent Go Agent fixture as the second consumer. The SDK incubates inside this repository and is not published as an independent module during this Sprint.

## 2. Source Documents

This PRD derives from:

- `docs/product-discovery/CONTEXT.md`
- `docs/product-discovery/REQUIREMENTS.md`
- `docs/product-discovery/TECHNICAL-HANDOFF.md`
- `docs/technical-architecture/CONTEXT.md`
- `docs/technical-architecture/ARCHITECTURE.md`
- `docs/technical-architecture/adr/0012-bound-the-durable-runtime-to-product-jobs.md`
- `docs/technical-architecture/adr/0027-schedule-jobs-with-leases-and-workload-classes.md`
- `docs/technical-architecture/adr/0029-separate-operational-telemetry-from-durable-agent-traces.md`
- `docs/technical-architecture/adr/0030-cancel-cooperatively-and-publish-through-a-barrier.md`
- `docs/sprint/SPRINT-3-PRD.md`
- `docs/superpowers/specs/2026-07-17-go-agent-observability-sdk-design.md`
- OpenTelemetry client design principles: `https://opentelemetry.io/docs/specs/otel/library-guidelines/`
- OpenTelemetry Trace API: `https://opentelemetry.io/docs/specs/otel/trace/api/`
- Bifrost OpenAI-compatible response contract: `https://docs.getbifrost.ai/providers/supported-providers/overview`
- Bifrost request correlation options: `https://docs.getbifrost.ai/providers/request-options`
- Bifrost retries and fallbacks: `https://docs.getbifrost.ai/features/retries-and-fallbacks`

If this PRD conflicts with an approved architecture or product decision, the approved source wins unless this PRD explicitly records the superseding decision.

## 3. Sprint Goal

Create one reconstructable internal execution tree for every Agent Run without coupling Agent instrumentation to Nano Notebook storage or Bifrost payloads:

```text
Admit Run + create Trace and root Agent Execution Span
  -> claim or reclaim Job + create Attempt Span
  -> record Model Call and Agent Action Spans through wrappers
  -> record accepted Checkpoints and lifecycle facts as Events
  -> connect recovery and Retry causality through Links
  -> publish or terminate Run
  -> close root Span
  -> load and validate the complete Durable Agent Trace
```

The Sprint succeeds when the Trace distinguishes:

- work that started but has no observed completion;
- work that completed but was never accepted as a Checkpoint;
- accepted outcomes reused by recovery;
- repeated execution after process or Lease loss;
- infrastructure Attempts inside one Run;
- an explicit Retry that creates a new Run and linked new Trace.

## 4. Success Criteria

Sprint 4 is complete only when all of the following are true:

1. Every newly admitted Agent Run atomically creates exactly one Trace identity and one root Agent Execution Span.
2. Every Job claim or reclaim creates one Attempt Span; a reclaim records the observed expiry of the prior Attempt and Links the new Attempt to it.
3. Every Models boundary invocation creates one Model Call Span before the gateway request and one terminal record when a result or error is observed.
4. Every Agent Action execution creates a distinct Action Span, including repeated execution of the same logical Action after recovery.
5. Checkpoint acceptance, publication, cancellation, deadline expiry, recovery exhaustion, and terminal Run transition are durable Events rather than inferred log messages.
6. Started work without a durably observed outcome remains visibly incomplete and is never rewritten into a fabricated failure.
7. A user Retry creates a new Trace and a cross-Trace `retried_from` Link without inheriting the prior Trace identity or Checkpoints.
8. Instrumentation code depends only on the recording API and Agent semantic conventions; exporter and PostgreSQL types do not enter Models, Action, or Controller interfaces.
9. The Nano Notebook Durable Exporter is required and unsampled; OpenTelemetry delivery is best-effort and cannot substitute for durable success.
10. Model Call records contain application-normalized metadata, usage when reported, and explicit known/unknown cost state without raw gateway or Provider payloads.
11. Durable Trace data is internally queryable as one validated tree with Events and Links, but no Trace API or Trace details are exposed to the browser.
12. Trace access, RLS, retention, deletion, and purge follow the parent Run/Chat/Notebook authorization and lifecycle.
13. Core, exporter, instrumentation, fault-injection, and second-consumer tests prove that the SDK is reusable and that the Nano Trace remains complete across recovery.
14. Existing Sprint 3 Agent behavior, Checkpoint recovery, cancellation, Retry, deadline, Publication Barrier, and final browser projection remain unchanged.

## 5. Canonical Terms

- **Agent Observability SDK:** the reusable Go instrumentation boundary containing a small recording API, SDK Runtime, Agent semantic conventions, instrumentation adapters, and exporter contracts.
- **Durable Agent Trace:** the mandatory internal retained record with exactly one Trace and one root Span per Agent Run.
- **Trace Span:** duration-bearing execution work with at most one parent, represented by immutable start and optional terminal records.
- **Trace Event:** an immutable instantaneous fact attached to a Trace Span.
- **Trace Link:** a typed causal reference between Spans that does not change parent-child ownership and may cross Trace boundaries.
- **Trace Context:** the serializable Trace and current Span identity used to continue recording across process and Job boundaries.
- **Semantic Convention:** a versioned stable name and attribute contract for a class of Agent Span, Event, or Link.
- **Instrumentation Adapter:** a wrapper or lifecycle hook that translates a stable technical boundary into recording API calls.
- **Exporter:** a replaceable delivery implementation that receives validated SDK records without deciding Agent business semantics.
- **Delivery Class:** the configured guarantee for one exporter destination: required durable delivery or best-effort telemetry delivery.
- **Model Call Metadata:** application-normalized metadata for one Models Module invocation, distinct from raw Bifrost or Provider payloads.

`Session`, raw `ToolCall`, Bifrost log entry, and OpenTelemetry Span remain external or overloaded terminology. Nano Notebook uses Agent Run, Agent Action, Model Call, and Durable Agent Trace.

## 6. Internal Acceptance Journey

Sprint 4 reuses the Sprint 3 four-location time comparison journey and adds controlled process loss:

1. Admission creates the User Message, Agent Run, Agent Job, Trace identity, and root Agent Execution Span in one PostgreSQL transaction.
2. The first claim creates Attempt 1 and starts Model Call 1.
3. Model Call 1 returns an ordered `current_time` proposal; its call metadata is recorded and the Proposal Checkpoint is accepted.
4. Action 1 starts and completes normally; its Action Result is accepted.
5. Action 2 starts, returns in process, and the Worker is terminated before its terminal Trace record and Result Checkpoint commit.
6. The first Action 2 Span remains started with no observed terminal outcome.
7. Lease expiry permits Attempt 2. Its Span Links to Attempt 1 with `continues` semantics.
8. Attempt 2 repeats logical Action 2 in a new Action Span linked to the incomplete prior execution, accepts its Result, and completes the remaining Actions.
9. Later Model Calls and calculation Actions complete, the Final Draft Checkpoint is accepted, and Publication succeeds.
10. The Run and root Agent Execution Span terminate as completed.
11. An internal Trace loader reconstructs the tree and proves both physical executions of Action 2 while the Checkpoint sequence still contains only the one accepted logical result.
12. A later explicit Retry scenario creates a separate Run and Trace linked to the prior root with `retried_from` semantics.

The browser continues to display only stable Run lifecycle state and the final Assistant Message.

## 7. Ownership And Boundaries

### 7.1 SDK Core

The reusable SDK owns:

- Trace, Span, Event, Link, and Trace Context recording contracts;
- identifier generation and validation;
- parent-child and Link validation;
- timing, status, error, attribute, and schema-version rules;
- required versus best-effort exporter dispatch;
- bounded diagnostics, flush, and shutdown contracts;
- reusable conformance tests.

The SDK core imports no Nano Notebook Agent, Job, Models, PostgreSQL, Bifrost, or HTTP types.

### 7.2 Agent Module

The Agent Module owns:

- the one-Trace-per-Run completeness policy;
- Nano-specific semantic conventions;
- the PostgreSQL Durable Exporter and Trace loader;
- Trace authorization, retention, and purge behavior;
- explicit Checkpoint, publication, cancellation, and terminal Events;
- reconstruction validation for Nano Notebook Runs.

Durable Agent Trace remains Agent Module data even though the recording mechanism is reusable.

### 7.3 Models Module

Models continues to own Bifrost protocol conversion. It additionally presents Provider-neutral Model Call Metadata to its instrumentation adapter. Raw response bodies, Provider call identifiers, Bifrost-private fields, and secrets never cross into the Agent domain.

### 7.4 Job Runtime

The Job Runtime owns claim, Attempt, Lease, heartbeat, reclaim, release, and recovery exhaustion. It emits lifecycle recording calls but does not own Trace storage or reinterpret Agent outcomes.

### 7.5 Application Composition

The Control Plane and Worker install SDK Runtime configuration and exporters at startup. Instrumented libraries depend on the recording API; only application composition knows the concrete exporters.

## 8. SDK Shape

```text
Instrumented Agent application
        |
        v
Recording API + Agent semantic conventions
        |
        v
SDK Runtime
  - identity and context
  - hierarchy and Links
  - timing, status, errors, validation
  - delivery dispatch
        |
        +--> required Nano Durable Exporter
        +--> best-effort OpenTelemetry Exporter
        `--> Memory Exporter for tests

Instrumentation adapters:
  Models | Agent Actions | Job lifecycle
Future optional adapters:
  Retrieval | Memory
```

The SDK is not a standalone service. It runs in-process and sends records to configured exporters. A future Collector exporter may be added without changing recording call sites.

## 9. Recording API Contract

The conceptual recording surface supports:

- start one root Trace Span;
- start one child Span from Trace Context;
- end one Span with a terminal status and bounded attributes;
- append one immutable Event;
- append one typed Link from a source Span to a target Trace and Span;
- serialize and restore Trace Context across process boundaries;
- flush and shut down configured exporters.

The API follows these rules:

- one Span has zero or one parent;
- one Link never changes parentage;
- Trace and Span identifiers are opaque, globally unique, non-secret values;
- records carry schema and semantic-convention versions;
- timestamps are explicit facts supplied or observed at the recording boundary;
- application-domain status is not inferred by the SDK;
- attributes are typed, bounded, and validated before exporter dispatch;
- a required recording call returns a durable result or an error;
- best-effort failure is reported diagnostically and does not change the required result.

No global mutable SDK configuration is required by instrumented code. The final application owns runtime installation and dependency injection.

## 10. Agent Semantic Conventions

### 10.1 Reusable Agent Semantics

The first version defines common names for:

- `agent.execution`
- `agent.model.call`
- `agent.action`
- `agent.retrieval`
- `agent.memory`

Retrieval and Memory establish reusable conventions and test fixtures but are not production Nano Notebook execution paths in Sprint 4.

Common attributes include bounded identifiers and normalized fields for:

- model and Provider names;
- operation name and status;
- start, end, and duration;
- safe error kind;
- input, output, cached, and reasoning token counts when reported;
- monetary cost amount, currency, source, and known/unknown state;
- instrumentation scope and version.

### 10.2 Nano Notebook Extensions

Nano-specific names cover:

- Run admission and terminal outcome;
- Job Attempt and attempt number;
- Lease expiry and reclaim;
- Checkpoint acceptance;
- Publication Barrier;
- cancellation and deadline expiry;
- recovery exhaustion;
- Run Retry.

Nano-specific conventions live outside the generic SDK core and use a distinct namespace.

## 11. Automatic And Explicit Instrumentation

### 11.1 Automatic Wrappers

Stable technical interfaces use decorators:

- Models wrapper creates Model Call Spans;
- Action executor wrapper creates Action Spans;
- future Retrieval and Memory wrappers follow the same pattern.

Wrappers automatically:

- derive the child Trace Context;
- create the required start record before invoking the wrapped interface;
- measure time;
- normalize safe metadata and error kind;
- create the terminal record when an outcome is observed;
- preserve the wrapped interface's business result and error behavior.

### 11.2 Explicit Domain Events

The application explicitly records facts that a wrapper cannot infer:

- Proposal, Action Result, and Final Draft Checkpoint accepted;
- Publication Barrier passed or rejected;
- Run cancelled, expired, failed, or completed;
- Retry admitted;
- recovery exhausted.

The Sprint does not use reflection, runtime patching, or hidden global hooks to discover domain behavior.

## 12. Durable Trace Lifecycle

### 12.1 Admission

The admission transaction creates:

- User Message;
- Agent Run;
- Agent Job;
- Trace identity;
- root Agent Execution Span start record;
- Run-admitted Event.

Failure to create the required Trace records rolls back the whole admission. Transport idempotency replay returns the originally admitted Run and Trace without creating duplicates.

### 12.2 Claim And Reclaim

The Job claim transaction creates the Attempt Span before returning the claim to a Worker.

On reclaim it also:

- records the prior Lease expiry observed by PostgreSQL;
- creates the next Attempt Span;
- adds a `continues` Link to the prior Attempt;
- increments the existing Job attempt number.

If Attempt three has expired, the recovery-exhaustion transaction records the terminal Event and ends the root Span while failing Run and Job together.

### 12.3 Model And Action Work

Each physical Model Call and Agent Action execution receives a new Span identity. A repeated execution carries the same logical decision or Action identity as bounded metadata and may Link to the prior incomplete execution.

Model Decision or Action Result acceptance remains a separate Checkpoint Event. A completed Span does not imply that its result became runtime authority.

### 12.4 Publication And Terminal State

Publication records a duration-bearing Span. The existing transaction that commits Assistant Message, Run, and Job terminal state also records:

- publication outcome;
- terminal Run Event;
- root Agent Execution Span end.

Cancellation, deadline expiry, and terminal failure end the root Span in the same authoritative transaction that ends the Run. In-flight child Spans without an observed terminal result remain unclosed.

### 12.5 Explicit Retry

Retry admission creates a new Run, Job, Trace, and root Span. It adds a cross-Trace `retried_from` Link to the prior root Span and inherits no prior Checkpoint or Trace identity.

## 13. Trace, Span, Event, And Link Rules

### 13.1 Trace Tree

- one Agent Run has exactly one Trace;
- one Trace has exactly one root Agent Execution Span;
- Attempt Spans are direct children of the root;
- Model Call, Action, and Publication Spans are children of the active Attempt;
- parentage is immutable after start;
- a terminal record may appear at most once for one Span identity;
- no completion is synthesized merely because a process disappeared.

### 13.2 Events

Events are append-only and idempotent by stable logical identity. An Event records a fact, not a mutable state snapshot. Duplicate same-identity/same-payload delivery reconciles; a conflicting payload is an invariant failure.

### 13.3 Links

Each Link contains:

- source Trace and Span identity;
- target Trace and Span identity;
- typed relationship;
- semantic-convention version;
- observed timestamp and bounded attributes.

Initial relationships are:

- `continues` for a reclaimed Attempt;
- `retries` for a repeated physical execution when the prior execution is known;
- `retried_from` for a new Run created through the product Retry command.

Links cannot authorize work, transfer ownership, merge Trace lifecycles, or imply Checkpoint acceptance.

## 14. Model Call Records

One Models Module invocation creates one Model Call Span even when Bifrost performs multiple internal Provider retries or fallbacks.

The start record includes:

- application Model Call identity;
- requested application model name;
- decision ordinal being attempted;
- whether Agent Action definitions are enabled;
- bounded input counts and hashes, not raw messages or prompts.

The terminal record includes when reliably observed:

- success or safe error kind;
- normalized result kind: Final Draft, Action Proposal, invalid response, timeout, or unavailable;
- finish reason;
- selected normalized Provider and model;
- input, output, total, cached, and reasoning tokens;
- gateway retry or fallback counts when synchronously available;
- latency;
- cost amount and currency with an explicit source.

Unknown metadata remains absent or explicitly unknown. Unknown cost is never stored as zero. Sprint 4 does not query Bifrost's log database or enable raw content logging to fill missing fields.

The Models adapter expands its Provider-neutral result without exposing the Bifrost wire response conceptually as:

```go
type ModelOutcome struct {
    Decision ModelDecision
    Metadata ModelCallMetadata
}
```

Provider response validation remains in Models. Agent instrumentation consumes only normalized metadata.

## 15. Durable Exporter And Storage

### 15.1 Storage Model

The Agent Module adds two internal PostgreSQL tables.

`agent_traces` establishes one Trace per Run:

| Field | Contract |
| --- | --- |
| `trace_id` | Opaque Trace identity |
| `run_id` | Unique parent Agent Run; delete cascades |
| `root_span_id` | Unique root Agent Execution Span identity |
| `schema_version` | Trace envelope version |
| `created_at` | PostgreSQL admission timestamp |

`agent_trace_records` stores append-only records:

| Field | Contract |
| --- | --- |
| `trace_id` | Parent Trace |
| `sequence_no` | Contiguous durable order assigned inside one Trace |
| `identity_key` | Stable idempotency identity |
| `record_kind` | `span_started`, `span_ended`, `event`, or `link` |
| `span_id` | Subject or source Span identity |
| `parent_span_id` | Immutable parent for `span_started`; otherwise nullable |
| `name` | Versioned semantic name |
| `target_trace_id` | Link target Trace; otherwise nullable |
| `target_span_id` | Link target Span; otherwise nullable |
| `occurred_at` | Fact timestamp |
| `payload_version` | Kind-specific payload version |
| `payload` | Bounded normalized JSON metadata |
| `payload_sha256` | Canonical payload hash for reconciliation |
| `created_at` | PostgreSQL persistence timestamp |

Required constraints enforce kind-specific shape, unique `(trace_id, sequence_no)`, unique `(trace_id, identity_key)`, one start and at most one terminal record per Span, valid same-Trace parents, and resolvable Link targets under the authorized lifecycle.

### 15.2 Append And Reconciliation

Required durable writes follow the Sprint 3 Checkpoint precedent:

1. validate the record before storage;
2. append under current Run and, where applicable, current Lease authority;
3. on uncertain commit, reload by `identity_key`;
4. matching kind and canonical hash means committed;
5. absence under unchanged authority permits retry with the same identity and payload;
6. conflicting content is an invariant failure;
7. stale Workers cannot append authoritative completion or acceptance records.

Admission, claim/reclaim, cancellation, expiry, exhaustion, and publication records participate in their owning PostgreSQL transactions. The generic exporter contract does not expose PostgreSQL transaction types; the Nano Durable Exporter provides the application-specific transactional integration.

### 15.3 Bounds

Sprint 4 defaults are configurable and intentionally above the bounded Sprint 3 journey:

| Bound | Default |
| --- | ---: |
| One Trace record payload | 16 KiB |
| Trace records per Run | 256 |
| Total encoded Trace payload per Run | 1 MiB |
| Attributes per record | 64 |
| Links per Span | 8 |

Bounds apply before durable append. Oversized or structurally invalid application records are deterministic instrumentation failures rather than silently truncated audit data.

## 16. Delivery And Failure Semantics

### 16.1 Required Durable Delivery

- the Durable Exporter is always installed for Agent execution;
- required start records commit before the corresponding Model Call or Action begins;
- a required terminal or acceptance record must commit before its result is used by later work;
- transient durable recording errors return from Controller execution, stop heartbeat progress, and allow existing Lease expiry and reclaim to recover;
- deterministic invalid-record or bound failures terminate the Run with safe internal code `agent_trace_invalid`;
- repeated infrastructure failure ultimately follows existing `recovery_exhausted` semantics.

### 16.2 Best-Effort Operational Delivery

- OpenTelemetry may sample, buffer, expire, or drop records;
- telemetry exporter failure never changes Run, Job, Checkpoint, Message, or durable Trace authority;
- bounded SDK diagnostics report telemetry failure without including user content or secrets.

### 16.3 Unclosed Work

A started Span with no terminal record means no terminal outcome was durably observed. A later reclaim may record the observed expiry of its parent Attempt and Link new work to the earlier Span, but must not invent a Model or Action failure result.

## 17. Trace Context And Correlation

- Trace Context contains opaque Trace and current Span identity, schema version, and bounded correlation fields.
- it is serializable and independent of in-memory Span objects;
- one Agent Job resolves the same Trace from its Run after every reclaim;
- the claimed Attempt Context becomes the parent context for Controller, Models, Actions, and publication;
- Go `context.Context` carries in-process correlation and cancellation but is not durable authority;
- Nano application Run ID, Job ID, attempt number, logical Action ID, and Model Call ID are recorded as bounded correlation attributes;
- Bifrost receives a generated correlation request ID when supported, but that gateway identifier is metadata rather than Trace identity;
- credentials, Lease Tokens, authorization headers, and session tokens never enter Trace Context or Trace payloads.

## 18. Authorization, Access, And Lifecycle

- Durable Agent Trace is internal Agent Module data.
- browser sessions and ordinary product REST/SSE queries receive no Trace access.
- the Control Plane may create admission, cancellation, Retry, and terminal records only through the commands that own those transitions.
- a Worker may read the Trace for an authorized active Run and append only under current Run/Lease authority where required.
- cross-Notebook, cross-Chat, and cross-creator Trace reads are denied by Go authorization and PostgreSQL RLS.
- Trace data follows the parent Run, Chat, Notebook, membership, and account deletion lifecycle.
- Trace tables cascade or purge with the parent Run and have no independent Sprint 4 TTL.
- access revocation takes effect before asynchronous physical purge, consistent with existing Deletion Tombstone policy.
- a future admin Trace projection requires a separate authorization and product decision and is not implied by internal storage access.

## 19. Data Governance

The Trace may retain:

- normalized Span, Event, and Link metadata;
- safe application identifiers and authoritative-data references;
- hashes, sizes, counts, statuses, safe error kinds, timing, token usage, and known cost metadata;
- normalized Agent Action name and execution status;
- Checkpoint identity and kind references.

The Trace does not retain:

- raw Provider or Bifrost request/response bodies;
- HTTP headers;
- full prompts, Chat transcripts, or duplicate Action Result payloads;
- Provider tool-call IDs;
- hidden reasoning, chain of thought, or thinking tokens as content;
- stack traces or unbounded error messages;
- credentials, API keys, cookies, authorization headers, Lease Tokens, or local environment values.

User content remains in its existing authoritative Message and Checkpoint locations. Trace records refer to those identities and store hashes or bounded summaries only when needed for correlation.

## 20. Operational Telemetry Boundary

The SDK may bridge the same recording calls to OpenTelemetry, but the two products remain distinct:

| Concern | Durable Agent Trace | Operational Telemetry |
| --- | --- | --- |
| Owner | Agent Module | Platform operations |
| Completeness | Mandatory for Agent Runs | Best effort |
| Sampling | Forbidden | Allowed |
| Retention | Parent Run lifecycle | Operational policy |
| Authority | Internal execution history | Diagnostics only |
| Storage | Agent PostgreSQL exporter | OTLP backend |
| Failure impact | May block Agent progress | Must not change Agent correctness |

Sprint 4 does not replace the existing HTTP/startup OpenTelemetry setup or require Jaeger to reconstruct a Run.

## 21. API, SSE, And Frontend

Sprint 4 adds no browser Trace surface.

- existing Run REST/SSE projections remain `queued | running | completed | failed | cancelled`;
- no Trace, Span, Event, Link, Model Call, token, cost, Action, Attempt, Lease, or Checkpoint detail enters product JSON;
- no user-visible Reasoning Trace or Action history is added;
- no admin route or dashboard is added;
- internal Trace loading is exposed through Agent Module interfaces and tests, not a public HTTP API;
- the existing source-less answer disclosure remains unchanged.

## 22. Migration And Upgrade Safety

The migration creates Trace tables, constraints, indexes, roles, grants, and RLS policies without rewriting Sprint 3 Checkpoints.

Historical behavior:

- terminal Runs created before Sprint 4 receive no fabricated reconstructed Trace;
- active pre-Sprint-4 Runs require a controlled deployment boundary with old Workers stopped;
- the migration creates a root Trace and a `migration.adopted` Event for each active Run, clearly marking that pre-migration execution history is unavailable;
- new Workers may continue those active Runs only after migration commit;
- every Run admitted after migration must have a complete Trace from admission;
- Retry of a historical terminal Run creates a new complete Trace and may Link to the old Run only when a target Trace exists.

Migration tests cover clean databases, populated terminal Sprint 3 history, and pre-migration queued and running Runs.

## 23. Verification

### 23.1 SDK Core Contracts

- Trace and Span identity validation;
- exactly one parent and immutable parentage;
- Trace Context serialization and restoration;
- immutable Event and typed Link validation;
- cross-Trace Link support;
- start and terminal lifecycle rules;
- concurrent recording safety;
- attribute and payload bounds;
- required and best-effort exporter isolation;
- flush and shutdown behavior.

### 23.2 Exporter Conformance

Memory and PostgreSQL exporters run the same reusable conformance suite:

- same identity and same canonical payload reconcile;
- conflicting identity fails;
- sequence and lifecycle constraints hold;
- started records remain visible without terminal records;
- terminal records are unique;
- Events are immutable;
- Links resolve and preserve parentage;
- schema versions round-trip;
- required errors return to the caller;
- best-effort failures cannot report durable success.

### 23.3 Instrumentation Adapters

- Models success, invalid response, timeout, unavailable, and cancellation;
- Final Draft and ordered Action Proposal metadata;
- token usage present and absent;
- cost known and unknown without zero ambiguity;
- Agent Action success, domain error, cancellation, and repeated physical execution;
- wrapped result and error behavior remains unchanged;
- no raw prompt, response, Action Result, or secret enters records.

### 23.4 PostgreSQL Integration

- admission creates Run, Job, Trace, root Span, and Event atomically;
- idempotency replay creates no duplicate Trace;
- claim/reclaim and Attempt records commit together;
- stale Lease cannot append Worker-owned records;
- cancellation, expiry, exhaustion, and publication end root Span with authoritative state;
- parent Run deletion removes Trace data;
- RLS blocks cross-user and application-browser access;
- clean and populated Sprint 3 databases upgrade safely.

### 23.5 Fault Injection

Inject loss or uncertain commit:

1. before Model Call start record: gateway is not called;
2. after Model Call start but before response: Span remains incomplete and later call is distinct;
3. after Model Call completion but before Checkpoint: completed call remains unaccepted and recovery may call again;
4. before Action start record: Action is not executed;
5. after Action execution but before terminal record: Span remains incomplete and recovery may repeat the Action;
6. after terminal record but before Result Checkpoint: completed Action is visible but unaccepted;
7. after Checkpoint Event uncertainty: identity/hash reconciliation prevents duplicate or conflicting acceptance Events;
8. after Lease reclaim: new Attempt Links to the prior Attempt and stale Worker cannot append;
9. after Final Draft Checkpoint: publication completes with no new Model Call;
10. during publication record commit: Trace and Run terminal state reconcile together;
11. during Retry admission: exactly one new linked Trace is created.

### 23.6 Second Consumer

A minimal Go fixture implements:

```text
input -> model -> action -> model -> output
```

It uses the recording API, Agent semantic conventions, Models/Action wrapper pattern, and Memory Exporter without importing Nano Notebook Agent, Job, Checkpoint, database, or Bifrost types.

### 23.7 End To End

- the real configured Qwen four-location journey produces one completed Trace;
- a deterministic process-loss journey shows incomplete and repeated Action executions while preserving one accepted Checkpoint result;
- Stop, deadline, recovery exhaustion, and Retry produce the required Events and root outcomes;
- reload restores the existing Chat with no Trace UI;
- querying Jaeger is unnecessary to prove Durable Agent Trace completeness.

## 24. Explicitly Out Of Scope

- user-visible Reasoning Trace, Action history, or execution stages
- admin backend, Trace tree dashboard, timeline, filters, token/cost charts, or alerts
- public or browser-accessible Trace API
- standalone Collector service or remote ingestion protocol
- publishing an independent Go module or guaranteeing `v1` compatibility
- Python, TypeScript, or other language SDKs
- general enterprise audit events outside Agent execution
- raw prompt, completion, Provider request, or Provider response retention
- Bifrost log-store ingestion or raw-content logging
- Provider pricing catalog ownership when trustworthy cost metadata is unavailable
- Source retrieval, Memory, Evidence, Citation, or Grounded Answer production integration
- new Agent Actions, parallel execution, MCP, plugins, external tools, Sandbox, or code execution
- Trace sampling, partial durable Trace retention, or independent Trace TTL
- replacing Run Checkpoints with Trace records
- using Trace data as execution authorization or publication authority

## 25. Delivery Sequence

Implementation should preserve independently verifiable seams in this order:

1. SDK core record model, Trace Context, Memory Exporter, and contract tests;
2. minimal Agent semantic conventions and second-consumer fixture;
3. PostgreSQL Trace schema, Nano Durable Exporter, loader, RLS, and migration tests;
4. admission root Trace and Run lifecycle Events;
5. Job claim/reclaim Attempt Spans and Links;
6. Models metadata contract and Model Call instrumentation;
7. Agent Action instrumentation and Checkpoint acceptance Events;
8. publication, cancellation, expiry, exhaustion, and Retry integration;
9. OpenTelemetry best-effort bridge and exporter isolation tests;
10. fault-injection matrix and real Qwen acceptance journey.

Each step must keep Sprint 3 Agent behavior and recovery tests passing or deliberately replace them with stronger equivalent coverage. No later step may weaken the separation between Run Checkpoints as runtime authority and Durable Agent Trace as mandatory internal execution history.

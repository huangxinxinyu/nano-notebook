# Go Agent Observability SDK Design

**Date:** 2026-07-17

**Status:** Design decisions approved; written specification pending review

**First production consumer:** Nano Notebook Sprint 4 Durable Agent Trace

## 1. Decision

Build a layered, Go-first Agent Observability SDK whose first complete production use is Nano Notebook's Durable Agent Trace.

The SDK is an instrumentation mechanism, not the Durable Agent Trace itself. It provides a small recording API, a configurable runtime, minimal Agent semantic conventions, instrumentation adapters, and replaceable exporters. Nano Notebook continues to own the meaning and completeness policy of its Durable Agent Trace.

The SDK will incubate inside this repository. It becomes an independent Go module only after a second minimal Agent consumer proves that its core API does not depend on Nano Notebook types.

## 2. Goals

- Produce a complete Durable Agent Trace for every Nano Notebook Agent Run.
- Make Trace, Span, Event, Link, context propagation, timing, status, and error recording reusable across Go Agent implementations.
- Automate instrumentation at stable technical boundaries such as Models and Agent Actions.
- Keep Nano Notebook domain facts explicit and outside the reusable core.
- Allow applications to select storage and telemetry destinations without changing instrumentation call sites.
- Preserve a clear distinction between mandatory durable recording and sampleable Operational Telemetry.
- Establish evidence for or against extracting the SDK into a standalone Go module.

## 3. Non-Goals

- Multi-language SDKs or a language-neutral collection protocol.
- A standalone Collector service in Sprint 4.
- An administrative Trace dashboard or user-visible Reasoning Trace.
- A general enterprise audit platform for identity, Notebook, membership, or Source changes.
- A general Agent framework, workflow engine, or replacement for Nano Notebook's Agent Controller.
- Persisting raw Bifrost or Provider request and response payloads.
- Modeling every Agent framework concept in the first semantic convention set.

## 4. Design Principles

### 4.1 Separate Mechanism From Policy

The SDK owns reusable mechanisms: identifiers, parent-child relationships, links, timestamps, duration, status, context propagation, record validation, and exporter dispatch.

The application owns policy: what creates one Trace, which records are mandatory, whether a recording failure blocks work, which fields may be retained, and how records follow domain-resource deletion.

### 4.2 Generality Comes From Dependency Direction

Instrumented libraries depend only on the small recording API and relevant semantic conventions. The final application installs the SDK runtime, chooses exporters, and supplies policy.

The reusable core imports no Nano Notebook Agent, Job, Models, PostgreSQL, Bifrost, or HTTP transport types.

### 4.3 Keep The Public Surface Small

The stable surface describes recording operations rather than storage or application workflows. Runtime configuration, exporters, and instrumentation packages may evolve independently of the core API.

### 4.4 Prefer Explicit Facts Over Inference

Technical wrappers automate mechanically observable calls. Domain transitions such as Checkpoint acceptance and Publication Barrier success remain explicit events emitted by the owning application code.

## 5. Layered Architecture

```text
Instrumented Agent application
        |
        | uses
        v
Small recording API + Agent semantic conventions
        |
        | implemented by
        v
SDK runtime
  - identifiers and context
  - parent-child relationships and links
  - timing, status, errors, policy
  - exporter dispatch
        |
        +--> required Durable Exporter
        +--> best-effort OpenTelemetry Exporter
        +--> Memory Exporter for tests
        `--> future Collector Exporter

Instrumentation adapters wrap stable boundaries:
  Models | Agent Actions | Retrieval | Memory | Job lifecycle
```

### 5.1 Recording API

The recording API expresses Trace, Span, Event, Link, context propagation, status, errors, and bounded attributes. It has no storage, transport, gateway, or application-domain dependencies.

### 5.2 SDK Runtime

The runtime implements the recording API and applies application-installed policy. It owns identifier creation, hierarchy, correlation, timing, concurrency safety, schema validation, data limits, and delivery to configured exporters.

### 5.3 Semantic Conventions

The first convention set defines only common Agent execution concepts:

- Agent Execution;
- Model Call;
- Agent Action;
- Retrieval;
- Memory Operation;
- common status, error, latency, token, and cost metadata.

Applications extend this vocabulary through their own namespace. Nano Notebook extensions include Run admission, Job Attempt, Lease loss, Checkpoint acceptance, Publication Barrier, cancellation, and Run Retry.

### 5.4 Instrumentation Adapters

Instrumentation adapters translate framework or application interfaces into recording API calls.

- A Models wrapper records Model Call Spans and normalized call metadata.
- An Agent Action wrapper records Action Spans and normalized outcomes.
- Later Retrieval and Memory wrappers use the same pattern.
- Job lifecycle hooks record Attempt Spans and recovery facts.
- Nano Notebook's Controller explicitly records accepted Checkpoints and publication decisions.

Adapters may depend on the interfaces they instrument. The recording API and runtime do not depend on those adapters.

### 5.5 Exporters

The SDK ends at the exporter contract. Exporters own storage or transport-specific delivery, not business semantics.

- The Nano Notebook Durable Exporter persists the authoritative Durable Agent Trace.
- The OpenTelemetry Exporter emits sampleable Operational Telemetry.
- The Memory Exporter supports deterministic contract and integration tests.
- A future Collector Exporter may transmit records without changing instrumentation.

## 6. Trace Model

### 6.1 Trace

One Nano Notebook Agent Run creates exactly one Durable Agent Trace and one root Agent Execution Span at Run admission. The Trace covers queue time, all infrastructure Attempts, Agent execution, publication, and the terminal Run outcome.

An explicit user Retry creates a new Agent Run and new Trace. It does not reuse the prior Run's Checkpoints or Trace identity.

### 6.2 Span

A Span represents duration-bearing work and has zero or one parent. The main tree includes Agent Execution, Attempt, Model Call, Agent Action, Retrieval, Memory, and Publication operations as applicable.

A persisted started Span with no terminal record means that no completion was durably observed. The SDK does not fabricate failure for such a Span after process loss.

### 6.3 Event

An Event is an immutable instantaneous fact attached to a Trace or Span. Nano Notebook examples include Run admission, Checkpoint acceptance, Lease loss, cancellation request, and terminal state transition.

### 6.4 Link

A Link is a typed causal reference that does not change parent-child ownership. It may connect Spans inside one Trace or cross Trace boundaries.

Initial Nano Notebook relationships include:

- a reclaimed Attempt linked to the prior Attempt it continues;
- a repeated operation linked to an earlier incomplete execution when that relationship is known;
- a new Retry Trace linked to the prior Trace with `retried_from` semantics.

Every Link targets a Trace and Span identity. A cross-Trace relationship targets a Span in the other Trace, normally its root Agent Execution Span.

## 7. Nano Notebook Lifecycle

```text
Run admission
  -> atomically create Run, Agent Job, Trace identity, and root Agent Execution Span
  -> queue
  -> start Attempt Span on claim or reclaim
  -> automatically instrument Model Calls and Agent Actions
  -> explicitly record accepted Checkpoint Events
  -> start later Attempt Spans after recovery, with Links where applicable
  -> record Publication operation and terminal Run Event
  -> end root Agent Execution Span
```

Cross-process continuation carries a serializable Trace context, not an in-memory Go Span object. A Worker reconstructs recording context from durable identifiers when it claims work.

## 8. Delivery And Failure Semantics

Exporter destinations have explicit delivery classes.

### 8.1 Required Durable Delivery

- A required start record must persist before its corresponding Model Call, Agent Action, or other mandatory observed work begins.
- A durable recording failure is returned to the application and cannot be hidden by successful telemetry delivery.
- A result whose required completion or acceptance record cannot persist is not treated as durably recorded or accepted.
- Nano Notebook maps these failures into its existing Job recovery and exhaustion policy.

### 8.2 Best-Effort Telemetry Delivery

- Operational Telemetry may be sampled, buffered, expired, or unavailable.
- Telemetry exporter failure does not change Agent correctness or Durable Agent Trace completeness.
- The SDK reports its own best-effort delivery failures through bounded diagnostics.

### 8.3 Multi-Exporter Isolation

One exporter cannot redefine another exporter's delivery guarantee. OpenTelemetry success does not compensate for Durable Exporter failure, and OpenTelemetry failure does not invalidate a successful durable record.

## 9. Data Governance

- The SDK records application-normalized Model Call metadata rather than raw Bifrost or Provider payloads.
- Model Call metadata may include model identity, timing, result kind, finish reason, token usage, cost when known, error classification, and gateway retry count when reliably available.
- Application identifiers and references connect Trace records to authoritative Run, Checkpoint, Action, and Message data without duplicating durable content.
- Provider headers, raw request and response bodies, Provider tool-call identifiers, hidden reasoning, thinking tokens, and credentials are excluded.
- Durable Agent Trace data follows its parent Agent Run lifecycle and has no independent Sprint 4 TTL.
- Operational Telemetry retains its independent sampling and expiry policy.

## 10. API And Module Evolution

- Sprint 4 incubates the SDK boundary in this repository without immediately publishing an independent module.
- The core API remains smaller and more stable than runtime, exporters, and instrumentation adapters.
- A second minimal Go Agent fixture must consume the API without importing Nano Notebook domain types.
- If extracted, the SDK begins at `v0.x`; `v1` waits for production evidence that the core API, failure semantics, and exporter contract are stable.
- Semantic-convention and record-schema versions are independent of the Go module version so historical Trace records remain interpretable across code upgrades.
- New capabilities should be additive wherever practical; experimental instrumentation does not expand the stable core by default.

## 11. Verification Strategy

### 11.1 Core Contract Tests

Verify Trace, Span, Event, Link, context propagation, hierarchy, concurrency, lifecycle, data limits, and error semantics independently of an Agent framework or database.

### 11.2 Exporter Conformance Suite

Every exporter implementation is tested against one reusable contract suite covering record identity, idempotency, started records, immutable Events, resolvable Links, schema version preservation, delivery-class errors, flush, and shutdown behavior.

### 11.3 Instrumentation Tests

Fake Model, Action, Retrieval, and Memory interfaces verify that wrappers create the expected Spans, normalized metadata, statuses, and errors without depending on Nano Notebook's Controller.

### 11.4 Consumer Proofs

- Nano Notebook is the complete production consumer.
- A minimal independent Go Agent fixture proves that the reusable core requires no Nano Notebook types.

### 11.5 Nano Notebook Fault Injection

Integration tests cover Worker process loss, Lease expiry and reclaim, uncertain durable-record commits, repeated execution before Checkpoint acceptance, cancellation, recovery exhaustion, publication, and Run Retry. The reconstructed Trace must distinguish accepted outcomes from attempted but incomplete or repeated work.

## 12. Acceptance Criteria

The design is successful when:

1. Every Nano Notebook Agent Run has one reconstructable Durable Agent Trace from admission through terminal state.
2. Started but uncompleted executions and later recovery Attempts remain distinguishable.
3. Model and Action instrumentation is reusable and does not require Controller-specific recording code.
4. Nano-specific lifecycle facts remain explicit application extensions.
5. Durable and Operational exporters use the same recording model without sharing delivery guarantees.
6. A second Go Agent fixture and a new exporter can be added without changing the core recording API.
7. Raw gateway and Provider payloads are absent from persisted Trace records.
8. Parent Run deletion removes the associated Durable Agent Trace under the existing purge lifecycle.

## 13. Consequences

The design creates more structure than a Nano-only tracing package and requires explicit conformance and compatibility discipline. In exchange, it produces a testable SDK boundary, keeps storage and Provider integrations replaceable, and lets Nano Notebook validate a reusable Agent instrumentation model through real recovery behavior rather than a speculative standalone framework.

The SDK deliberately borrows the API/runtime/convention/exporter separation and Trace/Span/Event vocabulary familiar from OpenTelemetry while retaining a separate durable delivery contract. Operational Telemetry remains diagnostic; Durable Agent Trace remains mandatory Agent-owned history.

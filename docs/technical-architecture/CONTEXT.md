# Nano Notebook Technical Architecture Context

This glossary defines the canonical technical language used by the Nano Notebook architecture. Product concepts remain defined in `docs/product-discovery/CONTEXT.md`.

## Runtime Topology

**Control Plane**:
The synchronous application surface that authenticates requests, enforces Notebook permissions, accepts commands, and exposes durable state. It does not perform long-running Source or Agent work inside request handlers.
_Avoid_: Backend, API server when referring to the whole system

**Worker**:
An independently deployable process that claims durable work and executes Source, Agent, or evaluation jobs outside request lifetimes.
_Avoid_: Background goroutine, cron process

**Module**:
A cohesive ownership boundary inside the Go application with an explicit public interface and private persistence behavior. A Module is not a network service unless operational evidence later justifies extraction.
_Avoid_: Microservice, package when discussing ownership boundaries

**Principal**:
The verified User identity and request context on whose behalf a Control Plane command or Worker continuation is authorized.
_Avoid_: Current user, user ID supplied by a client

**Capability**:
A named product operation authorized from a Principal's Notebook role and resource relationship, independently of whether the relevant rows are visible.
_Avoid_: Permission flag, endpoint role check

**Job**:
A durable unit of asynchronous work whose state survives process failure and can be claimed, reclaimed after lease expiry, cancelled, or completed by a Worker.
_Avoid_: Goroutine, task when durability matters

**Agent Run**:
The user-visible durable lifecycle of one requested answer. It owns product status and the input/output Message relationship; one input Message may have later Runs after explicit user retries, but a Run does not double as Worker delivery state.
_Avoid_: Queue item, model request

**Run Retry**:
An explicit user request to answer the latest unanswered input Message again after its prior Run was cancelled or failed. It creates a new Agent Run, is unavailable after the Chat advances, and is distinct from automatic execution attempts inside an existing Run.
_Avoid_: Job retry, Attempt, reopening a terminal Run

**Run Cancellation**:
The durable product decision that an active Agent Run will publish no answer. It may become final before in-flight work actually stops, is never resumed from Checkpoints, and a later Retry creates a new Run.
_Avoid_: Pause, process kill, guaranteed Provider cancellation

**Agent Job**:
The single internal durable delivery record that tells an Agent Worker which Run to advance across its model and Action steps. It remains one Job across Checkpoints and infrastructure Attempts, and the browser never depends on its state.
_Avoid_: Agent Run, frontend status

**Job Lease**:
An expiring claim that permits one Worker attempt to advance a Job while heartbeats continue. Lease expiry enables recovery and does not imply that the prior attempt produced no side effects.
_Avoid_: Lock, exactly-once execution

**Lease Token**:
The identity of the current leased execution of a Job. Reclaiming the Job replaces the token so stale Workers can no longer heartbeat, fail, or publish for it.
_Avoid_: Session token, Worker identity, permanent ownership

**Run Checkpoint**:
An immutable, Provider-neutral durable boundary after an Agent outcome is accepted, from which later execution can reuse accepted results and continue with the first incomplete step. It contains no transient running state, raw Provider payload, or diagnostic history and is not a snapshot of a Worker process or an in-flight model generation.
_Avoid_: Mutable step status, process snapshot, partial-token continuation, Durable Agent Trace

**Workload Class**:
A fixed product category such as interactive Agent, Source Processing, or offline Eval/Reindex, used to reserve concurrency and prevent background work from starving user-facing Jobs.
_Avoid_: Arbitrary queue, customer-defined priority

## Source Processing

**Extractor Adapter**:
A least-privileged conversion boundary that turns one Source input into a Normalized Source Artifact without owning product state, durable credentials, or publication decisions. It may wrap a library, model call, binary, or isolated process while preserving the same contract.
_Avoid_: Source Module, Agent Sandbox

**Normalized Source Artifact**:
The canonical, parser-independent representation of extracted Source content and its citation coordinates, produced before retrieval indexing.
_Avoid_: Parsed file, parser output

**Evidence Revision**:
An immutable version of a Source's Normalized Source Artifact and its Evidence Unit address space. A Citation pins an Evidence Revision so later extraction, OCR, or transcription improvements cannot change the evidence originally cited.
_Avoid_: Source version, index version

**Evidence Unit**:
A stable, source-native addressable span within a Normalized Source Artifact, such as a page text range, slide element, transcript interval, or image region. Citations resolve to Evidence Units or ranges of them, never to retrieval-index records.
_Avoid_: Chunk, vector point when discussing citation identity

**Retrieval Chunk**:
A rebuildable, possibly overlapping evidence window formed from one or more Evidence Units for candidate retrieval. Its boundaries and identifiers may change when retrieval policy changes without changing Citation identity.
_Avoid_: Citation span, authoritative content

**Retrieval Index Version**:
An immutable, rebuildable retrieval projection of one or more Evidence Revisions under a specific chunking, dense, and sparse indexing configuration. It can be replaced or removed without changing Citation identity.
_Avoid_: Evidence Revision, authoritative Source

## Agent Execution

**Run Evidence Set**:
The fixed set of immutable Sources and their active Evidence Revisions selected when a question creates an Agent Run. The Run also pins the corresponding Retrieval Index Version; later Chat selection, Source processing, and new Sources do not enter it, while deletion of a member Source invalidates the active Run rather than silently changing its evidence.
_Avoid_: Current Sources, live Notebook contents

**Agent Controller**:
The Go component that advances an Agent Run through its fixed outer stages while validating and bounding model-selected, read-only research actions.
_Avoid_: Workflow engine, autonomous agent loop

**Agent Action**:
A model-proposed, Agent Controller-authorized operation from a finite application-defined set. Each Action has a canonical typed input and result, is read-only or pure computation, and remains distinct from Provider tool-call formats and general external tools.
_Avoid_: General Tool, Provider Tool Call, MCP Tool, command

**Action Registry**:
The application-owned catalog through which the Agent Controller discovers registered Agent Action definitions and executors. It is extensible by code registration while remaining closed to runtime plugins, external discovery, and model-defined Actions.
_Avoid_: Plugin manager, MCP registry, dynamic Tool marketplace

**Action Proposal**:
A Provider-independent, ordered model request to invoke one or more registered Agent Actions. It is input to Agent Controller validation, not execution authority.
_Avoid_: Tool Call, command, approved Action

**Model Decision**:
The Provider-neutral result presented to the Agent Controller by one completed model invocation. It contains exactly one Final Draft or one ordered Action Proposal batch.
_Avoid_: Raw Provider response, Chat completion, chain of thought

**Model Call**:
One Agent Controller invocation of the Models Module, recorded with application-normalized metadata even when the gateway performs multiple Provider attempts internally. It excludes raw gateway or Provider request and response payloads.
_Avoid_: Provider request, Bifrost response, Agent Run

**Action Result**:
The accepted typed outcome of one Agent Action, containing either success data or an expected domain error. It is durable Run working state consumed by later model decisions and reused after recovery.
_Avoid_: Tool response, log entry, Trace Event

**Final Draft**:
An accepted model-produced candidate answer that may become an Assistant Message only through the Publication Barrier.
_Avoid_: Assistant Message, published answer, raw model response

**Run Budget**:
The limits pinned to one Agent Run for model decisions, accepted logical Agent Actions, elapsed time, and retained Action Result size. Success and expected domain error consume one Action each, recovery re-execution does not consume another, and one final model decision without Actions is reserved for graceful exhaustion.
_Avoid_: Provider quota, Job retry policy, context window

**Fixed Agent Loop**:
The Sprint 2A orchestration seam that executes exactly one `LoadRun -> BuildContext -> InvokeModel -> PublishAnswer` pass. It is named a loop for compatibility with later typed action iteration, but it contains no speculative loop or tool execution today.
_Avoid_: Autonomous loop, generic workflow engine

**Context Builder**:
The component that constructs the bounded input for the next model call from authorized Chat content, the current Run checkpoint, accepted action results, selected Evidence Units, and versioned Agent configuration. Its output is a model-facing projection, not durable authority or a claim to capture model-internal memory.
_Avoid_: Transcript replay, memory store, model snapshot

**Publication Barrier**:
The final transactional authorization, Source-availability, and Citation-validity check that alone may turn an Agent draft into a durable Assistant Message. Late or incomplete work cannot bypass it.
_Avoid_: Stream completion, model success

**Retrieval Scope**:
The server-constructed intersection of an authorized Notebook and a Run Evidence Set that every retrieval channel must enforce before returning candidates.
_Avoid_: Client filter, vector-database tenant

## External Input

**Fetcher Adapter**:
A least-privileged outbound network boundary that snapshots one approved public Source URL under strict protocol, destination, redirect, size, and time policy without access to product databases or durable credentials.
_Avoid_: HTTP client inside the Control Plane, web-search tool

## Data Lifecycle

**Deletion Tombstone**:
A minimal non-content authority record that makes a deleted resource immediately unavailable while idempotent purge work removes its data from derived and blob stores. It is not a restorable soft-delete feature.
_Avoid_: Archive, recycle bin

## Evaluation

**Eval Case**:
A versioned research question with its allowed evidence, expected evidence or answer rubric, and scoring metadata, used to compare retrieval and Agent configurations through production interfaces.
_Avoid_: Unit test, manually selected demo

**Eval Run**:
An offline execution of a fixed Eval Case set against fully identified Source, retrieval, model, prompt, and Agent configurations, producing quality, latency, token, and cost measurements.
_Avoid_: Online experiment, Agent Run

## Observability

**Agent Observability SDK**:
The reusable Go instrumentation boundary that describes Agent execution through a small recording API, Agent semantic conventions, and replaceable delivery destinations. It produces Operational Telemetry or Durable Agent Trace records without owning an application's workflow or domain decisions.
_Avoid_: Agent framework, audit platform, Durable Agent Trace

**Operational Telemetry**:
Sampleable and retention-bounded traces, metrics, and logs used to diagnose the health, latency, and resource behavior of requests and background execution across system components.
_Avoid_: Agent state, product audit record

**Durable Agent Trace**:
The retained internal execution record with exactly one Trace and one root Trace Span per Agent Run, following that Run's lifecycle and reconstructing every started execution attempt independently of Operational Telemetry sampling or expiry, including work with no observed completion or accepted Checkpoint. It is not directly user-visible and is distinct from a future administrative projection, the user-facing Reasoning Trace, and any claim of access to hidden model cognition.
_Avoid_: OpenTelemetry span set, admin dashboard, user-facing Reasoning Trace

**Trace Span**:
A duration-bearing node in a Durable Agent Trace that represents one execution operation, has at most one parent, and may remain without an observed terminal outcome after process loss.
_Avoid_: Mutable Job state, Checkpoint, log line

**Trace Event**:
An immutable instantaneous fact attached to a Durable Agent Trace or Trace Span, such as Checkpoint acceptance, cancellation, or Lease loss.
_Avoid_: Trace Span, mutable status row, log message

**Trace Link**:
A typed causal reference between Trace Spans or Durable Agent Traces that does not change their parent-child ownership and may cross Trace boundaries.
_Avoid_: Parent Span, nested Span, foreign-key ownership

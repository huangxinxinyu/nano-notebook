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
A durable unit of asynchronous work whose state survives process failure and can be claimed, retried, cancelled, or completed by a Worker.
_Avoid_: Goroutine, task when durability matters

**Job Lease**:
An expiring claim that permits one Worker attempt to advance a Job while heartbeats continue. Lease expiry enables recovery and does not imply that the prior attempt produced no side effects.
_Avoid_: Lock, exactly-once execution

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

**Operational Telemetry**:
Sampleable and retention-bounded traces, metrics, and logs used to diagnose the health, latency, and resource behavior of requests and background execution across system components.
_Avoid_: Agent state, product audit record

**Durable Agent Trace**:
The retained internal execution record required to reconstruct an Agent Run's observable stages and actions independently of Operational Telemetry sampling or expiry. It is distinct from both the user-facing Reasoning Trace and any claim of access to hidden model cognition.
_Avoid_: OpenTelemetry span set, user-facing Reasoning Trace

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

**Source Discovery Session**:
A private durable record of one Member's bounded Web search in one Notebook. It owns normalized candidates, selection, import outcomes, and a safe summary, but no Source content or Evidence.
_Avoid_: Search response, shared Source list, RAG context

**Web Search Provider**:
The server-side adapter boundary that accepts a bounded query and locale hints and returns Provider-neutral discovery candidates. Brave is the first adapter; its raw envelope and credential never cross the boundary.
_Avoid_: Evidence Search Action, Source content reader, browser search

**Chat Run**:
The Member-visible durable lifecycle of one requested Chat answer. It owns product status and the input/output Message relationship; one input Message may have later Chat Runs after explicit user retries.
_Avoid_: Agent Run, Agent Job

**Agent Run**:
One internal durable invocation of a pinned Agent Definition on behalf of a Chat Run, Studio Output, or parent Agent Run. It owns its Executor binding, Checkpoints, Trace, Agent Tree membership, and delegation relationships but not product publication state; many Agent Runs may invoke the same Definition.
_Avoid_: Chat Run, Agent Definition, Agent Job

**Studio Output**:
A shared Notebook product record for one Report, Flashcards deck, Mind Map, or Data Table. It owns Member-visible lifecycle and the validated artifact, references exactly one configured root Agent Run, and is readable by current Notebook Members while remaining separate from every Member's private Chat.
_Avoid_: Agent Result, Chat Run, Note, generic artifact

**Agent Tree**:
The durable runtime aggregate shared by one root Agent Run and every delegated child, owning the absolute deadline plus reserved and consumed logical budgets independently of the owning Chat Run. The Chat Run references only the root; Agent Delegations define child membership.
_Avoid_: Chat Run, workflow DAG, Agent Configuration Set

**Leader Run**:
The root Agent Run owned by a Chat Run. It durably routes a Chat turn into ordinary conversation or explicit Source Discovery delegation, while the owning Chat Run remains the Member-facing lifecycle and Message publication authority.
_Avoid_: HTTP router, hidden child Run, general orchestrator

**Leader Route Decision**:
The typed model classification of the current Member turn as `continue_chat` or requested `delegate_research`, with a bounded intent reason. Recent completed conversation may resolve references, but only the current turn can explicitly request new Source Discovery; the decision grants no authority by itself.
_Avoid_: Tool permission, inherited search mode, model reasoning text

**Delegation Policy**:
The deterministic application gate that converts a requested Leader Route Decision into an effective route after checking Member authority, the pinned Definition and Executor relationship, active-child limits, Provider availability, and root Run validity. Requested and effective routes remain distinguishable in durable state.
_Avoid_: Router prompt, model authorization, silent policy inference

**Research child Run**:
A child Agent Run linked to a Leader through one durable Agent Delegation. It may create one private Discovery Session through the bounded Web Search Provider and cannot publish Chat content, import Sources, or delegate another Run.
_Avoid_: Research answer, prompt mode, crawler

**Legacy Agent Role**:
The Sprint 9 `leader` or `research` classification stored on existing Agent Runs and interpreted only by the migration compatibility adapter. New Agent Definitions select a registered Agent Executor directly, so Role is not a separate runtime identity or authorization layer.
_Avoid_: Agent Executor, new Definition field, extensibility mechanism

**Agent Definition**:
An immutable, Git-owned declarative configuration that gives one callable Agent identity its registered Executor, exact Model Policy, Prompt and Contract bindings, Tool and child allowlists, and local Run Budget. It can select and narrow registered capabilities but cannot encode control flow, lifecycle transitions, arbitrary code, or new authority; a model may select an allowlisted definition but cannot create or modify one.
_Avoid_: Agent Run, Agent Executor, user prompt, runtime plugin

**Agent Executor**:
A Go implementation registered under one stable Executor identity that owns a bounded execution strategy, deterministic state transitions, capability ceiling, and permitted child Executor classes. Sprint 10 supports one implementation per identity; Agent Definitions select it and may narrow its bindings but cannot inject functions, workflow expressions, SQL, network endpoints, retry policy, or publication authority.
_Avoid_: Agent Definition, workflow DSL, configuration script

**Agent Catalog**:
The embedded, Git-owned collection of strict versioned Agent Definition files for every production Agent. Startup rejects unknown fields, executable configuration constructs, or unresolved Executor, Model Policy, Prompt, Tool, child, and Contract references, canonicalizes each definition, hashes it, and registers it immutably before admission may pin it.
_Avoid_: Go constructor defaults, mutable configuration service, feature-specific registry

**Model Policy**:
An immutable, Git-owned and versioned configuration that binds an exact Provider model route and non-secret invocation parameters such as temperature, maximum output tokens, and timeout. Agent Definitions reference an exact Model Policy version, and admission pins its identity, hash, and resolved Provider model; deployment environment variables may supply endpoints and credentials but cannot replace production Agent model behavior.
_Avoid_: Model environment override, mutable alias, Provider credential

**Agent Delegation MCP Tool**:
One model-facing MCP Tool generated from an allowlisted Agent Definition, through which a parent Agent requests a bounded child task and receives a durable handle through the negotiated `io.modelcontextprotocol/tasks` Extension. Each visible Tool identifies exactly one child Definition and Result Contract; it grants no authority itself because application policy and the Delegation Kernel validate and execute the request.
_Avoid_: Direct Run insert, MCP Sampling, child Agent server

**Agent Definition Reference**:
The exact `identity@version` reference used by release manifests, parent child-allowlists, and Agent Runs. A callable child Tool name is derived deterministically as `delegate.<identity>.v<version>`; configuration supplies only bounded delegation description and Contract metadata, never a second arbitrary Tool identity.
_Avoid_: Latest Agent alias, model-supplied child ID, manually duplicated Tool name

**Child Context Manifest**:
The immutable, server-constructed input authority for one child Agent Run, combining its bounded parent task with authorized product, evidence, configuration, tool, budget, deadline, and result-contract references. It contains no inherited model reasoning or implicit copy of the parent's complete context.
_Avoid_: Shared conversation, parent prompt dump, child memory

**Agent Tree Budget**:
The immutable aggregate deadline and logical model, Action, context, and result limits shared by one root Agent Run and its descendants. Each Agent Run also has a local Agent Definition budget and may consume only the smaller remaining allowance; Provider-reported monetary cost is observed rather than treated as exact authorization state.
_Avoid_: Agent local budget, Provider quota, billing limit

**Delegation Kernel**:
The bounded Agent runtime capability that owns parent-child Run creation, waiting, continuation, cancellation propagation, and terminal handoff independently of any child Executor's domain work. It executes only registered and configured relationships and is not a general workflow graph.
_Avoid_: Research executor, workflow engine, agent-to-agent chat

**Agent Delegation**:
The durable lifecycle relationship between one parent Agent Run and one configured child Agent Run. Its generic record preserves relationship identity, ordinal, terminal status, safe failure, and parent consumption separately from any Executor-owned outcome such as a Discovery Session.
_Avoid_: Research query plan, child result payload, workflow edge

**Delegation Outcome**:
The durable terminal handoff from one child Agent Run to its parent, with a bounded status and an Executor-owned result or safe error reference. The Delegation Kernel records and delivers the outcome, while the parent Executor decides whether to continue, degrade, or fail; consuming the outcome does not erase its terminal status, and the current Research policy maps child failure to Leader failure.
_Avoid_: Assistant answer, kernel business decision, implicit fallback

**Agent Result**:
The single immutable, Contract-versioned canonical JSON payload produced by one successful child Agent Run, stored once with its SHA-256 and byte size. Delegation Outcome, parent Action Result Checkpoint, and Trace carry only its stable reference and integrity metadata; authorized Context Builders or publication code resolve the payload when needed.
_Avoid_: Copied child transcript, parent-owned draft, Tool Invocation ledger

**Delegation Scheduling Receipt**:
The Controller-internal result of a successful generated MCP `tools/call`, containing the durable Delegation identity and accepted state. It proves only that Nano atomically scheduled the child and suspended the parent; it is not an Agent Result, is not appended to model context, and is not represented as an MCP Task.
_Avoid_: Child result, model-visible “started” output, private MCP Task wire format

**Run Retry**:
An explicit user request to answer the latest unanswered input Message again after its prior Chat Run was cancelled or failed. It creates a new Chat Run and root Agent Run, is unavailable after the Chat advances, and is distinct from automatic execution Attempts inside an existing Agent Run.
_Avoid_: Job retry, Attempt, reopening a terminal Run

**Run Cancellation**:
The durable product decision that an active Chat Run and its Agent Tree will publish no answer. It may become final before in-flight work actually stops, is never resumed from Checkpoints, and a later Retry creates a new Chat Run and root Agent Run.
_Avoid_: Pause, process kill, guaranteed Provider cancellation

**Agent Job**:
The single internal durable delivery record that tells an Agent Worker which Agent Run to advance across its model and Action steps. It remains one Job across Checkpoints and infrastructure Attempts, and the browser never depends on its state.
_Avoid_: Agent Run, frontend status

**Attempt Disposition**:
The typed execution-host decision produced when one leased Agent Attempt stops advancing: `completed`, `waiting`, `retryable`, `terminal`, or `abandoned`. An abandoned Attempt has a bounded cause such as `lease_lost` or `cancelled` and can commit no further effect. Retryable work is deliberately requeued with bounded backoff under the same Run, Configuration Set, deadline, and Checkpoints; Lease expiry remains only the recovery fallback when disposition cannot be committed.
_Avoid_: Raw executor error, implicit Lease timeout, user Run Retry

**Job Lease**:
An expiring claim that permits one Worker attempt to advance a Job while heartbeats continue. Lease expiry enables recovery and does not imply that the prior attempt produced no side effects.
_Avoid_: Lock, exactly-once execution

**Lease Token**:
The identity of the current leased execution of a Job. Reclaiming the Job replaces the token so stale Workers can no longer heartbeat, fail, or publish for it.
_Avoid_: Session token, Worker identity, permanent ownership

**Run Checkpoint**:
An immutable, Provider-neutral durable boundary after an Agent outcome is accepted, from which a later Attempt can reuse accepted results and continue with the first incomplete step. Its stable identity envelope is shared by Agent Executors, while each Executor owns the typed payload schema for its bounded steps. It contains no transient running state, raw Provider payload, or diagnostic history and is not a snapshot of a Worker process or an in-flight model generation.
_Avoid_: Mutable step status, process snapshot, partial-token continuation, Durable Agent Trace

**Agent Context Projection**:
The deterministic, Provider-neutral context rebuilt for each Model Call from the pinned Agent Definition and Prompt plus the current Chat's single linear causal history across Agent Runs. It projects each User Message and every accepted Run Checkpoint in order, representing a completed Action round as its Assistant proposal followed by every matching Action Result; an Agent Context Compaction may summarize an older prefix while retaining a token-bounded exact suffix.
_Avoid_: Agent memory service, Provider thread, session tree, active-branch projection, raw transcript, hidden reasoning

**Tool Result Aging**:
The deterministic Agent Context Projection layer between an inline Action Result and an Agent Context Compaction. Once an Agent Step is older than the retained recent Steps, its large Results are projected as a short shell naming the durable Result Checkpoint reference, while the Tool call and its input stay exact; the boundary advances in whole batches of Steps so the Provider prefix cache breaks once per batch. A Research Run may read an aged Result back through `read_tool_result`; no model call, summary, or Checkpoint rewrite is involved. Pinned by Definition version (`research.executor@44`, `chat.leader@8`); from `research.executor@45` readers keep 4 recent Steps and age in batches of 4 instead of rewriting earlier pages.
_Avoid_: Compaction, summary, truncation of the Checkpoint, history deletion

**Stable Request Prefix**:
The rule that the leading parts of a model request, the tool list and the system prompt, contain only text fixed for the whole Agent Run, because a Provider prefix cache is reused only up to the first byte that differs from an earlier request. Per-decision state, such as budget, Source import, duplicate-recovery, pause, and final-only notices, the Evidence Ledger, and Agent Status, is injected after the trajectory as user messages; a tool paused by repeated calls stays listed and is refused when it executes. Earlier trajectory messages change only in whole batches (Tool Result Aging) or at a Compaction. Research follows it from `research.executor@45`.
_Avoid_: Mid-run system-prompt edits, removing a tool for one decision, rewriting an earlier Tool Result on every decision

**Agent Context Compaction**:
An append-only, rolling summary boundary that replaces an older cross-Run Chat context prefix only inside later Agent Context Projections while the original Messages and Run Checkpoints remain durable authority. Work appended after one Compaction remains exact until the growing request crosses its pinned trigger again; a successor then summarizes the newly old prefix and preserves a new recent token-bounded suffix. A Compaction never separates an Agent Step.
_Avoid_: History deletion, Checkpoint rewrite, message-count truncation, memory snapshot

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

**Evidence Coverage**:
The source-native regions that a Normalized Source Artifact successfully represents, together with any precisely bounded regions it omits. Unknown coverage or loss of the Source's primary content prevents publication, while bounded gaps remain visible on an otherwise usable ready Source.
_Avoid_: Extraction log, success boolean, silent omission

**Source Processing Budget**:
The versioned server-side limits on one Source's format expansion and processing resources, including structural count, decoded size, media duration, pixels, time, memory, temporary storage, and external model calls. Exceeding a hard limit fails processing rather than publishing truncated evidence.
_Avoid_: User quota, silent truncation, Worker capacity

**Evidence Revision**:
An immutable version of a Source's Normalized Source Artifact and its Evidence Unit address space. A legacy precise Citation pins an Evidence Revision so later extraction, OCR, or transcription improvements cannot change the evidence originally cited.
_Avoid_: Source version, index version

**Evidence Unit**:
A stable, source-native addressable span within a Normalized Source Artifact, such as a page text range, slide element, transcript interval, or image region. Legacy precise Citations resolve to Evidence Units or ranges of them, never to retrieval-index records.
_Avoid_: Chunk, vector point when discussing citation identity

**Source Reference**:
An ordered, Source-level Citation declared by an inline `[source:<source_id>]` marker in a plain-text Final Draft. The Source must have returned structurally valid Evidence in an accepted search result for that Run, but the reference does not promise a page, Unit, excerpt, or rune range.
_Avoid_: Claim citation, exact quote, Evidence range

**Retrieval Chunk**:
A rebuildable, possibly overlapping evidence window formed from one or more Evidence Units for candidate retrieval. Its boundaries and identifiers may change when retrieval policy changes without changing Citation identity.
_Avoid_: Citation span, authoritative content

**Retrieval Index Version**:
An immutable, rebuildable retrieval projection of one or more Evidence Revisions under a specific chunking, dense, and sparse indexing configuration. It can be replaced or removed without changing Citation identity.
_Avoid_: Evidence Revision, authoritative Source

**Embedding Input Profile**:
The versioned, deterministic formatting contract that maps a Retrieval query or Source title plus Retrieval Chunk into Provider input. Query and document forms may be asymmetric, but both are pinned by the Retrieval Index Version rather than inferred from a mutable Provider default.
_Avoid_: Hidden prompt prefix, unversioned task type, Agent prompt

**Retrieval Index Promotion**:
The authoritative switch that makes one fully built and verified Retrieval Index Version active only after its identified offline Eval Run satisfies the approved policy. Building or evaluating an index does not promote it by itself.
_Avoid_: Reindex completion, deployment, automatic tuning

**BM25 Retrieval Channel**:
The versioned lexical candidate path that uses the same language-aware analyzer at indexing and query time to rank exact terms through classic BM25. In the first release, `sparse` means this channel rather than a learned sparse embedding model.
_Avoid_: Learned sparse retrieval, semantic retrieval, full-text filtering

**Evidence Search Action**:
The typed, read-only Agent Action through which the Research Agent submits a bounded query and purpose under Retrieval Scope and receives authoritative Evidence candidates. It may be invoked iteratively within the Run Budget and never exposes vector-store records as evidence authority.
_Avoid_: One-shot RAG prompt, Qdrant query tool, web search

**Retrieval Degradation**:
An explicit retrieval outcome in which a versioned fallback policy permits useful candidates after one configured candidate or ranking stage fails. It is neither successful execution of the full hybrid pipeline nor evidence that the selected Sources lack support, and it never relaxes Retrieval Scope or groundedness rules.
_Avoid_: Silent fallback, successful hybrid retrieval, insufficient evidence

## Agent Runtime

**Prompt Catalog**:
The application-owned collection of immutable, explicitly versioned instructions used by production Model Calls and model-assisted Source processing. Each use resolves one exact Prompt identity rather than depending on mutable or ad hoc instruction text.
_Avoid_: Inline system prompt, mutable prompt alias, remote prompt console

**Prompt Version**:
An immutable identified revision of one Prompt Catalog instruction and its model-facing contract. Once admitted for an Agent Run or Source-processing operation, it remains fixed across execution attempts and continuations.
_Avoid_: Current prompt, mutable template, deployment default

**Prompt Contract**:
The application-owned typed input or output shape paired with a Prompt Version and enforced independently of instruction wording. It bounds model communication without granting execution authority or accepting free-form reasoning as control state.
_Avoid_: Prompt formatting convention, model suggestion, chain of thought

**Agent Prompt Set**:
The legacy Sprint 9 compatibility set that bound Prompt Versions and Contracts for Leader and Research Profiles. Sprint 10 reads it only for already admitted Runs; new Agent Definitions bind exact Prompt and Contract versions directly.
_Avoid_: Agent Definition, global prompt bundle, new configuration layer

**Agent Configuration Set**:
The immutable deployment manifest that selects exact versioned Agent Definitions and Model Policies eligible for newly admitted Chat or Studio root Runs. It no longer owns duplicate Role profiles or Prompt/Tool/model mappings; each Agent Run pins exact referenced definitions, and changing an environment variable never changes an admitted Run.
_Avoid_: Agent Definition, current server config, mutable feature flags, latest configuration

**Agent Configuration Lifecycle**:
The bounded `expand -> activate -> drain -> retire` deployment protocol for immutable Agent Configuration Sets and their Definition/Executor contracts. A release adds support before admission activates it and may retire old support only after durable state proves that no non-terminal Run still references it; unsupported active configuration prevents Worker readiness rather than failing the Run.
_Avoid_: Latest-only deployment, runtime hot switch, capability-aware scheduler

**Agent Role Profile**:
The legacy Sprint 9 configuration record that grouped model, Prompt, Tool, budget, and Executor bindings by Role inside an Agent Configuration Set. Sprint 10 reads it only for already admitted Runs through a compatibility adapter; new admissions use Agent Definitions from the Agent Catalog.
_Avoid_: Agent Definition, new configuration extension point, runtime plugin

**Run Evidence Set**:
The fixed set of immutable Sources and their active Evidence Revisions selected when a question creates an Agent Run. The Run also pins the corresponding Retrieval Index Version; later Chat selection, Source processing, and new Sources do not enter it, while deletion of a member Source invalidates the active Run rather than silently changing its evidence.
_Avoid_: Current Sources, live Notebook contents

**Grounding Outcome**:
The Run-owned classification of a newly published answer as `source_less`, `source_free`, or `source_cited`. Selected-Source Runs always attempt Evidence search; valid allowlisted Source markers determine `source_cited`, while their absence determines `source_free`. Historical outcomes remain readable but are not produced by new Runs.
_Avoid_: Message answer mode, mixed answer, UI toggle

**Agent Controller**:
The Go component that advances an Agent Run through its Executor-owned outer stages while validating, authorizing, budgeting, checkpointing, and recovering model-selected MCP Tool actions.
_Avoid_: Workflow engine, autonomous agent loop

**Agent Action**:
A durable logical invocation of one allowlisted MCP Tool, accepted and authorized by the Agent Controller from a model proposal. Each Action has canonical typed input and result independent of Provider tool-call formats; synchronous tools complete inline, while delegation tools atomically schedule a durable child Run and suspend the parent behind a Delegation.
_Avoid_: Raw Provider Tool Call, arbitrary command, uncheckpointed MCP request

**MCP Tool Plane**:
The internal Host/Server boundary through which every production model-callable tool is discovered and invoked. The Nano Tools MCP Server exposes startup-registered application adapters plus generated Agent Delegation tools, while the Agent Controller owns allowlisting, authorization, budget, Checkpoints, recovery, and task suspension; it is not a second Agent runtime.
_Avoid_: External Tool marketplace, direct executor shortcut, second orchestration runtime

**Attempt Context Handle**:
A short-lived, opaque, process-local identifier that the MCP Host injects into request metadata outside model-visible Tool arguments. It binds one leased Agent Attempt to its Agent Run, fencing authority, pinned Agent Definition, scoped Tool allowlist, product authorization, deadline, and remaining budget; the Nano Tools MCP Server validates it for both discovery and invocation and rejects it after the Attempt loses authority. Recovery mints a new Handle rather than persisting or reviving the old one.
_Avoid_: Tool argument, Run ID supplied by the model, long-lived bearer token

**Logical Action Identity**:
The stable `action_id` assigned by an accepted Action Proposal and injected by the Host into MCP request metadata outside model-visible arguments. It survives infrastructure Attempts and is the idempotency key for state-changing Tool adapters, while the ephemeral Attempt Context Handle proves that the current caller may advance it.
_Avoid_: Provider tool-call ID, Attempt Context Handle, random retry ID

**Tool Registry**:
The application-owned registry backing the Nano Tools MCP Server's `tools/list` and `tools/call` behavior. Ordinary tools register typed MCP adapters at startup and Agent Definitions generate delegation entries during controlled deployment; runtime plugins, model-defined tools, and unscoped external discovery are prohibited.
_Avoid_: Legacy Action Registry, plugin manager, dynamic Tool marketplace

**Materialized Tool Definition**:
The immutable name, description, input/output schema, Contract identities, and SHA-256 returned by scoped MCP discovery for one Agent Run and Model Call. A later `tools/call` must resolve the same definition and hash or fail closed.
_Avoid_: Global mutable Tool schema, unversioned JSON object, Provider-only function definition

**Tool Scheduling Class**:
The code-registered execution constraint attached to an MCP Tool adapter: `ordered_sync` may share a bounded proposal batch and executes sequentially, while `exclusive_delegation` must be the only Action because it suspends the parent Agent Run. Agent Definitions may lower batch limits but cannot change a Tool's class.
_Avoid_: Model-selected concurrency, configurable Tool semantics, implicit fan-out

**Action Proposal**:
A Provider-independent, ordered model request to invoke one or more exposed MCP Tools. It is input to Agent Controller validation and becomes an Agent Action only after schema, capability, authorization, and budget checks; the proposal itself grants no execution authority.
_Avoid_: Authorized Tool Call, command, approved Action

**Model Decision**:
The Provider-neutral result presented to the Agent Controller by one completed model invocation. It contains exactly one Final Draft or one ordered Action Proposal batch.
_Avoid_: Raw Provider response, Chat completion, chain of thought

**Agent Step**:
One Model Decision plus the complete Action batch it requests and every corresponding Action Result; a Final-only decision is a Step with no Actions. It is the indivisible unit of context continuation and compaction, not an Agent Run or infrastructure Attempt.
_Avoid_: Agent Run, Model Call, partial Tool round

**Steering Message**:
A Member User Message addressed to a Run that is still queued or running. It admits no Run; the Controller delivers it at the Run's next decision boundary by appending a `steering_message` checkpoint before the decision it precedes, so the model sees it exactly once at a durable position. It is excluded from the Chat lane's turns and spends no Agent budget.
_Avoid_: Follow-up message, new turn, interrupt, mid-run prompt edit

**Decision Contract**:
A versioned schema that constrains a model's structured decision without invoking an executable capability or producing an Action Result. A Provider may encode it with function-call syntax, but the Models Module normalizes it as decision data rather than an MCP Tool; `select_leader_route` is the Sprint 10 example.
_Avoid_: Agent Tool, MCP Tool, Provider function-call syntax

**Model Call**:
One Agent Controller invocation of the Models Module, recorded with application-normalized metadata even when the gateway performs multiple Provider attempts internally. It excludes raw gateway or Provider request and response payloads.
_Avoid_: Provider request, Bifrost response, Agent Run

**Action Result**:
The accepted, normalized typed outcome of one Agent Action, containing either success data or an expected domain error from the MCP Tool Plane. It is durable Run working state consumed by later model decisions and reused after recovery, rather than raw transport output.
_Avoid_: Raw MCP response, log entry, Trace Event

**Tool Error Classification**:
The Agent Controller's exhaustive mapping of a Tool outcome into `domain_error`, retryable infrastructure failure, lost-authority abandonment, or safe terminal invariant failure. Only bounded error codes declared by the Materialized Tool Definition become model-visible Action Results; Transport, authorization, Lease, Definition, and schema-integrity details never enter model context, and a Tool adapter cannot choose Agent Run or product status.
_Avoid_: Raw MCP error passthrough, Tool-owned retry, model-visible authorization failure

**Tool Invocation Semantics**:
MCP Tool execution is at-least-once across infrastructure recovery: an Action whose Result Checkpoint is missing may be invoked again under a new Agent Attempt and Attempt Context Handle, but retains its Logical Action Identity. Read-only or pure Tools must tolerate repetition, state-changing Tools must enforce database-level idempotency by `action_id`, and only the currently fenced Attempt may append the accepted Action Result.
_Avoid_: Exactly-once RPC, per-Attempt idempotency, generic Tool invocation ledger

**Final Draft**:
An accepted model-produced plain-text candidate answer that may become an Assistant Message only through Source-marker normalization and the Publication Barrier.
_Avoid_: Assistant Message, published answer, raw model response

**Run Budget**:
The Definition-local limits pinned to one Agent Run for model decisions, accepted logical Agent Actions, context, result size, Attempts, and elapsed time. Success and expected domain error consume one Action each, while recovery re-execution does not consume another. A child also shares its Agent Tree deadline and aggregate logical limits and may use only the smaller remaining allowance; delegation never resets or pauses elapsed time.
_Avoid_: Provider quota, Job retry policy, context window

**Fixed Agent Loop**:
The bounded orchestration seam introduced in Sprint 2A and now advanced by the Agent Controller through Definition-limited model decisions and MCP Tool Actions until a terminal Executor-owned outcome. It is not an autonomous loop or general workflow engine.
_Avoid_: Autonomous loop, generic workflow engine

**Context Builder**:
The component that constructs the bounded input for the next Model Call from the pinned Agent Definition and Prompt, authorized product input, accepted Run Checkpoints and Action Results, and selected Evidence Units. Its output is a model-facing projection, not durable authority or a claim to capture model-internal memory.
_Avoid_: Transcript replay, memory store, model snapshot

**Publication Barrier**:
The final transactional authorization, Source-availability, and Source-reference-validity check that alone may turn an Agent draft into a durable Assistant Message. It revalidates execution authority and provenance, not textual claim support. Late or incomplete work cannot bypass it.
_Avoid_: Stream completion, model success

**Retrieval Scope**:
The server-constructed intersection of an authorized Notebook and a Run Evidence Set that every retrieval channel must enforce before returning candidates.
_Avoid_: Client filter, vector-database tenant

## External Input

**Web Reader Adapter**:
A least-privileged outbound network boundary that reads one approved public HTML URL under strict protocol, destination, redirect, size, time, and concurrency policy and returns bounded cleaned Markdown without access to product databases or durable credentials.
_Avoid_: Raw HTML archive, generic crawler, web-search tool

## Data Lifecycle

**Deletion Tombstone**:
A minimal non-content authority record that makes a deleted resource immediately unavailable while idempotent purge work removes its data from derived and blob stores. It is not a restorable soft-delete feature.
_Avoid_: Archive, recycle bin

## Evaluation

**Eval Case**:
A human-authored, versioned research question with fixed non-sensitive Source fixtures, allowed and expected Evidence or equivalent Evidence sets, an answer rubric, and scoring metadata. Model-generated judgments may supplement its results but cannot redefine its ground truth or authorize promotion alone.
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
The retained internal execution record with exactly one Trace and one root Trace Span per Agent Run, following that Run's lifecycle and reconstructing every started execution attempt independently of Operational Telemetry sampling or expiry, including work with no observed completion or accepted Checkpoint. It is restricted developer data, remains distinct from its administrative projection, and never contains or claims access to model chain of thought.
_Avoid_: OpenTelemetry span set, admin dashboard, Member-facing trace, chain of thought

**Agent Trace Processor**:
The application-owned observability component that validates and converges Durable Agent Trace records into retained authority and query projections without owning Agent execution or generic Operational Telemetry.
_Avoid_: OpenTelemetry Collector, Agent Worker, generic log consumer

**Replay**:
An encrypted, allow-listed, retention-bounded record of normalized content used to debug one observed Agent operation. It is loaded only through an explicit audited `platform.trace.replay` request, contains no raw Provider envelope or chain of thought, and never grants browsing access to the parent Source.
_Avoid_: Full Source archive, raw Provider log, unbounded prompt history

**Trace Span**:
A duration-bearing node in a Durable Agent Trace that represents one execution operation, has at most one parent, and may remain without an observed terminal outcome after process loss.
_Avoid_: Mutable Job state, Checkpoint, log line

**Trace Event**:
An immutable instantaneous fact attached to a Durable Agent Trace or Trace Span, such as Checkpoint acceptance, cancellation, or Lease loss.
_Avoid_: Trace Span, mutable status row, log message

**Trace Link**:
A typed causal reference between Trace Spans or Durable Agent Traces that does not change their parent-child ownership and may cross Trace boundaries.
_Avoid_: Parent Span, nested Span, foreign-key ownership

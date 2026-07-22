# Nano Notebook Technical Architecture

## 1. Purpose And Scope

Nano Notebook is a small, single-region SaaS research workspace implemented with a Go core. The first release targets roughly 100 registered users and about 10 concurrent Agent or Source-processing jobs. Its engineering depth is concentrated on durable Agent execution, evidence-grounded retrieval, authorization isolation, observability, and evaluation rather than hyperscale infrastructure.

The current milestone is a complete local product. Production launch is a later delivery stage with AWS as the intended direction, S3 as the production Blob Store, and static Web Client delivery through S3 and CloudFront. Exact AWS compute, networking, backup, OIDC, email, and secret-management services are not prerequisites for local completion.

The first release does not include an Agent Sandbox, arbitrary code execution, browser or computer use, general external tools, cloud-drive synchronization, Notes, search discovery, or generated Outputs. Search and the committed Output families remain later scope and will be scheduled after the initial dependency graph is complete.

## 2. System Shape

```mermaid
flowchart LR
    Browser[React + TypeScript SPA]
    CP[Go Control Plane]
    PG[(PostgreSQL)]
    Blob[(MinIO local / S3 production)]
    Q[(Qdrant)]
    Jobs[PostgreSQL Durable Jobs]
    SourceW[Source Workers]
    AgentW[Agent Workers]
    EvalW[Eval Workers]
    Fetcher[Restricted Fetcher Adapter]
    Extractor[Restricted Extractor Adapters]
    Bifrost[Bifrost Model Gateway]
    Providers[OpenAI / Gemini / Qwen]
    OTel[OpenTelemetry Collector]

    Browser -->|REST commands and queries| CP
    CP -->|SSE projections| Browser
    Browser -->|presigned Source upload| Blob
    CP --> PG
    CP --> Jobs
    Jobs --> SourceW
    Jobs --> AgentW
    Jobs --> EvalW
    SourceW --> Fetcher
    SourceW --> Extractor
    SourceW --> Blob
    SourceW --> Q
    AgentW --> Q
    AgentW --> PG
    AgentW --> Blob
    EvalW --> Q
    SourceW --> Bifrost
    AgentW --> Bifrost
    EvalW --> Bifrost
    Bifrost --> Providers
    CP -. telemetry .-> OTel
    SourceW -. telemetry .-> OTel
    AgentW -. telemetry .-> OTel
    EvalW -. telemetry .-> OTel
```

The Go application is a modular monolith with independently deployable Worker processes. Modules communicate through in-process interfaces and share PostgreSQL without sharing ownership of one another's tables. A Module becomes a network service only after operational evidence justifies extraction.

## 3. Authoritative Stores

| Store | Responsibility | Explicitly not responsible for |
| --- | --- | --- |
| PostgreSQL | Users, Sessions, Notebook membership, Source metadata, Chat, Agent Run, Job, Citation, version and publication state | Large Source bodies, vector similarity, operational telemetry |
| MinIO/S3 | Original Source blobs, normalized artifacts, large derived artifacts and selected Trace payloads | Authorization, workflow state, queryable business truth |
| Qdrant | Rebuildable dense and sparse retrieval projections | Authoritative text, permissions, Source or Citation lifecycle |
| Bifrost | Provider protocol translation and bounded call-level routing/fallback | Agent workflow state, provider selection policy ownership, durable product state |
| OpenTelemetry backend | Sampleable operational traces, metrics, and logs | Durable Agent Trace or product audit truth |

Every non-PostgreSQL record must be reachable from authoritative metadata and either rebuildable or removable through a Module-owned lifecycle.

## 4. Go Module Ownership

| Module | Owns | Depends on |
| --- | --- | --- |
| Identity | User identity, Local Credentials, opaque Sessions, later OIDC mapping | PostgreSQL |
| Notebook | Notebooks, ownership, Membership, invitations, role-derived Capabilities | Identity |
| Source | Source lifecycle, raw Blob references, Evidence Revisions, Evidence Units, and Evidence Coverage | Notebook, Jobs, Models |
| Chat | Private Chats, source-selection preference, User and published Assistant Messages | Identity, Notebook, Source |
| Agent | Agent Runs, fixed stages, accepted read-only actions, publication, Durable Agent Trace responsibility | Chat, Source, Retrieval, Jobs, Models |
| Retrieval | Retrieval Index Versions, chunk projections, scoped hybrid retrieval and result validation | Source, Notebook, Qdrant |
| Jobs | Durable Job admission, leases, attempts, retries, cancellation and Workload Classes | PostgreSQL |
| Models | Capability-level provider policy and Bifrost invocation | Bifrost |

Notebook membership never grants access to another Member's private Chat. Chat owns the private conversation; Source owns citable content; Agent and Retrieval hold references rather than alternate copies of authority.

## 5. Authentication And Authorization

Local development uses email and password Local Credentials and PostgreSQL-backed opaque application Sessions. Internal User identity is credential-neutral. Public launch is blocked on managed, provider-neutral OIDC and disabling Local Credential entry points.

Authorization uses two layers:

1. Go Capability policies map the authenticated Principal, Notebook role, resource ownership, and operation to a decision.
2. PostgreSQL row-level security prevents cross-Notebook and cross-creator reads even when application queries are defective.

Workers reauthorize before continuing durable work. Privileged maintenance and migration roles are separate from request and Worker roles. Clients never supply trusted Qdrant filters or authoritative ownership identifiers.

Sprint 6 is an Owner-only delivery slice matching the current membership schema and UI. It does not add invitations, Viewer or Editor behavior, ownership transfer, Member management, or a shared-Notebook library; those remain a separate delivery slice without changing the initial product commitment to Viewer, Editor, and Owner roles. Source and Retrieval handlers must nevertheless authorize through Capability policies and row-level security rather than embedding an Owner-only assumption inside either module. This seam, together with server-constructed Retrieval filters, preserves the final role model and cross-Notebook, cross-user, and cross-Chat isolation without exposing nonfunctional sharing controls in Sprint 6.

## 6. Source Ingestion And Evidence

### 6.1 Upload And Import

The Control Plane creates short-lived upload intents. A multi-file browser interaction obtains and finalizes an independent intent for each admitted file, so every file becomes its own Source and a rejection or processing failure never rolls back unrelated accepted Sources. Notebook capacity is rechecked during each finalize transaction. Browser uploads go directly to MinIO/S3, and the Source Module finalizes only validated objects. A content hash rejects a byte-identical upload only within the same Notebook and returns that Notebook's existing Source; hashes never expose or reuse Source identity across Notebook boundaries. Public URLs are fetched by a restricted Fetcher Adapter with HTTP(S)-only policy, public-destination validation for IPv4 and IPv6, redirect revalidation, size and decompression limits, timeouts, and no product database or durable credential access. Each accepted URL fetch creates a new immutable Source snapshot even when its content matches an earlier snapshot.

Document and media parsing runs behind least-privileged, format-specific Extractor Adapters. The first release owns a lightweight, Citation-oriented extraction pipeline rather than deploying or depending on a general-purpose document parsing platform. An Adapter may use a Go library, a restricted external program, or an external OCR, vision, or transcription model, but it must return Nano's Normalized Source Artifact contract instead of exposing a provider-native document-conversion format. The authoritative Source Worker owns Job state, validates the output, and publishes artifacts; Extractors cannot publish product state or access PostgreSQL and Qdrant.

### 6.2 Durable Pipeline

Each Source advances through a fixed durable pipeline:

```text
uploaded -> validating -> normalizing -> segmenting -> indexing -> verifying -> ready
                                                                  \-> failed
```

Stages checkpoint completed work and artifact references. OCR, transcription, parsing, and embedding are not repeated merely because a later stage failed. Adapter-internal bounded parallelism does not become a generic DAG.

Admission and every Adapter enforce a versioned Source Processing Budget in addition to the product byte and word limits. The budget bounds format-specific expansion and work such as decoded bytes, page or slide count, embedded objects, redirects and DOM nodes, media duration, pixels and frames, wall time, CPU, memory, temporary storage, and external model calls. Admission rejects limits knowable from metadata; later discovery safely fails processing with a typed reason. No stage silently truncates a Source and publishes it as complete. Concrete values are frozen by Sprint 6 capacity tests rather than the overall architecture.

Normalization also produces explicit Evidence Coverage. Verification may publish a ready Source with precisely bounded gaps when the remaining evidence is usable, but it must preserve those gaps for product inspection. Unknown coverage or loss of primary content fails the Source. Retrieval indexes only published Evidence Units and never represents a gap as evidence.

Document normalization prefers native structure over full-page visual inference. PDF, DOCX, and PPTX Adapters first extract text, tables, content-bearing images, and source coordinates; only non-decorative visual regions and pages without a usable text layer enter OCR or vision processing. Standalone image Sources enter vision processing as a whole. Skipped and failed visual regions remain explicit Evidence Coverage gaps.

### 6.3 Evidence Identity And Index Versions

An immutable Source may have multiple Evidence Revisions when extraction, OCR, or transcription changes. Each revision contains stable Evidence Units aligned to source-native structural blocks: page regions for PDF, slide elements for PPTX, document or HTML blocks, transcript intervals, and image regions. Oversized blocks split only by versioned source-relative character, row, cell, or region boundaries rather than model tokenization. Historical precise Citations pin one Unit or a continuous Unit range; new conversational Source references intentionally identify only the Source.

Retrieval Chunks are deterministic, structure-aware, overlapping windows over one or more Evidence Units and never become Citation identities. Chunking preserves structural boundaries where practical, keeps tables and visual evidence intact, never crosses a Source or Evidence Revision, and splits an oversized Unit only through versioned source-relative boundaries. Qdrant points retain the covered Unit references, so authoritative evidence can be reloaded after dense, sparse, fusion, and reranking stages. Changing chunk size, overlap, or algorithm creates a new Retrieval Index Version without changing historical Citation targets; offline evaluation selects the concrete parameters rather than the architecture fixing them.

Chunking or embedding changes create a Retrieval Index Version without changing the Evidence Revision. Workers fully build and verify new artifacts and Qdrant points, then an identified offline Eval Run must satisfy the approved promotion policy before PostgreSQL publishes the active version. Agent Runs pin their Evidence Revisions and Retrieval Index Version so processing changes cannot alter evidence mid-run.

## 7. Retrieval Architecture

All active Sources in one embedding space share Qdrant Collections. Indexed Payload fields include Notebook, Source, Evidence Revision, and index-version identity. The Retrieval Module constructs every filter from the authorized Notebook and the fixed Run Evidence Set; selected Sources use a server-built match-any condition. PostgreSQL revalidates returned Evidence Unit identities before authoritative content is loaded.

The first-release sparse path is classic BM25, not a learned sparse embedding model. Its Chinese, English, and mixed-language analyzer, tokenizer, dictionaries, and BM25 parameters are immutable Retrieval Index Version inputs, and indexing and query processing use the same configuration.

The first-release retrieval pipeline is:

```text
typed Agent query
-> dense retrieval under Retrieval Scope
-> sparse retrieval under the same Retrieval Scope
-> RRF rank fusion
-> authoritative evidence load
-> bounded reranking
-> Evidence candidates returned to Agent Controller
```

The Research Agent reaches this pipeline only through the typed `search_evidence` Evidence Search Action. A Model Decision may propose multiple purposeful queries, inspect accepted candidate results, and refine later queries within the existing Run Budget. Each accepted Action Result contains Provider-neutral authoritative Evidence references and bounded previews rather than Qdrant records; Checkpoints prevent recovery from repeating accepted searches. Query purpose, inspected candidates, ranking and selection outcomes, and Retrieval Degradation feed restricted Durable Agent Trace records and audited Replay rather than Member-facing Chat data; model chain of thought is never requested or captured.

Dense-only retrieval is an intermediate implementation milestone, not the completed product path. Embedding and reranker models, analyzers, chunking, candidate limits, fusion weights, and stopping thresholds are selected through offline evaluation rather than fixed by overall architecture. Learned sparse models remain deferred unless evaluation demonstrates a stable BM25 recall gap that justifies another model dependency.

The promoted configuration normally reranks every fused candidate set. A transient reranker failure receives bounded retry and may then return the unchanged RRF order under an explicit, traced degradation outcome; it never fabricates reranker scores or relaxes Retrieval Scope. Evidence returned through a degraded search remains eligible for Source markers; an answer without a valid marker remains `source_free`.

Dense and BM25 normally execute for every query. After bounded retry, one failed candidate channel may explicitly degrade to the other only when the surviving channel completed and satisfies the versioned minimum-candidate policy. If the surviving channel also fails or cannot meet that minimum, Retrieval fails rather than claiming insufficient evidence. Only completed configured channels with no useful candidates may contribute to an insufficient-evidence conclusion. Every degradation identifies the missing stage in Durable Agent Trace and the developer Dashboard.

Gemini converts visual and structured inputs into region-linked normalized textual evidence. Documents, HTML, transcripts, OCR, and visual descriptions enter one text Retrieval Channel in the first release. Original media and Citation coordinates remain available, but native visual-vector retrieval and cross-channel fusion are deferred.

## 8. Durable Agent Runtime

The bounded PostgreSQL Durable Runtime supports fixed product Job types, not arbitrary workflows. The Agent Controller advances a Run through fixed outer stages such as planning, research, retrieval, inspection, comparison, refinement, verification, composition, and terminal states. The model may propose only typed, read-only research actions; Go validates scope, budgets, stage preconditions, and permissions.

Sprint 2A establishes the first production-shaped slice with one fixed pass: `LoadRun -> BuildContext -> InvokeModel -> PublishAnswer`. A request transaction commits the User Message, product-facing Agent Run, and internal Agent Job atomically. The independent Worker claims the Job from PostgreSQL, builds one in-memory request from the system prompt and latest 20 durable Messages, calls Bifrost without provider token streaming, and publishes the complete Assistant Message through one transaction. This is intentionally a fixed Agent Loop with no tool-call iteration, Retrieval, MCP, checkpoint, or generic workflow abstraction.

Sprint 2A Jobs have only `queued`, `running`, and terminal delivery state. PostgreSQL `LISTEN/NOTIFY` reduces wake-up latency and a five-second indexed scan recovers lost notifications; the Job row is always queue truth. Attempts, leases, heartbeats, fencing, process-loss recovery, and safe re-execution are Sprint 2B work and must be added before claiming at-least-once recovery semantics.

The completed durable runtime will use at-least-once leases, attempts, heartbeats, and idempotent effect boundaries. Fixed interactive Agent, Source Processing, and offline Eval/Reindex Workload Classes reserve capacity so background work cannot starve user-facing Runs.

Sprint 3 retains exactly one Agent Job for one Agent Run across all model decisions, Actions, Checkpoints, and infrastructure Attempts. A Worker claim advances as many incomplete nodes as its current authority and Attempt timeout allow; reclaim resumes the same Job from accepted Checkpoints. Model and Action nodes do not become child Jobs or a Job chain.

Sprint 2B keeps the current Agent Job lease generation on the `agent_jobs` row instead of creating a durable Attempt history. Claiming queued or expired work increments `attempt_no`, replaces a random `lease_token`, and sets `lease_expires_at` in one PostgreSQL transaction. A Worker conditionally extends that deadline with periodic heartbeats using PostgreSQL time. Heartbeat, failure, and publication writes must present the current unexpired Lease Token; reclaiming the Job fences every older execution. Full per-attempt history belongs to the Sprint 4 Durable Agent Trace rather than this recovery state.

The Sprint 2B defaults are a 30-second lease and a heartbeat every 10 seconds. Each successful heartbeat sets `lease_expires_at` to PostgreSQL current time plus 30 seconds rather than adding time to the prior deadline. These values are server configuration, not a product API contract. A zero-row conditional heartbeat means the attempt has lost authority and must cancel its local execution; a database error never counts as a successful renewal, and final publication independently revalidates the current unexpired Lease Token.

Sprint 2B does not add Run Checkpoints to the source-less Fixed Agent Loop. Recovery after an expired lease reconstructs context from durable Chat state and re-executes the single model invocation; a Provider call interrupted in flight cannot resume from a partial generation. If the Worker loses a completed model result before the existing Publication Barrier commits it, recovery may repeat that model call, while fencing still permits only the current lease to publish one Assistant Message.

Automatic Job recovery is limited to an expired lease on a still-active, non-cancelled Run, which means the runtime lost the executor without receiving a terminal outcome. An explicit terminal Bifrost result such as model timeout, unavailability after gateway retries, or invalid response fails the Run and Job without another Agent Job attempt. This prevents Job recovery from multiplying the gateway's bounded Provider retry budget; a user retry creates a new Run.

An Agent Job permits at most three total execution attempts, including its initial claim. The first two expired leases may be reclaimed; expiry of the third attempt atomically fails the active Run and Job with the safe code `recovery_exhausted` instead of starting a fourth model call. This bounds crash-loop cost and duration while leaving product retry as a new Run.

Lease expiry and reclaim do not add a product-visible `recovering` or `retrying` Agent Run state. The Run remains `running` across infrastructure attempts; only completion, explicit failure, terminal cancellation, or exhausted recovery changes its user-visible state. Attempt number and lease transitions remain internal Job Runtime data and are absent from SSE and Chat projections.

Reclaim is a single PostgreSQL claim transaction, not a two-step transition through `queued`. The indexed claim query selects either an ordinary queued Job or an expired running Job with fewer than three attempts using `FOR UPDATE SKIP LOCKED`; an expired selection remains `running` while its attempt number increments and its Lease Token and deadline are replaced. An expired third attempt is terminalized as `recovery_exhausted` instead of being reclaimed.

On graceful Worker shutdown, the process stops claiming new Jobs, cancels its active model-call context, and conditionally sets the current lease deadline to PostgreSQL current time using its Job ID and Lease Token. It then notifies the Job channel so another Worker can reclaim without waiting 30 seconds. If PostgreSQL is unavailable during shutdown, stopping heartbeats falls back to natural lease expiry; the Job never moves back through `queued`.

Run Checkpoints become required in Sprint 3 before the Agent Controller introduces multi-step model/Action iteration. The runtime persists a checkpoint only after the Agent Controller accepts an Action Proposal, a completed Action Result, or a Final Draft. Loading, context construction, and validation remain recomputable; recovery reuses the ordered accepted outcomes and continues with the first incomplete step. This checkpoint state is runtime authority rather than a Worker process snapshot or the Sprint 4 Durable Agent Trace.

Accepted outcomes form an append-only Run Checkpoint sequence. Proposal, per-Action Result, and Final Draft identities are unique within a Run so uncertain writes can be reconciled idempotently; each append validates the current unexpired Lease Token and Run deadline. The runtime stores no mutable checkpoint `running` state, Attempt history, or diagnostic transition stream.

Checkpoint payloads have a canonical hash. After an uncertain insert outcome, the Worker reloads the stable logical identity: a matching hash confirms success, absence under the same valid authority permits retry, and a different hash is an invariant violation that cannot be overwritten. Lost authority stops the write; continued PostgreSQL uncertainty ends lease renewal and yields recovery to a later Attempt.

Checkpoint payloads are Provider-neutral and limited to recovery: Controller-generated Action identity and order, canonical typed inputs, typed success or safe domain-error results, and Final Draft content. Context Builder reconstructs model input from Chat, pinned Run configuration, and these accepted outcomes. Raw Provider payloads and identifiers, token usage, and diagnostic data are deferred to Sprint 4.

Checkpoints remain internal and attached to their Agent Run after any terminal outcome. Sprint 3 exposes no product API or UI for them and adds no independent TTL; they follow the Run's authorized parent deletion and purge lifecycle. Sprint 4 defines separate Durable Trace retention and payload purge policy.

Each model invocation returns one Provider-neutral Model Decision containing exactly one Final Draft or one ordered Action Proposal batch. Models and the Bifrost adapter own Provider tool-call schemas and message conversion. The Agent Controller owns Proposal validation, stable Action identity, acceptance, and execution; both or neither decision variants fail as `model_invalid_response`.

Sprint 3 proves this boundary end to end with the current real `aliyun/qwen-flash` configuration through Bifrost. Automated adapter contracts cover final, single-Action, ordered multi-Action, and invalid responses. Additional real Provider acceptance is deferred; Provider-neutral domain types must not imply multi-Provider verification that the Sprint did not perform.

A Provider response becomes accepted Run state only when its normalized Proposal or Final Draft Checkpoint commits. If authority is lost before that boundary, recovery may invoke the model again and receive a different decision, so actual Provider calls may exceed accepted model-decision budget. Run deadline and Provider retry limits bound this at-least-once window; the runtime does not claim exactly-once model invocation.

Sprint 3 does not persist Action catalog or per-Action versions and does not maintain multiple executor versions. The built-in `calculate` and `current_time` contracts remain stable across the Sprint; an incompatible future change must drain or terminalize active Runs before deployment or introduce versioning in the owning later design.

The Worker owns an internal extensible Action Registry. Each built-in Action registers one canonical name, description, input schema, typed decoder/validator, and executor at startup; duplicate names fail startup, registration is immutable after startup, and discovery order is deterministic. Agent Controller obtains the Run-allowed Action definitions and resolves accepted names only through this Registry, so adding a later built-in Action does not add Controller branches. This is code-level extension, not a reusable Action SDK, runtime plugin system, MCP discovery surface, or user-installed Tool registry.

A model decision may contain an ordered batch of Agent Actions. The Agent Controller checkpoints the accepted batch once, executes its Actions sequentially, and checkpoints each Action Result in proposal order. After recovery it skips accepted results and resumes at the first incomplete Action; concurrent Action execution within a batch is deferred.

An expected Action domain error is an accepted typed Action Result: it is checkpointed and returned to the model so a later decision can repair the request or explain the limitation. Unknown Actions and malformed Provider responses fail as model protocol errors. Cancellation, lease loss, and infrastructure faults retain their runtime semantics and are never converted into Action Results.

Every Sprint 3 Run pins separate limits for model decisions, total Actions, Actions per ordered batch, elapsed time, and retained Action Result size. Recovery derives consumed decision and Action capacity from accepted Checkpoints and retains the original Run deadline. When an Action-capable limit is exhausted, the Controller removes Action definitions and permits one reserved final model decision to answer from accepted results or explain the limitation; it never grants more Action capacity.

The Sprint 3 configurable defaults are four Action-capable model decisions, one reserved Action-disabled final decision, eight accepted logical Actions per Run, and four Actions per ordered batch. The four-location current-time and time-difference journey is the delivery scenario for these bounds. They apply only when a Run is admitted and may change later based on Eval and observability evidence.

Admission pins an absolute Run deadline in the same transaction as the User Message, Run, and Job, with a configurable Sprint 3 default of ten minutes from commit. Queue delay, Worker outage, lease recovery, and later attempts consume the same deadline. Expiry terminalizes an otherwise-active Run as `run_deadline_exceeded` and releases the active-Run slot; the existing per-attempt timeout remains nested beneath and cannot extend the Run deadline.

An idempotent `ExpireIfOverdue` command uses PostgreSQL time to atomically fail an overdue active Run and its Job. Worker queue scans call it before claim, admission and Retry call it before active-slot enforcement, and Run SSE calls it for the initial snapshot and heartbeat cycle. No dedicated deadline sweeper is added; concurrent expiry, cancellation, and publication serialize through authoritative Run and Job locks.

Total Action capacity counts accepted logical Actions, including Actions that produce expected domain-error results. Recovery may re-execute an accepted Action whose result is incomplete without charging it again. A proposal rejected before acceptance consumes no Action capacity, and Provider retries do not enter the Action count.

Run Checkpoints recover only infrastructure interruption within the same active Run. User Stop remains terminal cancellation: remaining Actions and publication are forbidden, and the Run is never resumed. Retry creates a new Run with newly pinned configuration, time zone, budgets, and deadline and reuses no prior Checkpoint.

Sprint 3 retains heartbeat-based cancellation propagation without a dedicated cancellation listener. The Agent Controller revalidates active Run state, current Lease Token, and Run deadline before model or Action execution and before every Proposal, Action Result, Final Draft, and publication commit. The heartbeat cancels an in-flight model context after Stop or lost authority, while durable boundary checks and the Publication Barrier remain authoritative.

Sprint 3 admission pins an IANA time zone on each Agent Run from the user's declared browser time zone, with UTC as the safe fallback when it is absent or invalid. The built-in `current_time` Agent Action uses that pinned zone by default and may accept an explicit valid IANA zone for another location; Worker host time zones never affect the result. The accepted observed time and zone are retained in the Action Result Checkpoint and reused after recovery.

Run Checkpoint representation, Run Working State schema, retry values beyond the three-attempt Agent Job policy, and context-manifest format beyond the Sprint 2B single-call recovery slice are deferred to the multi-step Agent Runtime detailed-design grill. Overall architecture requires that a Run survive process loss and that the future Context Builder can construct the next bounded model input from persistent Chat, accepted actions, Evidence, and versioned configuration.

No generic workflow SDK, arbitrary DAG, deterministic replay, exactly-once promise, multi-Agent runtime, or Agent Sandbox enters the first release. An external MQ is metrics-triggered evolution rather than scheduled scope. If PostgreSQL dispatch becomes a measured bottleneck in the intended AWS environment, SQS plus a transactional outbox is the preferred direction while PostgreSQL retains authoritative Job and Run state.

## 9. Cancellation And Publication

Stop, Source deletion, Membership removal, and Notebook deletion first persist cancellation or invalidation. Workers observe it at bounded transitions, reauthorize, and request cancellation of in-flight dependencies where supported. Expired Worker attempts cannot commit through lease fencing and state-version checks.

For a user Stop command, the PostgreSQL transaction is the product boundary. If cancellation commits before publication, the Agent Run becomes terminal `cancelled` immediately, releases the user's active-Run slot, and can never publish an Assistant Message; aborting an in-flight Provider request remains a best-effort resource optimization. If publication commits first, the completed answer is not retroactively cancelled. Recovery cleans up cancelled Jobs without re-executing their model call.

The same Stop transaction marks the queued or running Agent Job `cancelled`. Sprint 2B adds no separate cancellation listener: a running Worker discovers the lost authority when its next 10-second conditional heartbeat updates zero rows, then cancels its local model-call context. A model result that arrives before that detection remains provisional. Publication always locks and revalidates the Run and Job as `running`, the current unexpired Lease Token, authorization, and absence of cancellation, so a result arriving during this window is discarded rather than published.

A Publication transaction error is not itself a terminal Run failure because the Worker may have lost the commit acknowledgement after PostgreSQL committed. The current Worker first reloads authoritative state: `completed` means publication succeeded; `running` with the same unexpired Lease Token permits a bounded retry using the same in-memory normalized model result; `cancelled` or a lost lease discards the result. If PostgreSQL remains unavailable and the outcome cannot be reconciled, the Worker stops renewing and lets lease recovery inspect durable state. A new attempt can reclaim only a still-running Run, while uniqueness and the Publication Barrier prevent a duplicate Assistant Message.

The initial release does not stream draft answer tokens to Members. A draft remains internal and becomes a durable Assistant Message and Citations only through one transactional Publication Barrier that revalidates:

- the current Principal and private Chat ownership;
- Notebook membership and role;
- every Source and Evidence Revision in the Run Evidence Set;
- Source-reference resolution and grounding-plan validation;
- absence of cancellation or invalidation.

Before entering that transaction, every selected-Source Run must have a durable `search_evidence` attempt. The Composer always returns ordinary text and may place `[source:<source_id>]` after material it used. Grounding retains markers only for Sources represented by structurally valid Evidence in accepted search results, removes malformed or invented markers, and persists ordered Source references. It does not create claims, duplicate answer substrings, request provider JSON mode, or invoke a runtime support verifier.

The source-less path has no Citations or Run Evidence Set. A selected-Source `source_free` Run retains its pinned Evidence Set and search provenance but publishes no Source reference; a `source_cited` Run copies its allowlisted Source references atomically. Both paths revalidate private Chat ownership, current Notebook membership, execution authority, every pinned Source and Evidence Revision, and the normalized-answer digest before inserting exactly one Assistant Message. These checks fence authorization, deletion, cancellation, and lease races; they do not assert that a sentence is entailed by a Source. Failed Runs publish no Assistant Message.

Cancelled, failed, or invalidated work retains the User Message and Run status but never presents an incomplete response as a completed Grounded Answer. Product copy may say “stopped”; the canonical durable Agent Run state is `cancelled`.

## 10. Chat And Browser Interfaces

The Web Client is a React and TypeScript SPA built with Vite. It consumes JSON REST commands and queries from the Go Control Plane and uses SSE for asynchronous projections. The client first reads the latest durable snapshot; SSE is never authoritative state.

The browser stops an owned Run with an authenticated, CSRF-protected `POST /api/v1/agent-runs/{run_id}/cancel`. Cancelling queued or running work atomically returns the terminal `cancelled` Run snapshot; repeating the command for that Run returns the same successful snapshot without an additional idempotency key. A completed or failed Run returns `409 run_not_cancellable`, and inaccessible Runs remain safe `404`s. Per-Run SSE projects the committed `cancelled` snapshot and then closes; the Chat retains the User Message and offers retry.

Command replay, execution recovery, and product retry have different identities. Replaying the original Message command returns its existing Run; reclaiming an expired lease creates another execution attempt inside the same Run and Job; an explicit retry after `cancelled` or `failed` creates a new Run and Job for the same immutable input Message. A terminal Run is never reopened. The retry command is independently idempotent and freezes the then-current model and prompt configuration on the new Run.

Sprint 2B therefore removes the Sprint 2A global uniqueness of `agent_runs.input_message_id`. One input Message may own multiple historical cancelled or failed Runs, at most one queued or running Run, and at most one completed Run. Completed-answer regeneration, multiple successful answer variants, and branching are excluded and not planned scope.

The browser retries an owned cancelled or failed Run with `POST /api/v1/agent-runs/{run_id}/retry` and a required `Idempotency-Key`. One transaction locks and validates the prior Run and input Message, rejects an existing completed Run or any active Run for the user, creates a new queued Run with the then-current model and prompt configuration plus a new queued Agent Job, and notifies the Worker. Replaying the same key returns that same new Run. Runs are grouped and ordered by their shared input Message; Sprint 2B adds no `retry_of_run_id` lineage column.

Citation interaction uses one Source Viewer shell with format-specific adapters. New inline Source references open the ready Source for normal inspection without a quote tooltip or focused coordinate. Historical precise Citations continue to focus their addressed Evidence Unit or range and may show a bounded authoritative excerpt. Resolution never refreshes an external Source or grants original-file download.

Run Retry is available only while that input remains the Chat's latest unanswered User Message. If any later Message exists, the retry command returns `409 retry_not_latest`; old failed or cancelled Runs remain immutable history and cannot create an implicit branch. The Context Builder independently anchors its latest-20 query at the Run's input Message tuple rather than the Chat head, so later content can never enter a recovered or retried Run even under a race. Historical retry and conversation branching are not planned scope.

The Sprint 2B Chat snapshot replaces its single `active_run` field with a `runs` projection containing only the newest Agent Run for each User Message. Queued or running projections let the browser reconnect the one permitted active Run; the latest unanswered cancelled or failed projection restores Retry; older terminal projections explain unanswered historical Messages without offering Retry; completed projections correlate with their durable Assistant Message. The snapshot exposes no superseded Runs, Jobs, attempts, leases, or Trace data.

The selected browser UI baseline is React 19, TypeScript, Vite, Tailwind CSS 4, shadcn/ui New York-style primitives on Radix UI, TanStack Query, TanStack Table, React Hook Form, Zod, Sonner, and locally hosted Material Symbols. Sprint 2A mounts `@assistant-ui/react` through its external-store boundary: PostgreSQL-backed Messages and Run state remain server-owned, while Assistant UI supplies accessible thread and composer interaction. The browser assigns the User Message UUID, submits one command, and projects queued/running/terminal state from a per-Run EventSource.

Sprint 2A SSE sends complete current Run snapshots rather than provider token deltas. The Control Plane owns one shared PostgreSQL Run listener and fans wake-ups to in-process per-Run subscribers; each subscriber reloads authorized PostgreSQL state. Reconnect reads the latest snapshot, so this slice needs no durable event log, cursor, or `Last-Event-ID`. Those stronger replay contracts are added only when a later feature actually requires them.

Sprint 3 does not expose Agent Actions, Checkpoints, budgets, or infrastructure recovery through Run snapshots or SSE. The browser continues to observe only stable Run lifecycle states and the final published answer; Checkpoint appends create no client event contract. Durable Trace access and Action history UI remain Sprint 4 scope.

Sprint 3 removes the Sprint 2A single-call `iteration_count`, `finish_reason`, and prompt, completion, and total token columns from Agent Run. Decision count needed for recovery is derived from Checkpoints. Per-call finish reason, usage, latency, Provider retries, and accurate cost accounting belong to Sprint 4 Model Call records rather than an incomplete Run aggregate.

Each Chat belongs to one creator and remains private even from the Notebook Owner. A submitted User Message with no selected Sources creates a source-less model-knowledge Run. A Run with selected Sources pins a fixed Run Evidence Set, must attempt Evidence search, and then publishes either `source_free` plain text or `source_cited` plain text with allowlisted inline Source references; Source-selection changes apply only to later Runs. Only the Publication Barrier can add the corresponding Assistant Message. Failed or stopped Runs can be retried as new Runs without editing history.

## 11. Model Gateway And Provider Portfolio

Bifrost runs as a standalone local container with file configuration and environment-provided secrets. Nano Notebook owns routing intent and durable state; Bifrost performs protocol translation and bounded call-level retry or fallback. Retry budgets must not multiply across the Agent Runtime and gateway.

The initial capability portfolio is:

| Capability | Providers and boundary |
| --- | --- |
| Text generation | OpenAI, Gemini, and Qwen |
| Vision normalization | Gemini default, OpenAI second implementation |
| Embedding | Gemini Embedding 2 candidate at 768 dimensions; one model and input profile per Retrieval Index Version; either change requires reindex |
| Reranking | Qwen `qwen3-rerank` participates in evaluation |
| Transcription | One accepted OpenAI implementation initially because timestamp quality is part of Citation correctness |

Bifrost UI, persistent configuration database, semantic cache, and Agent features are excluded. The current candidate pins `gemini/gemini-embedding-2`, 768 dimensions, and asymmetric `gemini-retrieval-v1` query/document formatting; it becomes active only through the existing offline Eval promotion gate.

## 12. Observability And Evaluation

Operational Telemetry uses OpenTelemetry-compatible context across the Control Plane, Jobs, Workers, Bifrost, PostgreSQL, Qdrant, and Blob Store. It may be sampled and expired. The Agent Module separately owns the mandatory Durable Agent Trace; both are correlated by trace, Run, Job, and attempt identifiers.

Sprint 6 extends the restricted developer Trace Dashboard with RAG execution. Durable metadata available to `platform.trace.read` records Evidence Search Action purpose identifiers, candidate identities and rank positions, Dense and BM25 completion, RRF and reranker transitions, authoritative Evidence loading, selection outcomes, Retrieval Degradation, whether research was performed, eligible Source count, valid Source-reference count, discarded marker count, latency, token usage, and cost without Source or Chat bodies. Sensitive query text, Evidence excerpts, and normalized model content may appear only in encrypted, allow-listed Replay guarded by `platform.trace.replay`, audited on every attempt, and retained for seven days by default. Replay is limited to content used by that observed operation and never grants complete Source browsing. Neither Trace nor Replay requests, derives, or stores model chain of thought, and none of this data is projected into Member-facing Chat APIs.

Sprint 6 includes an offline Eval Harness that invokes the same Source, Retrieval, Models, Agent, and Citation module interfaces used by the product while running locally or in CI; it does not require a deployed production environment or a separate test-only RAG implementation. Human-authored, versioned Eval Cases use fixed non-sensitive Source fixtures and identify allowed Sources, expected Evidence or equivalent Evidence sets, answer rubrics, forbidden facts, and parsing-coverage expectations across every supported Source family. Model judges may supplement language and completeness scoring but cannot change golden truth or authorize promotion alone. Eval Runs record the full extractor, Evidence Revision, Retrieval Index Version, Composer model, prompt, and Agent configuration and measure parsing coverage, retrieval quality, Source-reference precision, answer quality, forbidden facts, latency, tokens, and cost. Promotion policy has three layers: authorization, deletion, Source-reference identity, and Publication Barrier invariants permit zero failures; designated critical cases must each pass; remaining quality, latency, and cost metrics use aggregate thresholds frozen before candidate configurations are compared. An Eval Run satisfying all three layers is required before Retrieval Index Promotion. Execution may use CLI and Workers; a management UI, online A/B system, automatic ground-truth generation, and automatic tuning platform are excluded.

## 13. Deletion And Retention

Destructive commands synchronously remove product visibility and future use in PostgreSQL, cancel affected Runs, and create idempotent Module-owned purge work. A minimal non-content Deletion Tombstone may coordinate cleanup but is not a restorable soft-delete feature.

Purge Jobs remove Qdrant projections, Blob Store objects, derived artifacts, private Chat content, and relevant Trace payloads according to ownership and references. Cleanup failure cannot make content visible again. Existing messages keep their Citation marker after Source deletion, but resolution reports the Source unavailable and reveals no deleted passage.

Production launch must define backup expiry and each Provider's retention and deletion guarantees. The product's immediate access revocation does not wait for backup or Provider cleanup.

## 14. Verification Strategy

Verification is layered around real ownership boundaries:

- unit tests cover Capability policy, fixed state transitions, Context Builder selection, filter construction, Citation validation, and pure adapters;
- integration tests use real PostgreSQL with RLS, MinIO, Qdrant, and Bifrost with controlled upstreams;
- provider contract suites use sanitized fixtures plus opt-in live smoke tests;
- a real-S3 compatibility suite is a production-launch gate;
- local end-to-end tests exercise the four product acceptance journeys in a browser;
- fault injection covers Worker termination, expired leases, duplicate attempts, Provider timeout, Qdrant unavailability, Source deletion, Membership removal, and Publication Barrier races;
- authorization tests attempt cross-Notebook, cross-role, and cross-creator access at both Go policy and RLS layers;
- Fetcher tests cover private and reserved address ranges, DNS rebinding defenses, redirects, decompression limits, and unsupported protocols.

Mock-only green tests are insufficient for local product completion.

## 15. Local Development And Production Gates

Docker Compose runs PostgreSQL, MinIO, Qdrant, Bifrost, and local observability dependencies. Go Control Plane, Go Workers, and React/Vite run natively by default for fast debugging; Extractors remain containerized for isolation. Repository-owned commands provide bootstrap, migration, seed, start, test, and cleanup flows. CI may use a profile that also containerizes application processes. Kubernetes is not part of local development.

Before production launch, the architecture requires at least:

- managed OIDC enabled and Local Credential entry points disabled;
- AWS S3 compatibility verified and backup/deletion policy defined;
- production secret management, email delivery, network egress, and Extractor isolation accepted;
- Provider retention and privacy behavior documented;
- operational telemetry backend and alerts configured;
- production compute topology selected from measured local and pre-production behavior.

## 16. Deferred Detailed Design

The following are intentionally unresolved until the owning subsystem is implemented:

- future multi-step Run Working State, Checkpoint, Ledger, cancellation boundaries, and full Trace schemas;
- chunking, embeddings, sparse representation, retrieval limits, fusion weights, reranking, and evaluation thresholds;
- exact parser libraries, OCR prompts, transcription timestamp normalization, and Citation coordinate schemas;
- Go HTTP framework, SQL access layer, migrations, repository package layout, and frontend component choices beyond the selected browser UI baseline;
- session expiry values, invitation email implementation, retry/timeout/concurrency values not fixed by an owning Sprint, and retention durations;
- production AWS compute, OIDC provider, email provider, secret store, observability backend, and optional future MQ.

These decisions must preserve the authority, isolation, and data-flow boundaries in this document.

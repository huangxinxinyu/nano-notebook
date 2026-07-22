# Nano Notebook Sprint 6 PRD

## Document Status

- **Sprint:** Sprint 6
- **Status:** Approved for implementation
- **Date:** 2026-07-20
- **Theme:** Multiformat Sources, versioned evidence, hybrid retrieval, grounded answers, and Citations
- **Delivery boundary:** One Owner can add and inspect supported Sources, research a fixed Source selection through the existing durable Agent, and receive an atomically published verified Answer with inline Citations. Sprint 6 also delivers the developer RAG Trace extension and offline evaluation gate. Sharing roles and invitations remain a later delivery slice.

**2026-07-22 amendment:** ADR 0038 supersedes the claim-generation, exact Evidence-address, and runtime Claim Support Verifier requirements below for new Runs. Selected-Source Runs now always attempt Evidence search and publish plain text with optional allowlisted Source-level references. Historical precise Citations remain readable.

## 1. Decision

Sprint 6 delivers Nano Notebook's first complete Source-grounded research path:

1. ingest every confirmed initial Source format except pasted text;
2. normalize each immutable Source into stable, citable Evidence Units;
3. build deterministic Retrieval Chunks and a versioned dense plus BM25 projection in Qdrant;
4. retrieve through Dense, BM25, RRF, and reranking behind one typed `search_evidence` Agent Action;
5. compose and independently verify claim-to-evidence support;
6. publish one complete Answer and its Citations through the existing Publication Barrier;
7. resolve each Citation in a format-aware Source Viewer;
8. expose RAG execution only in the restricted developer Dashboard and audited Replay;
9. select and promote parsing and retrieval configurations through an offline Eval Harness.

Nano owns the Source, Evidence, Retrieval, Agent, Citation, and evaluation contracts. It may call external OCR, vision, transcription, embedding, reranking, and language models through narrow adapters, but it does not depend on MinerU or another general-purpose document-parsing service.

## 2. Source Documents

This PRD derives from:

- `docs/product-discovery/CONTEXT.md`
- `docs/product-discovery/REQUIREMENTS.md`
- `docs/product-discovery/TECHNICAL-HANDOFF.md`
- `docs/technical-architecture/CONTEXT.md`
- `docs/technical-architecture/ARCHITECTURE.md`
- ADRs 0005, 0006, 0008, 0009, 0013, 0014, and 0017 through 0028
- ADRs 0030 through 0035
- `docs/sprint/SPRINT-3-PRD.md`
- `docs/sprint/SPRINT-5-PRD.md`

If this PRD conflicts with an approved product or architecture decision, the approved source wins unless this PRD explicitly records a superseding decision.

## 3. Sprint Goal

The user journey is:

```text
add one URL or select one or more local files
  -> each item becomes an independent immutable Source
  -> validate and normalize through a format-specific Extractor Adapter
  -> publish Evidence Revision and Evidence Coverage
  -> deterministically chunk, embed, and index Dense + BM25
  -> verify and mark Source Ready
  -> select ready Sources in a private Chat
  -> Agent iteratively calls search_evidence
  -> Dense + BM25 -> RRF -> authoritative load -> rerank
  -> compose claim/evidence mapping
  -> verify every material claim
  -> Publication Barrier atomically publishes Answer + Citations
  -> Citation opens the immutable Source snapshot at its Evidence Unit
```

No draft answer token, retrieval stage, candidate list, verifier detail, or model reasoning is shown to the Member. The browser shows a basic running state and Stop, followed by the complete verified result or a safe terminal failure.

## 4. Success Criteria

Sprint 6 is complete only when:

1. PDF, TXT, Markdown, DOCX, PPTX, public HTML, public YouTube with captions, MP3, WAV, M4A, PNG, JPEG, and WebP can each complete the Source lifecycle under accepted fixtures.
2. Multi-file selection creates independent Sources and reports each result without cross-item rollback.
3. A Source never becomes Ready after unknown extraction loss, silent truncation, incomplete index publication, or failure to prove its active evidence/index pairing.
4. A usable Source may become Ready with only precisely bounded Evidence Coverage gaps, which are prominent in its Viewer and excluded from retrieval.
5. Exact file duplicates are rejected only within the same Notebook; a repeated external URL creates a new immutable snapshot.
6. Originals and normalized artifacts remain in S3-compatible storage, authoritative metadata and evidence identity remain in PostgreSQL, and Qdrant remains a rebuildable projection.
7. Citations address stable Evidence Units or continuous Unit ranges, never Retrieval Chunks or Qdrant points.
8. Chunking is deterministic, structure-aware, versioned, and reproducible from an Evidence Revision.
9. Every normal search executes Dense and classic BM25 under the identical server-built Retrieval Scope, fuses with RRF, reloads authoritative Evidence, and reranks.
10. Permitted retrieval degradation is explicit, bounded, traced, and never treated as proof of zero Source support.
11. The Agent can issue and refine multiple purposeful `search_evidence` Actions within its fixed Run Budget, with accepted results checkpointed for recovery.
12. Every material factual or synthesized claim in a Grounded Answer has valid supporting Citations and an accepted Claim Support Record before publication.
13. Unsupported claims are researched again, rewritten, removed, or replaced with an explicit insufficient-evidence statement.
14. Partial Source support never mixes hidden model knowledge into a Grounded Answer.
15. Complete, non-degraded zero-support research may publish only a fresh, fully disclosed Model-Knowledge Answer with no Source Evidence or Citations.
16. Source deletion, authorization loss, cancellation, incomplete research, or degraded retrieval cannot trigger model-knowledge fallback or publish a partial result.
17. A Citation hover shows a bounded authoritative excerpt and selection opens the correct type-specific Source location without refetching mutable external content.
18. A ready Source can be opened directly from the Sources list in the same Viewer, including all Evidence Coverage warnings and no original-file download.
19. Member APIs expose only Source lifecycle, selection, basic Run state, final Answer, Citations, Viewer content, and safe failures.
20. The restricted developer Dashboard exposes RAG execution metadata; sensitive normalized query/evidence/model/verifier content requires explicit audited Replay and expires after seven days by default.
21. The offline Eval Harness exercises the same product interfaces, passes every invariant and critical case, and promotes only configurations satisfying pre-frozen quality, latency, and cost gates.
22. Cross-Notebook, cross-user, cross-Chat, unselected-Source, deleted-Source, and client-supplied-filter attacks return no evidence.
23. Source and interactive Agent capacity tests satisfy the target of about 100 registered users and 10 concurrent Agent/Source jobs without background evaluation starving interactive work.
24. Sprint 1 through Sprint 5 authentication, Chat, durable recovery, checkpoints, cancellation, publication, Trace collection, Replay, and Dashboard behavior remains green.

## 5. Sprint Scope

### 5.1 Source Inputs

| Family | Accepted input | Authoritative viewer basis |
| --- | --- | --- |
| PDF | uploaded `.pdf` | immutable PDF page and source-native region |
| Plain text | uploaded `.txt`, `.md` | normalized text range |
| Office | uploaded `.docx`, `.pptx` | normalized document block or rendered slide region |
| Web | public HTTP(S) HTML or direct supported document URL | immutable normalized page snapshot |
| YouTube | public video URL with usable captions | immutable imported transcript interval |
| Audio | uploaded `.mp3`, `.wav`, `.m4a` | immutable transcript interval and time range |
| Image | uploaded `.png`, `.jpg`/`.jpeg`, `.webp` | immutable original image region |

Pasted text, authenticated or paywalled pages, recursive crawling, embedded-media import, cloud drives, source discovery, automatic refresh, and arbitrary video ingestion are excluded.

### 5.2 Owner-Only Delivery Slice

The current schema and UI support only the Notebook Owner. Sprint 6 does not implement Viewer, Editor, invitations, ownership transfer, Member management, leaving a Notebook, or a shared library.

This is a delivery boundary, not a replacement product model. Source and Retrieval modules authorize through Capability interfaces and PostgreSQL RLS; they do not hardcode Owner checks. Server-built Retrieval Scope and private-Chat ownership remain mandatory so the later sharing slice does not require rewriting the RAG core.

### 5.3 Member-Facing Surface

Sprint 6 adds:

- multi-file local selection and one-at-a-time URL addition;
- per-Source `Processing`, `Ready`, and `Failed` state;
- failure reason, Retry, Rename, and permanently destructive Remove;
- Source selection per Chat, defaulting a new Chat to all then-ready Sources;
- direct ready-Source inspection and Evidence Coverage warnings;
- basic Agent running state and Stop;
- complete Answer with inline Citations;
- Citation preview and Source Viewer navigation;
- safe insufficient-evidence, fallback, deletion, and processing messages.

It adds no Source overview, suggested questions, user-facing RAG stages, candidate display, reasoning trace, chain of thought, answer token streaming, or original-file download.

## 6. Canonical Terms

Sprint 6 uses the definitions in the product and technical context documents. The implementation and UI must preserve these distinctions:

- Source is an immutable evidence snapshot; Evidence Revision versions its normalized evidence.
- Evidence Unit is stable Citation identity; Retrieval Chunk is a rebuildable search window.
- Retrieval Index Version pins chunking, analyzer, BM25, embedding, fusion, and reranking configuration.
- Run Evidence Set freezes the selected Source and Evidence Revision identities for one Run.
- Retrieval Scope is the server-built authorization intersection, never client input.
- Evidence Search Action is Agent research, not web search or a direct Qdrant tool.
- Retrieval Degradation is an explicit partial pipeline outcome, not successful hybrid search or insufficient evidence.
- Claim Support Record is a typed verification result, not hidden model reasoning.
- Grounding Outcome belongs to the Run and is not duplicated as Message `answer_mode`.

## 7. Ownership And Authority

### 7.1 Source Module

Owns Source identity, input metadata, immutable snapshot references, state transition, Evidence Revision publication, Evidence Coverage, Source title, duplicate policy, Viewer authorization, retry, deletion, and purge intent.

### 7.2 Extractor Adapters

Convert one admitted input into Nano's Normalized Source Artifact. They may use a library, restricted program, or external model API but own no product state, PostgreSQL credentials, Qdrant credentials, publication decision, or durable retry. Their scratch space is ephemeral and bounded.

### 7.3 Retrieval Module

Owns deterministic chunk construction, Retrieval Index Versions, Qdrant projection, Dense/BM25 execution, RRF, authoritative Evidence reload, reranking, degradation policy, result validation, and index promotion. It never becomes Source or Citation authority.

### 7.4 Agent Module

Owns Run Evidence Set, `search_evidence` Action use, accepted search Checkpoints, research iteration, composition, Claim Support Records, Grounding Outcome, final validation, and publication.

### 7.5 Models Module

Owns capability-specific adapters for text generation, vision, transcription, embedding, and reranking. Each call returns Provider-neutral typed results and normalized usage metadata. Raw Provider envelopes do not enter Source, Retrieval, or Agent authority.

### 7.6 Storage Authority

| Store | Authoritative content |
| --- | --- |
| Application PostgreSQL | Sources, revisions, units, coverage, versions, selections, Run evidence, claim support, Citations, lifecycle and deletion authority |
| S3-compatible Blob Store | immutable originals, normalized artifacts, rendered pages/slides, transcript and viewer artifacts |
| Qdrant | rebuildable dense and sparse Retrieval Chunk projection with scoped payload identifiers |
| Collector PostgreSQL/Object Store | diagnostic Trace metadata and encrypted retention-bounded Replay only |

Qdrant results are identifiers and scores. The Retrieval Module must reauthorize and reload Evidence from Source authority before returning an Agent Action Result.

## 8. Source Admission And Lifecycle

### 8.1 File Upload

One browser selection may contain multiple files. The Control Plane admits each item independently and creates a short-lived direct-upload intent. The browser uploads the bytes to Source object storage and finalizes each item independently. Finalization rechecks:

- authenticated Capability and Notebook ownership;
- remaining 50-Source capacity;
- supported extension, sniffed media type, and format consistency;
- declared and observed byte limit;
- object existence, size, and checksum;
- same-Notebook content hash duplication;
- idempotency identity.

An invalid item receives its own terminal admission result. It does not revoke or roll back unrelated accepted items. Abandoned intents expire and their objects are purged.

### 8.2 URL Admission

The Control Plane accepts one known public HTTP(S) URL. A restricted Fetcher Adapter validates public IPv4 and IPv6 destinations before connection and after every redirect, defends against DNS rebinding, limits redirects, compressed and expanded bytes, time, and content type, and has no product database or durable credential access.

A YouTube URL follows the caption-import adapter. Direct supported document URLs follow the corresponding document adapter. Other accepted responses follow the primary-HTML adapter. Every successful fetch is a new immutable Source even when bytes match an earlier URL snapshot.

### 8.3 State Machine

The durable internal pipeline is:

```text
uploaded -> validating -> normalizing -> segmenting -> indexing -> verifying -> ready
       \---------------------------------------------------------------> failed
```

The Member sees only `Processing`, `Ready`, or `Failed`. Each transition is fixed, lease-fenced, idempotent, and resumable from durable boundaries. Retry of a Failed Source starts new processing work without mutating an already published Evidence Revision. Processing one item never blocks use of other Ready Sources.

No stage silently truncates. A knowable hard-limit violation is rejected at admission; a later-discovered limit safely fails the Source with a typed reason.

## 9. Normalized Evidence Contract

Every Extractor Adapter returns a versioned Normalized Source Artifact containing:

- Source and extraction-configuration identity;
- ordered structural blocks and their source-native coordinates;
- text, tables, transcript segments, OCR text, and visual descriptions as applicable;
- embedded or rendered artifact references needed by the Viewer;
- stable source-relative boundaries for oversized-unit splitting;
- language and structural metadata required by analysis and retrieval;
- complete Evidence Coverage, including precisely bounded omissions and reason codes;
- checksums sufficient to verify deterministic publication.

Publication validates schema, bounds, ordering, coordinates, object references, UTF-8 safety, size, and coverage. Unknown coverage, loss of primary content, no usable evidence, or invalid coordinates fails the Source. Known non-primary gaps may publish Ready and remain visible in the Viewer.

### 9.1 Native Structure First

- PDF extracts native text, tables, images, page identity, and coordinates before OCR or vision.
- DOCX and PPTX extract OOXML structure, relationships, text, tables, media, and coordinates before visual processing.
- Only content-bearing visual regions and pages or slides without usable native text enter OCR or vision.
- Standalone images enter OCR and visual understanding as a whole.
- HTML retains primary content and stable normalized block order, not live page behavior.
- Audio and YouTube evidence uses immutable timestamped transcript segments.

Nano does not ship a universal parser platform. Concrete libraries, isolated binaries, prompts, and provider adapters are selected behind format contracts through fixture, license, safety, and capacity tests; changing them creates a new extraction configuration and Evidence Revision.

## 10. Evidence Identity And Chunking

Evidence Units follow source-native structure: PDF page regions, slide elements, document/HTML blocks, transcript intervals, text ranges, table regions, and image regions. A Citation addresses one Unit or a continuous Unit range inside an immutable Evidence Revision.

Retrieval Chunks are deterministic, structure-aware overlapping windows over one or more Evidence Units. They:

- never cross Source or Evidence Revision;
- preserve headings, paragraphs, tables, visual evidence, and transcript locality where practical;
- split oversized Units only by versioned source-relative character, row, cell, time, or region boundaries;
- carry the exact covered Evidence Unit references;
- are reproducible from normalized artifacts without a model call;
- never become Citation identity.

Chunk size, overlap, and structure policy are frozen inside a Retrieval Index Version. Offline evaluation selects the promoted parameters.

## 11. Index Build And Promotion

For one fully published Evidence Revision, the Retrieval Worker:

1. creates deterministic Retrieval Chunks;
2. analyzes each chunk with the versioned Chinese/English/mixed-language analyzer;
3. produces classic BM25 sparse representation;
4. produces dense embeddings through the configured Models capability;
5. writes Qdrant points containing only projection data and scoped identifiers;
6. verifies expected point count, payload filters, vector dimensions, sparse values, checksums, and authoritative unit references;
7. marks the Source index build complete without yet changing global active-version authority.

A Retrieval Index Version identifies the chunker, analyzer and dictionaries, BM25 parameters, embedding model/dimension, candidate bounds, RRF parameters, reranker, degradation policy, and minimum-candidate policy. A fully built candidate version becomes active only through Retrieval Index Promotion after an identified passing offline Eval Run. Promotion is an atomic PostgreSQL authority change; old versions remain available while pinned Runs require them and are later purged safely.

Learned sparse embeddings are excluded from Sprint 6.

## 12. Retrieval Pipeline

Every `search_evidence` call receives a typed query, purpose identifier, bounded candidate request, Run Evidence Set, and pinned Retrieval Index Version. The Retrieval Module constructs the Retrieval Scope from authorized server state.

Normal execution is:

```text
Dense candidates under Retrieval Scope
  + BM25 candidates under identical Retrieval Scope
  -> deterministic RRF
  -> PostgreSQL authorization and Evidence identity validation
  -> authoritative bounded Evidence preview load
  -> bounded reranker
  -> ordered Provider-neutral Evidence candidates
```

Indexing and query-time BM25 use the identical language-aware analyzer. RRF never compares incomparable Provider scores directly. The reranker operates only on the bounded fused set and cannot expand Retrieval Scope.

### 12.1 Degradation

- Dense, BM25, and reranker receive bounded retries.
- One candidate channel may degrade to the other only when the survivor completed and meets the versioned minimum-candidate policy.
- Reranker failure may return unchanged RRF order after bounded retry.
- Both candidate channels failing, or a survivor below minimum, fails retrieval.
- Only all configured channels completing with no useful candidate constitutes an empty search result.
- Any degraded search is traced and cannot establish eligibility for zero-support model-knowledge fallback.

No fallback relaxes authorization, Citation validation, Claim Support verification, or Publication Barrier checks.

## 13. Agent Research And Grounding

Sprint 6 registers one new read-only Agent Action: `search_evidence`. A Model Decision may propose multiple purposeful searches, and later decisions may refine queries after inspecting accepted candidates. Go validates query count, size, purpose identity, Retrieval Scope, Run Budget, and result bounds before execution and Checkpoint acceptance.

The Context Builder receives only bounded authoritative Evidence previews from accepted Action Results. It does not receive raw Qdrant points, unselected Sources, deleted evidence, hidden web context, or model chain of thought.

Composition returns a typed Final Draft plus claim/evidence mapping. Verification then performs:

1. deterministic Source, revision, unit, scope, deletion, range, and Citation-shape checks;
2. an independent model Claim Support verification using a separately versioned prompt and schema;
3. coverage validation ensuring every material factual or synthesized claim is supported;
4. bounded re-research or rewrite when a claim fails.

At budget exhaustion, an unsupported claim is removed or replaced by an explicit insufficient-evidence statement. It is never published as grounded.

The composer and verifier configurations are pinned separately. They may initially use the same base model but are distinct Model Calls and cannot share hidden state.

## 14. Grounding Outcomes And Publication

### 14.1 Source-Grounded

A partially or fully supported question publishes only claims supported by selected Sources. Gaps are disclosed. Model knowledge cannot silently fill them.

### 14.2 Model Knowledge Without Selected Sources

When the Run Evidence Set is empty, the existing source-less behavior continues. The Answer has no Citations and clearly avoids claiming Source grounding.

### 14.3 Zero-Support Fallback

When selected Sources exist, a whole-answer fallback is eligible only after complete, non-degraded bounded research records zero supporting Evidence. It uses a fresh model call containing no Source passage, Evidence preview, or Citation handle. Its opening states that the response is not based on the selected Sources, and it has no Citations.

Partial support, ambiguous support, incomplete research, retrieval degradation, Source deletion, authorization loss, cancellation, and deadline or recovery failure are ineligible.

The producing Run owns Grounding Outcome. Assistant Message does not regain `answer_mode`.

### 14.4 Publication Barrier

One transaction revalidates current Run authority, lease fence, Chat ownership, Notebook Capability, every pinned Source and Evidence Revision, active deletion state, every Citation, every Claim Support Record, and Grounding Outcome invariants. It then inserts exactly one Assistant Message and its Citations while completing the Run and Job. Failure publishes neither Answer nor Citations.

## 15. Source Viewer And Citations

One Viewer shell supports two entry modes:

- direct Source inspection from the Sources list;
- Citation resolution focused on an addressed Evidence Unit or continuous Unit range.

Format adapters render:

- PDF: immutable page plus source-native highlight region;
- PPTX: immutable rendered slide plus element region;
- image: immutable original image plus region;
- audio and YouTube: immutable transcript plus timestamp interval;
- HTML and DOCX: immutable normalized block with surrounding structure;
- TXT and Markdown: immutable normalized text range.

Hover preview loads a bounded authoritative Evidence excerpt. If precise coordinates are unavailable, the Viewer focuses the real Evidence Unit without fabricating a narrower highlight. Direct Source inspection displays Evidence Coverage warnings in source order. The Viewer never refetches URLs or YouTube, grants original-file download, exposes object-store credentials, or reads Qdrant as content authority.

After Source deletion, the Answer and Citation marker remain, but resolution reports unavailable and reveals no former passage.

## 16. Developer Trace And Replay

Sprint 6 extends the existing Collector and Dashboard semantics with:

- Source processing stage, adapter/configuration, coverage, and failure metadata;
- search purpose and logical Action identity;
- Dense and BM25 completion, candidate identities, ranks, and counts;
- RRF and rerank position transitions;
- authoritative Evidence load and Agent selection outcome;
- Retrieval Degradation stage and reason;
- Claim Support verdict and publication outcome;
- latency, token, and cost measurements by stage.

`platform.trace.read` exposes metadata only, with no Query, Evidence, Source, Chat, model, or verifier body. An explicit audited `platform.trace.replay` request may load only allow-listed normalized content used by the observed operation, encrypted at rest, returned `no-store`, and retained for seven days by default. Replay cannot browse a complete Source. Neither path requests, derives, labels, or stores chain of thought.

Member-facing APIs and SSE contain none of this diagnostic detail.

## 17. Offline Evaluation

The offline Eval Harness runs locally or in CI through the same Source, Retrieval, Models, Agent, Citation, and Viewer-facing evidence interfaces used by the product. It has no second RAG implementation and no management UI.

Human-authored, versioned Eval Cases contain fixed non-sensitive Source fixtures, questions, allowed Sources, expected Evidence or equivalent Evidence sets, parsing-coverage expectations, required facts, forbidden claims, and scoring rubrics. Every supported Source family and Chinese, English, and mixed-language retrieval has critical cases.

An Eval Run pins extraction, Evidence Revision, chunker, analyzer, BM25, embedding, fusion, reranker, degradation, composer, verifier, prompt, and Agent configuration. It records:

- extraction and Evidence Coverage correctness;
- retrieval recall and ranking quality;
- Citation identity, correctness, and claim coverage;
- unsupported-claim and groundedness rates;
- answer rubric results;
- latency by stage;
- tokens and estimated cost.

Promotion has three gates:

1. authorization, deletion, Citation identity, and Publication Barrier invariants allow zero failures;
2. every designated critical case passes;
3. remaining quality, latency, and cost aggregate thresholds, frozen before comparison, pass.

Model judges may supplement language and completeness scoring but cannot alter golden truth or authorize promotion. Concrete retrieval parameters and thresholds are established from the initial benchmark and capacity runs, recorded with the promoted version, and changed only by another evaluated version.

## 18. Failure Semantics

- Admission failure creates no selectable Source and leaves no durable unowned object.
- Processing failure exposes one safe typed reason and Retry/Remove; it publishes no active Evidence Revision.
- Known bounded extraction gaps may publish Ready; unknown gaps fail.
- Index build or verification failure leaves no active partial version.
- Qdrant data loss causes rebuild or explicit unavailability, never evidence loss.
- Retrieval failure produces no Answer and cannot masquerade as insufficient evidence.
- Verification exhaustion removes or discloses unsupported content; it never relaxes support rules.
- Cancellation, authorization loss, Source deletion, or stale lease prevents publication even after model work completes.
- Viewer coordinate failure falls back to the actual Evidence Unit; authority or deletion failure returns unavailable.
- Trace or Replay delivery failure follows Sprint 5 diagnostic semantics and does not change product authority.

## 19. Security, Privacy, And Deletion

- Every Control Plane operation uses Capability authorization plus PostgreSQL RLS.
- Every Worker continuation reauthorizes and presents its current lease fence.
- Every retrieval channel receives only server-constructed Notebook, Run Evidence Set, and version filters.
- Extractors and Fetcher have no application database, Qdrant, or durable product credentials.
- Fetcher blocks private, loopback, link-local, reserved, and rebinding destinations for IPv4 and IPv6.
- Uploaded and fetched content is untrusted; parsers run under least privilege with bounded CPU, memory, time, file count, expansion, and scratch storage.
- External model requests send only the minimum content needed for the configured operation and follow documented retention policy.
- User content is not used for product-model training.
- Deleting a Source immediately revokes product visibility and future retrieval in PostgreSQL, invalidates affected active Runs, and enqueues idempotent purge of originals, normalized/viewer artifacts, Qdrant points, and relevant Replay content.

## 20. Capacity And Performance

Target operating profile remains about 100 registered users and 10 concurrent Agent/Source jobs.

Interactive Agent, Source Processing, and offline Eval/Reindex use fixed Workload Classes. Interactive capacity is reserved; evaluation cannot consume it. Source processing enforces per-format versioned budgets for bytes, expanded structure, pages/slides, objects, DOM nodes, duration, pixels/frames, model calls, wall time, memory, and temporary storage.

Sprint 6 capacity tests freeze concrete limits and promoted retrieval bounds. At minimum they prove:

- 10 mixed Agent/Source jobs remain bounded in memory, temporary disk, connections, and model concurrency;
- processing one large legal Source does not prevent reads of Ready Sources;
- direct upload does not proxy 100 MB bodies through the Control Plane;
- Qdrant filtering remains mandatory and indexed under the target corpus;
- retrieval, verification, Citation load, and final publication latency are measured separately;
- evaluation and reindex work yield to interactive workloads;
- cancellation and deletion terminate future work and prevent stale publication.

The PRD does not promise a public production latency SLO. The promoted Eval Run records the local acceptance thresholds used for this delivery.

## 21. Verification

### 21.1 Source Contracts

- golden extraction fixtures for every supported format;
- native text, structure, tables, visual regions, transcript time, and coordinate tests;
- deterministic artifact and Evidence Unit identity tests;
- malformed, encrypted, empty, oversized, decompression-bomb, object-count, and timeout cases;
- bounded-gap Ready versus unknown-gap Failed tests;
- duplicate, multi-file partial-result, retry, rename, and permanent-delete tests;
- Fetcher SSRF, redirect, DNS rebinding, content-type, and size tests.

### 21.2 Retrieval

- deterministic chunk rebuild and version-change tests;
- identical index/query analyzer tests for Chinese, English, and mixed language;
- Dense, BM25, RRF, authoritative reload, and reranker contract tests;
- channel/reranker degradation matrix;
- Qdrant payload filter and hostile client-filter tests;
- stale, forged, deleted, cross-Notebook, unselected-Source, and wrong-version point rejection;
- index build, verification, promotion, pinning, rollback, and purge tests.

### 21.3 Agent And Citations

- iterative `search_evidence` Action and Checkpoint recovery tests;
- Run Budget and bounded Action Result tests;
- deterministic Citation range and scope validation;
- verifier pass, fail, malformed result, retry, rewrite, and exhaustion tests;
- partial-support no-blending tests;
- zero-support fallback eligibility and fresh-context isolation tests;
- Publication Barrier races for deletion, authorization, cancellation, and lease loss;
- exactly-one atomic Answer plus Citations publication.

### 21.4 Web And Dashboard

- multi-file per-item status and accessibility;
- Processing, Ready, Failed, Retry, Rename, Remove, and selection behavior;
- direct Viewer and every format-specific Citation target;
- hover preview, bounded-gap warning, coordinate fallback, and deleted Source behavior;
- no answer token streaming and no Member-facing diagnostic data;
- developer RAG Trace metadata and capability-gated audited Replay;
- Simplified Chinese and English copy, keyboard navigation, responsive layout, and browser acceptance.

### 21.5 Evaluation And Regression

- critical Eval Cases for every Source family and language path;
- frozen-threshold promotion reproducibility;
- model-judge non-authority tests;
- 10-job mixed workload and cancellation capacity test;
- Go unit/integration/race/vet/format/build gates;
- Web unit/typecheck/lint/build/accessibility/Playwright gates;
- real PostgreSQL, MinIO, Qdrant, Bifrost-controlled upstream, and opt-in live-provider smoke tests;
- Sprint 1 through Sprint 5 regression journeys.

Mock-only green tests are insufficient for completion.

## 22. Explicitly Out Of Scope

- pasted text
- cloud-drive import or synchronization
- web search, Source discovery, recursive crawling, or automatic URL refresh
- MinerU or another general-purpose parsing service
- learned sparse retrieval or native visual-vector retrieval
- automatic Source overview or suggested questions
- Member-facing retrieval stages, Trace, Replay, verifier details, or chain of thought
- answer token streaming
- original-file download
- generated Outputs, saved answers, reports, study guides, quizzes, slides, mind maps, or audio overviews
- Viewer/Editor behavior, invitations, ownership transfer, Member management, or shared-library UI
- public links, shared Chats, organizations, or anonymous access
- Eval UI, automatic golden-data generation, automatic tuning, or online A/B testing
- production launch, OIDC, Kubernetes, multi-region operation, HA, or SLA commitments

## 23. Delivery Sequence

1. freeze Source, Evidence, Citation, Retrieval, and Eval contracts plus golden fixtures;
2. add Source authority schema, RLS, lifecycle, purge intent, and owner-facing APIs through TDD;
3. add independent direct-upload intents, multi-file Web flow, URL admission, and restricted Fetcher;
4. implement format adapters incrementally behind one Normalized Source Artifact contract, starting with TXT/Markdown and fixed PDF fixtures before Office, HTML, transcript, audio, and image paths;
5. add Evidence Revision, Evidence Unit, Evidence Coverage, normalized/viewer artifact publication, and direct Source Viewer;
6. add deterministic chunking and Retrieval Index Version authority;
7. add Qdrant Dense and BM25 projection, scoped search, RRF, authoritative reload, reranking, and degradation;
8. register `search_evidence`, pin Run Evidence Set, and integrate iterative research with existing Checkpoints and Run Budget;
9. add composition mapping, deterministic Citation checks, Claim Support verifier, Grounding Outcome, and atomic Answer/Citation publication;
10. add Citation previews and every format-specific Viewer focus behavior;
11. extend Trace semantics, Collector projection, Dashboard, and audited Replay for RAG;
12. build the offline Eval Harness, establish frozen gates, evaluate candidates, and promote the first Retrieval Index Version;
13. execute security, deletion, failure, capacity, live-provider, browser, and Sprint 1-5 regression gates;
14. audit every success criterion against concrete evidence before completion.

Each implementation step is split into independently reviewable, verified commits. No step may make Qdrant authoritative, publish partial answers, bypass Capability/RLS checks, expose internal RAG execution to Members, or introduce a general-purpose parsing service.

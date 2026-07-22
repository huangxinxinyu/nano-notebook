# Nano Notebook Product Requirements

## Product Definition

Nano Notebook is a research workspace for individual researchers and deep learners. Its destination is source-grounded research: a person collects trusted material inside a bounded Notebook, asks multi-step questions, and verifies important conclusions against original evidence. Before Source and retrieval delivery, the product also supports a clearly disclosed model-knowledge Chat mode so the durable Agent interaction can be useful and exercised end to end.

The initial product is not a general-purpose assistant, a team knowledge base, or a content-production suite. Its core loop is:

1. Create or enter a Notebook.
2. Add trusted Sources.
3. Select the Sources relevant to a question.
4. Ask the Research Agent to investigate across them.
5. Verify the Grounded Answer through Citations.

## Release Boundary At A Glance

| Area | Initial release | Committed follow-up | Not committed |
| --- | --- | --- | --- |
| Source entry | Documents, known public web pages, YouTube, audio, images | Search discovery | Pasted text, cloud drives, and sync |
| Research | Disclosed model-knowledge Chat plus read-only multi-step Agent and strict grounded mode | Search-assisted Source discovery | External actions, code, user-visible execution traces, undisclosed evidence blending |
| Evidence | Text, structure, tables, transcripts, OCR, visual evidence, passage-level Citations | None required | Mutable or silently refreshed evidence |
| Collaboration | Email invites, Viewer/Editor/Owner, shared Sources, private Chats | None required | Public links, shared Chats, organizations |
| Durable content | Private Chats | Reports, guides, maps, quizzes, slides, audio Outputs | Notes and shared drafts |
| Clients | Responsive web | None required | Native mobile application |
| Commercial surface | None | None required | Billing, plans, enterprise administration |

Anything absent from the initial-release column is outside initial acceptance unless this document is explicitly revised.

### Foundation Delivery Sequence

The model-knowledge Chat capability is a formal but deliberately narrow product capability introduced in Sprint 2A. It answers directly from the configured model without claiming to have read Sources, searched the web, or produced a Grounded Answer. It is used while no accepted search result contains a valid Evidence range, including when Sources are selected but irrelevant to an ordinary conversational request. The Answer carries no Citations and cannot claim Source support; the Agent may suggest useful Sources when they would materially improve accuracy, recency, depth, verification, or citation quality.

This mode proves private Chat, durable Message/Run/Job admission, real model execution, publication, refresh, and failure behavior before RAG exists. It does not weaken the grounded contract: after any accepted search returns citeable Evidence, the final response is strict grounded JSON, and model knowledge never fills gaps in a partially supported Answer.

## Initial Release

### Accounts And Library

- An account is required to create, own, or join a Notebook.
- Registration and invitations use email identity. The authentication mechanism is a technical decision.
- The library separates owned and shared Notebooks and supports title search plus recent activity ordering.
- A user can own up to 100 Notebooks. Shared, non-owned Notebooks do not consume that quota.
- A user can create, rename, open, leave, and, when Owner, permanently delete a Notebook.
- Deleting a Notebook permanently deletes its Sources, pending invitations, membership, and all private Chats contained by it. The Owner must confirm the destructive action, and affected Members are notified.
- Account deletion requires the user to transfer or delete every owned Notebook first. Deleting the account then permanently removes the user's remaining private Chats and memberships.

### Notebook Information Architecture

The desktop Notebook workspace has three stable regions:

- **Sources**: add, inspect, select, rename, and remove evidence.
- **Chat**: manage private Chats and interact with the Research Agent.
- **Outputs**: a reserved information-architecture region for future generated work.

The initial interface does not show dead Output controls or empty promotional placeholders. It preserves enough layout and navigation capacity to add the Output workspace later without redefining the Notebook mental model.

The initial release is a responsive web product. A native mobile application is not required.

### Source Inputs

The initial release supports:

- PDF
- TXT
- Markdown
- DOCX
- PPTX
- Public HTML web pages
- Public YouTube videos with captions
- Uploaded MP3, WAV, or M4A audio
- Uploaded PNG, JPEG, or WebP images

The local-file picker supports selecting multiple files in one interaction. Every accepted file creates an independent Source with its own processing lifecycle; an invalid or failed item does not roll back the other accepted files, and the interface reports the outcome per item.

Adding a known URL is Source ingestion, not web search.

For public web pages, only the page's primary text is imported. Nested pages, embedded media, authenticated pages, and paywalled content are unsupported. A URL resolving directly to a supported document is treated as that document type.

For YouTube, only public videos with available captions are accepted, and the imported evidence is the transcript rather than the video stream. Videos without usable speech or captions fail with a clear reason.

Uploaded audio is transcribed during Source processing. Audio without usable speech fails with a clear reason.

Uploaded images are processed through OCR and visual understanding. The Source viewer preserves the original image and lets a Citation identify a text region or visual region when possible.

Documents include extracted text, document structure, readable tables, and usable embedded visual evidence. A Source that yields no usable textual or visual evidence fails with a clear reason.

### Source Processing

- Each Notebook contains at most 50 Sources.
- Each Source is limited to 100 MB and 500,000 words or equivalent text, whichever limit is reached first.
- Format-specific processing limits additionally bound expanded bytes, pages or slides, embedded objects, HTML expansion, media duration, decoded pixels, and other resource-amplifying structure. Exceeding a hard limit fails the Source with a useful reason; processing never silently truncates a Source and presents it as complete.
- A Source moves through visible `Processing`, `Ready`, or `Failed` states.
- Only `Ready` Sources can be selected for Chat.
- Processing one Source does not block Members from using other ready Sources.
- A failed Source displays a useful failure reason and can be retried or removed.
- A ready Source can be opened directly from the Sources list in the same Source Viewer used by Citations. It exposes its original or normalized content and any Evidence Coverage warnings for inspection; the initial release does not generate a Source overview or offer original-file download.
- A Source with usable evidence may become `Ready` when processing omitted only precisely identified regions. Its inspection view prominently identifies every known coverage gap; unknown coverage or loss of primary content makes the Source `Failed`.
- Retrieval and Grounded Answers use only published evidence and never infer content from a processing gap.
- Citations navigate to a page, slide, section, transcript timestamp, image region, or normalized passage appropriate to the Source type.

### Source Lifecycle

- A Source is an immutable evidence snapshot.
- Uploading a byte-identical file to the same Notebook is rejected as a duplicate and identifies the existing Source. Duplicate detection never reveals or reuses Source identity across Notebooks; adding a known external URL again creates a new immutable snapshot even when its fetched content is unchanged.
- Editors and Owners can add, rename, retry, and delete Sources but cannot replace their evidence content in place.
- Public web Sources do not refresh automatically. Updated material is added as a new Source.
- Source deletion is permanent and requires confirmation.
- Deleting a Source removes it from every Chat's available selection and all future Agent use.
- Existing Chat messages and inline Citation markers are never rewritten after Source deletion.
- Opening a Citation whose Source was deleted reports that the Source is unavailable and does not reveal the former passage.

### Private Chats

- Every Member can create multiple private Chats within a Notebook.
- A Chat and its history are visible only to its creator, including from the Owner.
- Chats do not share conversation history with one another.
- A Chat title is generated from its first question and can be renamed.
- The creator can permanently delete a Chat after confirmation.
- The initial release does not support sharing, archiving, starring, exporting, or restoring Chats.
- Historical user messages cannot be edited. A user can retry a failed or stopped Agent run as a new run only while its question remains the latest unanswered User Message; after the Chat advances, historical retry and branching are unavailable.
- A user can copy a completed answer, but completed-answer regeneration is unavailable and the answer cannot be published or shared in the initial release.

### Chat Response Controls

- Each Chat supports `Default` and `Learning Guide` response modes.
- `Learning Guide` favors explanations, questions, and progressive understanding while remaining strictly grounded.
- Response length can be `Shorter`, `Default`, or `Longer`.
- These controls affect subsequent runs in that Chat and do not rewrite history.
- Arbitrary custom instructions and custom personas are excluded from the initial release.

### Source Selection

- A new Chat initially selects every ready Source present when the Chat is created.
- Each Chat remembers its own Source selection.
- A Member can include or exclude Sources before submitting the next question.
- Selection changes affect subsequent answers only.
- Sources added later do not silently enter existing Chats.
- When no Source is selected, a question runs in model-knowledge mode. When Sources are selected, the final response remains claim-free text while no accepted search contains a valid Evidence range. Any citeable Evidence switches the Run to strict grounded JSON; partial support remains grounded and discloses its gaps.

### Research Agent

The Research Agent is a read-only, multi-step research assistant. For one question it may perform several rounds of retrieval, inspect evidence across Sources, compare claims, synthesize findings, identify contradictions, and verify support before answering.

The initial Research Agent cannot:

- Mutate Sources, Notebooks, membership, or permissions
- Browse the internet or search for new Sources
- Execute arbitrary code
- Call external services
- Create durable Outputs
- Silently mix unselected Sources, model knowledge, or hidden internet context into a source-grounded answer

If selected Sources partially support an answer, the Agent uses only that Evidence and states what remains insufficient instead of filling gaps with general knowledge. Before any accepted result contains citeable Evidence, the Composer may instead finish with claim-free text; this remains valid after an empty, failed, or degraded no-Evidence search and creates no Citations. Source deletion, authorization loss, and cancellation still prevent publication. Message rows do not carry a duplicate answer mode.

### Agent Run Experience

- A submitted question creates a durable Agent run.
- While work is active, the interface shows only a basic running state and Stop control; retrieval queries, candidate rankings, evidence-selection details, verification records, internal execution, and model reasoning are not Member-facing product data.
- Draft answer tokens are not streamed to Members. A verified Answer and its Citations appear together only after the Publication Barrier commits them.
- A user can navigate elsewhere in the web product and return without losing the run.
- Reloading the page reconnects to the current run or displays its latest durable state.
- The user can stop a run.
- A stopped or failed run preserves the question and offers retry only while it remains the latest unanswered User Message.
- An incomplete response is not presented as a completed Grounded Answer.
- The initial release permits one active Agent run per user at a time. Other Chats remain readable while it runs.
- If a selected Source is deleted before a run completes, the run stops without producing a completed answer and offers retry against the remaining selection only while its question remains the latest unanswered User Message.
- Removing a Member or deleting the Notebook cancels affected active runs before applying the permanent-deletion lifecycle.

### Grounded Answers And Citations

- A Grounded Answer uses only the Sources selected for that Chat run.
- Key factual claims and synthesized conclusions require inline Citations.
- A conclusion synthesized from multiple Sources cites each material supporting Source.
- A Citation marker is not sufficient by itself: before publication, each factual or synthesized claim must pass support and coverage verification against the cited evidence. Unsupported claims are researched again, removed, or replaced by an explicit insufficient-evidence statement.
- Hovering a Citation previews the supporting original passage.
- Selecting a Citation opens one Source Viewer shell at the corresponding page region for PDF, rendered slide element for PPTX, original image region, audio or YouTube transcript interval, normalized HTML or DOCX block, or TXT/Markdown range. When precise coordinates are unavailable, the Viewer falls back to the real Evidence Unit and never fabricates a narrower highlight.
- A very short Source may be cited as a whole when a meaningful passage cannot be isolated.
- Answers distinguish agreement, disagreement, and missing evidence across Sources.
- Answers follow the language of the user's question. Source language does not restrict answer language.
- The Viewer reads the immutable Source snapshot and never refreshes a URL or YouTube resource during Citation resolution; the initial release does not offer original-file download.

### Sharing And Permissions

Each Notebook has exactly one transferable Owner and at most 50 additional Members.

| Capability | Viewer | Editor | Owner |
| --- | --- | --- | --- |
| View ready Sources | Yes | Yes | Yes |
| Create and manage own private Chats | Yes | Yes | Yes |
| Add, retry, rename, or delete Sources | No | Yes | Yes |
| Invite or remove Members | No | No | Yes |
| Change Member roles | No | No | Yes |
| Transfer ownership | No | No | Yes |
| Delete Notebook | No | No | Yes |
| Download original uploaded files | No | No | No |

- The Owner invites a specific email address as Viewer or Editor.
- An invitee must register before accessing the Notebook.
- Invitations expire after seven days and may be revoked or resent by the Owner.
- Pending invitations consume the 50-Member capacity until revoked or expired.
- Public links, anonymous access, group invitations, and anyone-with-the-link access are excluded.
- Removing a Member or voluntarily leaving immediately revokes access and permanently deletes that Member's private Chats in the Notebook.
- Rejoining does not restore prior Chats.
- Transferring ownership selects an existing Member as the new Owner and demotes the previous Owner to Editor by default.

### Language And Presentation

- The initial interface supports Simplified Chinese and English.
- Source content and Chat questions may use other languages when the configured model can process them.
- Grounded Answers default to the language of the current question.
- Dates, file sizes, processing states, errors, destructive warnings, and permission labels are localized.

### Data Expectations

- Source and Chat content is private to the access boundaries described above.
- User content is not used to train product models.
- Owner and Editor access to shared Sources never grants access to another Member's private Chat.
- A Platform Operator with `platform.trace.read` may inspect RAG execution metadata but no Source or Chat body. A separately authorized `platform.trace.replay` request may reveal only the bounded normalized Query, Evidence excerpts, and model or verifier content used by that Agent operation; every attempt is audited, Replay expires after seven days by default, and it never permits browsing the complete Source.
- Removing content through the permanent-delete actions defined above is presented as irreversible.
- Provider-specific retention, backup expiry, and production key-custody guarantees remain production policy decisions.

## Committed Follow-Up Scope

These product capabilities are expected after the initial release but are not initial-release acceptance criteria:

- Source discovery through search, with explicit review before results become Sources
- An Output workspace for reports, study guides, mind maps, quizzes, slide decks, and audio overviews

These Outputs are committed product scope. Their decomposition, dependencies, delivery order, milestones, and estimates are intentionally deferred until the technical `grill-with-docs` session; no coarse roadmap is treated as an accepted schedule.

Search never silently expands a Chat's evidence. A discovered item must be reviewed and added as a Source before the Research Agent can cite it.

## Uncommitted Ideas

These may be reconsidered but are not promised:

- Notes or saving a Chat response as a shared Notebook object
- Public or link-based Notebook sharing
- Cloud-drive imports and synchronization
- Shared Chats or collaborative Chat threads
- Original-file download
- Native mobile applications
- Generated Source overviews or suggested Chat questions

## Explicit Non-Goals For Initial Release

- General web search or Deep Research
- An unlabeled general-purpose assistant or undisclosed model-knowledge claims inside grounded answers
- User-visible retrieval, verification, execution traces, or model chain of thought
- A global knowledge base spanning Notebooks
- External tools, code execution, or automation
- Durable generated Outputs
- Note taking or shared drafts
- Cloud connectors
- Public sharing
- Organizations, teams, enterprise administration, audit dashboards, or billing
- Source version replacement or automatic URL refresh
- Chat export, sharing, archive, favorite, or recovery
- Custom Chat instructions or personas

## Primary Acceptance Journeys

### Solo Research

1. A user creates a Notebook and adds several supported Sources.
2. Each Source reaches a clear ready or failed state.
3. The user opens the normalized or original content and sees any Evidence Coverage warnings.
4. The user creates a Chat, adjusts selected Sources, and asks a cross-Source question.
5. The user can leave, reload, or stop the running work without losing its durable state.
6. The user receives a Grounded Answer.
7. Every key conclusion can be traced through an inline Citation to supporting context.
8. A YouTube transcript, uploaded audio file, and uploaded image can each become a usable Source with type-appropriate Citations.

### Shared Research

1. An Owner invites one Viewer and one Editor.
2. The Editor adds a Source that becomes available to every Member.
3. Each Member asks private questions that no other Member can see.
4. The Viewer cannot mutate Sources or membership.
5. The Editor cannot manage membership or delete the Notebook.
6. Removing a Member immediately removes access and permanently deletes that Member's private Chats.

### Evidence Deletion

1. An Editor deletes a Source after a destructive warning.
2. Existing answers remain unchanged.
3. Their affected Citation markers remain visible but report that the Source is unavailable.
4. No future Agent run can select or retrieve the deleted Source.

### Durable Agent Run

1. A user starts a multi-step question.
2. The user navigates away or reloads the page.
3. Returning to the Chat shows the continuing or completed run rather than losing it.
4. Stopping or failure retains the question and permits a clean retry.

## Definition Of Product Completion

The initial product baseline is complete when all four primary acceptance journeys work end to end and the following statements hold:

- A user can reach a verifiable Grounded Answer without leaving the Notebook workspace.
- A key claim cannot appear as successfully grounded without a usable Citation or an explicit insufficient-evidence result.
- A Viewer, Editor, or Owner cannot cross the role and private-Chat boundaries defined in the capability matrix.
- Source and membership deletion produce the documented irreversible outcomes without silently rewriting surviving Chat history.
- An Agent run survives navigation and reload, restores its current state, and can be stopped or retried without exposing internal execution details.
- Reloading a Chat restores the latest Agent Run outcome for each User Message; only the latest unanswered Message may expose Retry.
- No initial-release workflow depends on a committed follow-up or uncommitted capability.

Growth, retention, monetization, and enterprise adoption are not success criteria for this learning-focused initial product.

## Product-Level Work Areas

The initial product requires eight coherent work areas, independent of later technology choices:

1. Account and Notebook library
2. Notebook membership and role enforcement
3. Multiformat Source ingestion, transcription, OCR, visual processing, inspection, and lifecycle
4. Immutable evidence addressing for Citations
5. Durable private Chat and message lifecycle
6. Durable multi-step Research Agent runs
7. Strict grounded answering and passage-level Citation interaction
8. Responsive Notebook workspace with future Output expansion capacity

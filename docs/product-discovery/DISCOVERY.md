# Product Discovery

This document preserves the product reasoning behind Nano Notebook. It records confirmed decisions separately from proposals so that an unanswered question is never mistaken for a requirement.

## Current Product Thesis

Nano Notebook is a source-grounded research workspace for individual researchers and deep learners. A user creates or enters a Notebook to work with the sources available inside that Notebook.

## Confirmed Decisions

### Primary User

The initial product serves individual researchers and deep learners who already have trusted material and want to understand, compare, trace, and develop knowledge from it.

This focus excludes a generic AI assistant as the initial product identity. Team knowledge management and content creation may be supported by later capabilities, but they are not the primary framing.

### Source Acquisition Scope

The product accepts uploaded and external Sources. Search-based source discovery remains distinct from adding a known URL and is still outside the initial core workflow.

Keeping search in scope matters because the product may eventually support the full journey from finding material to understanding it. A Member adding a URL they already know is source ingestion, not search.

The product scope includes uploaded PDF, TXT, Markdown, DOCX, and PPTX documents; public web-page URLs; public YouTube URLs; uploaded audio; and uploaded images. Pasted text and cloud-drive imports are excluded because there is not yet enough user or product value to define and maintain those entry paths responsibly.

The initial release supports document upload, public web-page URLs, public YouTube URLs with captions, uploaded audio, and uploaded images. YouTube contributes transcript evidence, audio is transcribed, and images contribute OCR and visual evidence.

A Notebook can contain at most 50 Sources in the initial product.

An individual Source can contain at most 100 MB of input data and 500,000 words or the equivalent text length. A Source is rejected if either limit is exceeded. The language-neutral measurement for equivalent text length is deferred to technical design.

A user can own at most 100 Notebooks. Notebooks shared with the user in a non-owner role do not count toward this limit.

### Notebook Boundary

A Notebook is the primary product object. It persistently groups the sources for a research topic and acts as the access boundary between different bodies of material.

Users access different sources by entering different Notebooks. This rules out a single global source pool as the initial mental model.

### Sharing And Roles

A Notebook can be shared. Access is represented by three roles: Viewer, Editor, and Owner.

The roles follow the NotebookLM Enterprise boundary. Viewers inspect Sources and use private Chat; Editors also maintain Sources; the single Owner additionally manages access, transfers ownership, and deletes the Notebook.

### Notes

The initial product will not include Notes. Users will not be able to promote a chat response into a separate persistent Notebook artifact in the first release.

Notes may be reconsidered later, but they are not currently a committed future capability. This keeps the initial content model focused on shared Sources and Chat.

### Outputs

The initial release does not create durable Outputs outside Chat. Reports, study guides, mind maps, quizzes, slide decks, and audio overviews are committed future product capabilities. Their concrete roadmap will be created only after technical decomposition during the technical `grill-with-docs` session.

The initial information architecture must reserve a stable place for a future Output workspace so that adding it does not require redefining the core Notebook layout. The initial interface should not expose non-functional Output controls merely to fill that space.

### Collaboration Model

Sources belong to the Notebook and are visible to its Members according to their role. Chats belong to the individual Member who created them and are not visible to any other Member, including Editors and Owners.

The initial product does not support sharing a Chat. Sharing a Notebook grants access to its shared Sources, not to any Member's research history.

Each Member can create multiple independent private Chats inside the same Notebook. Chats do not share conversation history with one another, although each can use Sources from the containing Notebook.

A Chat receives an automatically generated title based on its first question. Its creator can rename it or permanently delete it after confirmation. The initial product does not support archiving, starring, or restoring Chats.

A new Chat initially selects all Sources currently in the Notebook. Each Chat remembers its own Source selection. A Member can include or exclude Sources before asking a question; the change affects subsequent answers only and does not rewrite existing answers.

Sources added to the Notebook after a Chat is created are not automatically selected in that existing Chat. Newly created Chats continue to select all Sources available at their creation time.

When an Editor or Owner deletes a Source, it is immediately removed from every Chat's available selection and cannot support future answers. Existing Chat messages and their inline Citation markers are never rewritten. Opening an affected Citation reports that the Source was deleted or is no longer accessible and does not reveal the former passage.

Source deletion requires confirmation and cannot be restored in the initial product. The confirmation warns generically that existing Citations will become unavailable, without revealing which Members or private Chats used the Source.

A Source is an immutable evidence snapshot. A Member with edit access can rename it but cannot edit or replace its content. A web-page Source does not refresh automatically. Updated material must be added as a new Source, after which the old Source may be deleted explicitly.

Immutability ensures that a historical Citation never silently points to evidence different from what supported the original answer.

### Grounding Principle

Chat produces Grounded Answers using only Sources selected from the current Notebook and never supplements a partially supported answer with model knowledge or internet information. While no accepted search result contains a valid Evidence range, Chat may instead produce a claim-free Model-Knowledge Answer with no Citations and no claim of Source support. Any citeable Evidence activates the strict grounded contract.

Future search results must first be added to the Notebook as Sources before Chat can use them as evidence.

### Research Agent Boundary

The initial Research Agent may iteratively retrieve evidence, compare multiple Sources, synthesize findings, and verify support before producing a Grounded Answer. It is not limited to a single retrieval-and-response step.

The Research Agent is read-only. It cannot add, edit, rename, or delete Sources; change permissions; browse the internet; execute arbitrary code; call external services; or create durable Outputs. These exclusions apply even when the requesting Member otherwise has permission to perform the corresponding Notebook action manually.

### Agent Run Visibility

The Member-facing product shows only a basic active state, Stop, the final Answer, and Citations. It does not expose retrieval queries, candidate rankings, evidence-selection details, verification records, internal execution, or model chain of thought. Operationally useful research actions belong to the restricted developer Trace Dashboard and audited Replay rather than the private Chat interface.

Users can stop an Agent run. A stopped or failed run preserves the user's question and offers retry only while it remains the latest unanswered question; once the Chat advances, historical retry and branching are unavailable. An incomplete response is not presented as a completed Grounded Answer.

**Status**: Confirmed.

### Citation Standard

Key factual claims and synthesized conclusions in a Grounded Answer require inline Citations. Hovering a Citation previews the supporting original passage; selecting it opens the Source at that passage with surrounding context. When a Source is too short to identify a meaningful passage, the Citation may reference the entire Source.

The standard follows NotebookLM's citation interaction while making citation coverage for key claims an explicit Nano Notebook requirement.

### Viewer Research Access

A Viewer can view the Notebook's Sources and create private Chats grounded in those Sources. A Viewer cannot add, remove, or modify Sources.

Viewer is therefore a research-use role, not merely a static preview role. Viewing does not include a committed ability to download the original uploaded material.

### Permission Reference

The role model should follow Google NotebookLM rather than define independent permissions one capability at a time.

Google documents two variants. In consumer NotebookLM, Editors can view, add, and remove shared contents and can share the Notebook further. In NotebookLM Enterprise, Editors otherwise act like Owners but cannot delete or share the Notebook or revoke access. The Enterprise variant therefore places access management exclusively with the Owner.

Google's documentation promises Viewers read-only access to Sources inside the Notebook but does not explicitly promise download access to an original uploaded file. Nano Notebook will not infer a download entitlement from read access; original-file download is excluded unless it is deliberately added later.

Nano Notebook adopts the Enterprise variant. Viewer can view Sources and conduct private Chats. Editor adds the ability to add and remove Sources. Owner adds exclusive responsibility for inviting Members, changing roles, revoking access, and deleting the Notebook. Original-file download is excluded from the initial product.

Each Notebook has exactly one Owner. Ownership can be transferred only to an existing Member. A transfer atomically makes the recipient the Owner and demotes the previous Owner to Editor, so a Notebook never has zero or multiple Owners.

In the initial release, only the Owner can invite a specific person by email and assign Viewer or Editor access. An invitee without an account must register before accessing the Notebook. Public links, anonymous access, and anyone-with-the-link access are excluded from the initial release.

A Notebook can have one Owner and at most 50 additional Members. Pending invitations reserve Member capacity until accepted, revoked, or expired.

An invitation expires seven days after it is issued. The Owner can revoke it before expiration or resend it after expiration; resending starts a new seven-day period. Expired and revoked invitations release reserved Member capacity.

When a Member is removed or voluntarily leaves a Notebook, access ends immediately and all of that Member's private Chats in the Notebook are permanently deleted without a retention or recovery window. The confirmation must state this irreversible consequence. Rejoining the Notebook later does not restore prior Chats.

Deleting a Notebook permanently deletes all Sources, pending invitations, membership, and every Member's private Chats. The Owner confirms the destructive action and affected Members receive a notification. This follows the established no-recovery lifecycle rather than introducing a separate trash model.

## Scope Guardrails

- This discovery session covers product shape and requirements only.
- Search must remain visible in the future product scope even though initial Source entry is limited to direct uploads and known public URLs.
- Technology, architecture, model, storage, deployment, and implementation choices are deferred to a separate grilling session.
- Notes and saved chat outputs are excluded from the initial product.

## Delivery Priorities

The immediate delivery priorities are the agent execution foundation, Source ingestion and grounded retrieval, permission enforcement, and private Chat lifecycle. Agent-runtime and retrieval technology choices remain deferred to the separate technical grilling session.

## Discovery Protocol

When the product owner is uncertain about a behavior, research the current official NotebookLM behavior first. Present the reference behavior, identify product-edition differences or gaps in Google's documentation, make a recommendation, and wait for explicit human confirmation before recording a Nano Notebook requirement.

## Decision Queue

Questions are resolved in dependency order, one at a time.

### Resolved: Collaboration Model

Which content is shared when multiple Members enter the same Notebook?

**Decision**: Sources are shared and each Member's AI conversations are private. The initial product has no shared output or Note layer.

**Reasoning**: This prevents unrelated personal exploration from becoming a noisy shared transcript and matches the product's initial focus on individual research. Deliberate knowledge publishing is deferred with Notes.

**Status**: Confirmed.

## Product References

These references describe product behavior as observed in official documentation on 2026-07-12. They inform the decision but do not define Nano Notebook's requirements.

### NotebookLM: Shared Knowledge, Private Exploration

Google shares a Notebook's sources and saved notes while keeping each Member's chat history private. A Viewer can interact with the Notebook but cannot upload sources or add notes. An Editor can maintain shared contents; in NotebookLM Enterprise, an Editor cannot delete or share the Notebook or revoke access.

A chat response becomes durable Notebook content only when a user explicitly saves it as a note. Public Notebook viewers can ask their own questions and consume artifacts created by owners or editors.

NotebookLM keeps three distinct content lifecycles:

1. **Chat history** persists for the user and contributes context to that user's later responses, but remains private.
2. **Note** persists on the Notebook's noteboard. A user may write a Note manually or save a complete AI response as a Note. A saved response preserves its original formatting, tables, and clickable inline citations, and is not editable after creation. Manually written Notes are editable. Notes in a shared Notebook are readable by Viewers and editable by Editors, with edits synchronized to collaborators.
3. **Source** is part of the Notebook's default evidence base. A Note does not automatically become a Source or automatically influence every answer: Notes are used in a prompt only when specifically selected. A user may explicitly convert one or all Notes into a Source.

This means "Save to Note" is a promotion from private conversation into durable shared output, not a promotion directly into the Notebook's evidence base.

NotebookLM places citations inline in Chat responses. A user can hover over a citation to preview the full quoted passage and select it to navigate to the passage in its Source with surrounding context. Google notes an exception for very short Sources, where NotebookLM may reference the entire document instead of an individual passage. Google's documentation does not promise that every factual statement receives its own citation.

Google documents that Editors can remove Sources and that Chat history is retained separately. For linked Drive Sources that become inaccessible, NotebookLM no longer lets users view or interact with the Source and excludes it from subsequent Chat use. Google does not document whether an existing answer remains visible after an ordinary Source deletion or exactly how its old Citation is rendered. Treating the answer as retained and the Citation as unavailable is therefore a product inference, not confirmed NotebookLM behavior.

References:

- [Use chat in NotebookLM](https://support.google.com/notebooklm/answer/16179559?hl=en)
- [Create and add notes in NotebookLM](https://support.google.com/notebooklm/answer/16262519?hl=en)
- [NotebookLM frequently asked questions](https://support.google.com/notebooklm/answer/16269187?hl=en)
- [Share notebooks in NotebookLM Enterprise](https://docs.cloud.google.com/gemini/enterprise/notebooklm-enterprise/docs/share-notebooks)
- [Use public notebooks and featured notebooks](https://support.google.com/notebooklm/answer/16322204)

### Claude Projects: Shared Knowledge, Explicit Chat Snapshots

Claude Projects share project knowledge and instructions. Each Member starts private chats against that shared context. A user may deliberately share a snapshot of a chat; later messages remain private until the snapshot is updated.

This is the clearest reference for separating the common evidence base from personal reasoning history.

Reference: [Manage project visibility and sharing](https://support.claude.com/en/articles/9519189-manage-project-visibility-and-sharing)

### Perplexity Projects: Collaboration-First Threads

Perplexity Projects share files and support Viewers and Contributors. In collaborative Projects, session sharing defaults to shared; Contributors can create sessions or continue existing ones. Sessions and files can also be pinned into the Project for all Members.

This model favors a visible team research trail over private individual exploration.

Reference: [What are Perplexity Projects?](https://www.perplexity.ai/help-center/en/articles/10352961-what-are-spaces)

### Microsoft 365 Copilot Notebooks: Shared Source Permissions

Sharing a Copilot Notebook attempts to grant recipients access to its linked files. The product therefore exposes a second permission concern: Notebook access does not necessarily guarantee source access when a source is governed by an external system.

This concern would become relevant only if Nano Notebook later reconsidered linked cloud sources. Cloud-drive imports are not currently in scope.

Reference: [Share a Microsoft 365 Copilot Notebook](https://support.microsoft.com/en-US/Microsoft-365-Copilot/share-a-microsoft-365-copilot-notebook)

### Working Interpretation

There are two established collaboration patterns:

1. **Private-first research**: share the evidence base, keep exploration private, and explicitly promote useful results. NotebookLM and Claude Projects follow this pattern.
2. **Shared-first research**: make research threads visible and continuable by collaborators. Perplexity Projects follows this pattern.

Because Nano Notebook's primary user is currently an individual researcher or deep learner, the private-first pattern remains the recommendation. Choosing the shared-first pattern would reposition the product toward collaborative team research and would make conversation governance a core requirement.

### Later Questions

The product baseline is consolidated in [REQUIREMENTS.md](./REQUIREMENTS.md). Remaining implementation choices belong to the separate technical grilling session.

## Session Log

### 2026-07-12

1. Chose individual researchers and deep learners as the primary user.
2. Limited initial source acquisition to upload while retaining search in future scope.
3. Established Notebook as both the source-isolation boundary and the sharing boundary.
4. Named Viewer, Editor, and Owner as the three access roles; detailed permissions remain unresolved.
5. Proposed, but did not yet confirm, a collaboration model of shared sources, private AI work, and explicitly shared outputs.
6. Compared NotebookLM, Claude Projects, Perplexity Projects, and Microsoft 365 Copilot Notebooks to identify private-first and shared-first collaboration patterns.
7. Excluded Notes and saved chat outputs from the initial product.
8. Confirmed that Chats are private to their creator, including from Notebook Owners, and cannot be shared in the initial product.
9. Confirmed that Viewers can view Sources and create private Chats but cannot maintain Sources.
10. Chose Google NotebookLM as the permission-model reference and withdrew the assumption that Source read access includes original-file download.
11. Adopted the NotebookLM Enterprise permission boundary, reserving access management and Notebook deletion for Owners.
12. Established a single transferable Owner for each Notebook.
13. Initially limited Sources to uploaded PDF, TXT, Markdown, DOCX, and PPTX documents, then reopened URL-based and other external inputs for full product discovery.
14. Limited each Notebook to 50 Sources.
15. Limited each Source to 100 MB; no word-count limit is yet confirmed.
16. Included pasted text, public web pages, public YouTube videos, uploaded audio, and uploaded images in the product Source scope; excluded cloud-drive imports. The pasted-text portion was later superseded by decision 37.
17. Initially set the Source inputs to document upload, pasted text, and public web pages; this was later superseded by decision 36.
18. Limited each Source to 100 MB and 500,000 words or equivalent text, whichever limit is reached first.
19. Limited each user to owning 100 Notebooks while excluding shared non-owned Notebooks from that quota.
20. Required Chat to answer strictly from selected Notebook Sources and to disclose insufficient evidence rather than use ungrounded knowledge. The zero-support case was later refined by decision 40.
21. Required inline, passage-level Citations for key claims, with hover previews, source navigation, and a whole-Source fallback for very short material.
22. Allowed each Member to create multiple independent private Chats within a Notebook.
23. Added automatic Chat titles, manual renaming, and confirmed permanent deletion; excluded archive, favorite, and restore controls.
24. Made Source selection persistent per Chat, defaulted new Chats to all current Sources, and prevented later Source additions from silently entering existing Chats.
25. Defined Source deletion to preserve historical Chat text and Citation markers while making their deleted Source targets unavailable.
26. Made Source evidence immutable while allowing title changes; updates are added as new Sources rather than replacing cited content.
27. Limited the initial release to Source management and Grounded Chat while committing to a future Output workspace and reserving its place in the product information architecture.
28. Prioritized agent execution, grounded retrieval, permissions, and Chat lifecycle without selecting their implementation technologies.
29. Defined the initial Research Agent as a read-only, multi-step Source research assistant with no Notebook mutation or external tools.
30. Required high-level stages and an expandable structured Reasoning Trace while excluding a user-facing claim of raw chain-of-thought access. This was superseded by decision 38.
31. Limited initial Notebook sharing to Owner-issued email invitations for Viewer or Editor access and excluded public or anonymous links.
32. Limited each Notebook to one Owner plus 50 additional Members, with pending invitations consuming capacity.
33. Required immediate permanent deletion of a departing or removed Member's private Chats, with no recovery after rejoining.
34. Set email invitations to expire after seven days, with Owner revocation and resend controls.
35. Consolidated the confirmed product boundary, reference-derived defaults, non-goals, and acceptance journeys in `REQUIREMENTS.md`.
36. Promoted YouTube, audio, and image Sources into the initial release; kept Outputs as committed scope while deferring their concrete roadmap to the technical grilling session.
37. Removed pasted text from the initial release and uncommitted Source scope because no distinct user value justified maintaining a separate manual evidence-entry path; TXT and Markdown remain the lightweight text inputs.
38. Removed the user-visible Reasoning Trace and detailed stages from the initial product. Members see only basic Run state, Stop, the final Answer, and Citations; restricted developer Trace and Replay retain observable RAG execution data, while model chain of thought is never captured.
39. Removed generated Source overviews and suggested Chat questions from the initial release because they add non-essential generated content outside the Answer and Citation loop; both remain uncommitted future ideas.
40. Allowed a whole-answer model-knowledge fallback only after complete, non-degraded research finds zero support in selected Sources. Partial support never mixes with model knowledge, fallback uses a fresh call without Source passages, carries no Citations, and discloses its basis in the Answer.
41. Allowed multi-file selection for local uploads while keeping each accepted file an independent Source and processing lifecycle; one invalid or failed item does not roll back the others.
42. Made the Citation-oriented Source Viewer directly accessible from the Sources list for inspecting ready content and Evidence Coverage, without adding Source summaries or original-file download.
43. Replaced Source-selection-driven final JSON and the fresh zero-support fallback with an Evidence-aware contract: claim-free text is valid until a search returns a citeable Evidence range; after that transition, grounded JSON, verification, and Citations are mandatory. This supersedes decision 40 while preserving the no-mixing rule for partially supported Answers.

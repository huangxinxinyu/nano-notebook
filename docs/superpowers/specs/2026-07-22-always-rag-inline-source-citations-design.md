# Always-RAG inline Source citation design

## Outcome

When one or more Sources are selected, Nano Notebook always attempts Evidence search before it permits a final answer. Retrieved Evidence is optional context for the Composer: the Composer may use it and add Source references, or ignore it and answer normally. The user-visible answer is always plain text. It never contains a model-authored `claims` array and never depends on duplicate text matching.

References use the Open Notebook-style marker `[source:<source_id>]`. Nano validates every marker against the Sources represented by accepted `search_evidence` results, converts valid markers into clickable numbered references, and discards invalid markers. New Citations are Source-level: selecting one opens the corresponding Source without promising a precise page, Unit, excerpt, or rune range.

## Product rules

- A Run with no selected Sources keeps the existing bare-chat path and performs no Source search.
- A Run with selected Sources must attempt `search_evidence` before a final response. There is no chat-intent classifier and no pre-retrieval decision about whether RAG is needed.
- After search, the Composer sees the question, conversation context, and accepted Action results. It decides whether the retrieved material is useful by either including valid Source markers or omitting them.
- A final response containing at least one valid marker is Source-cited. A response containing no valid marker is Source-free, even when retrieval returned Evidence.
- Empty, failed, degraded, or irrelevant retrieval does not change the final transport contract. Any nonblank plain-text answer remains valid and contains no Citations unless it includes a valid Source marker.
- The system does not automatically add a Source reference that the Composer omitted. This avoids attributing a general answer to retrieved material that the Composer did not declare it used.
- This design intentionally gives up claim-level support verification and precise Evidence navigation. It retains Source selection, authorization, retrieval provenance, marker allowlisting, and atomic publication.

## Always-RAG transition

For a selected-Source Run, the first research-capable model request forces the specific `search_evidence` function rather than sending `tool_choice:auto`. The model still authors the query and purpose, but it cannot finish or select an unrelated Action before the first search attempt.

After one durable `search_evidence` result exists, ordinary Agent-loop behavior resumes. The Composer may perform another focused search within the existing Action budget or return its final plain-text answer. Recovery derives the transition from durable Checkpoints, so a resumed attempt does not repeat an already accepted mandatory search solely to satisfy this rule.

A failed or complete-empty search counts as an attempted search and is returned to the Composer. The Composer may explain the limitation or answer without using Sources. Infrastructure failures retain their current typed Action-result and Run failure behavior; the always-RAG rule does not convert provider or lease failures into fabricated empty results.

## Final response and marker contract

The grounded system prompt no longer asks for JSON, claims, Evidence addresses, or verbatim duplicate spans. It tells the Composer:

1. use retrieved content only when it helps answer the current request;
2. place `[source:<source_id>]` immediately after material derived from a retrieved Source;
3. use only Source IDs present in the Action results;
4. omit Source markers when the answer does not use retrieved content;
5. acknowledge insufficient retrieved information instead of presenting unsupported model knowledge as Source-backed.

The model adapter treats every non-tool assistant response as plain text. Provider JSON mode is not used for final conversational answers. `FinalDraft` becomes a text-only decision value; `DraftClaim`, grounded-final format selection, and claim/citation decoding are removed from the active response path.

The marker parser is server-owned and bounded. It recognizes only the exact `[source:<source_id>]` syntax, caps the number of occurrences, and preserves first-occurrence order. A Source ID is allowlisted only when at least one structurally valid Evidence range for that Source appeared in an accepted search result for the same Run. Repeated references to the same Source reuse one published Citation number. Malformed markers and markers for non-allowlisted Sources are removed from the published text and create no Citation; they do not fail an otherwise valid answer.

The normalized assistant text retains valid Source markers so the frontend can render them at the position chosen by the Composer. The API also returns the durable Source-level Citation records required to resolve each marker safely. The frontend replaces markers with inline numbered citation buttons and deduplicates repeated markers in first-occurrence order.

## Grounding and persistence

New grounded outcomes are based on accepted markers rather than model-authored claims:

- `source_less`: no Sources were selected;
- `source_free`: Sources were selected and search was attempted, but the normalized final text contains no valid Source marker;
- `source_cited`: Sources were selected, search was attempted, and the normalized final text contains one or more valid Source markers.

The Grounding Service parses durable search results, builds the allowlist of retrieved Source IDs, normalizes the final text, and persists the ordered Source references. It no longer invokes the runtime Claim Support Verifier. Existing historical claim-level rows remain readable, but new Runs do not write `agent_claim_support_records` or claim-addressed `agent_draft_citations`.

Persistence adds a claim-free draft Source-reference representation keyed by Run and reference ordinal. Publication revalidates that:

- the Run and Job still hold publication authority;
- every selected Source pin remains valid and authorized;
- a search attempt exists for a selected-Source Run;
- every published Source reference belongs to the Run Evidence Set and appeared in an accepted search result;
- the normalized final text and ordered references match the persisted grounding-plan digest.

Source-level rows in the published Citation representation do not require `claim_text`, Evidence Revision, Unit, or rune bounds. Legacy precise Citation rows keep those fields for historical compatibility. Citation APIs distinguish the two shapes. A Source-level Citation resolves current authorized Source metadata and opens the normal Source Viewer without a preview excerpt or focused coordinate. If the Source has been deleted or is no longer available, the reference remains visible but resolves as unavailable.

## Failure and degradation behavior

- Plain text after the mandatory search attempt: accepted.
- Valid retrieved Source marker: retained and published as a Source Citation.
- Unknown or invented Source marker: removed; no Citation is published for it.
- No Source marker despite returned Evidence: accepted as `source_free`; no automatic attribution is added.
- Marker for a selected Source that did not appear in search results: removed.
- Search returns no Evidence: plain text remains valid and citation-free.
- Reranking is degraded but Evidence is returned: those returned Sources are still eligible markers, and degradation remains visible in Run metadata.
- Final response is empty: rejected under the existing nonblank final-answer invariant.
- Mandatory search cannot be durably attempted because execution loses authority or infrastructure fails: the Run follows the existing typed failure path and publishes nothing.

## API and interface behavior

Assistant text displays references inline, matching the answer location chosen by the Composer. Each distinct Source receives one stable number per message. Repeated markers reuse that number. The accessible label identifies the Citation number and Source title rather than a claim string.

Selecting an inline Source Citation opens the same Source Viewer used by the Sources panel. It does not show a quote tooltip or claim-specific highlight. Legacy precise Citations continue to show their existing excerpt and coordinate behavior.

## Evaluation and observability

Traces record whether mandatory search was attempted, whether any retrieved Sources were marker-eligible, the number of valid and discarded markers, the final grounding outcome, and retrieval degradation. Raw private content remains confined to the existing Replay boundary.

Runtime claim-support metrics no longer apply to new Source-level answers. Eval continues to measure parsing coverage, retrieval recall, answer quality, and forbidden facts. Citation evaluation changes from Evidence-range precision and claim coverage to Source-reference precision: every emitted Source must belong to the allowed Source set for the case. Answer faithfulness remains an offline quality evaluation rather than a transactional runtime verifier.

## Testing

Implementation proceeds test-first and covers:

1. selected-Source requests force the specific `search_evidence` Action before any final response;
2. recovery does not repeat an accepted mandatory search;
3. final assistant text is accepted after empty, failed, degraded, and evidence-bearing searches without JSON mode;
4. valid Source markers are retained, ordered, deduplicated, and published;
5. malformed, invented, selected-but-not-retrieved, and excessive markers are removed without failing the answer;
6. Source-free and Source-cited grounding plans are durably distinct and publication revalidates their digests and Run authority;
7. new Citation APIs and UI render inline Source references and open the Source Viewer without precise highlighting;
8. legacy precise Citations remain readable;
9. bare chat, Action iteration, cancellation, retry, SSE projection, and publication atomicity regressions remain green;
10. a live selected-Source degree-planner question completes after RAG with plain-text Source references, while selected-Source ordinary conversation also completes without citations.

## Non-goals

- A pre-retrieval chat or Source-intent router.
- A sidecar Evidence-use judge in the first implementation.
- Model-authored claims, exact-substring matching, or provider-enforced final JSON.
- Claim-level runtime verification or automatic removal of unsupported individual sentences.
- Precise page, Unit, timestamp, region, or rune-range navigation for new Source-level Citations.
- Automatically attributing an unmarked answer to every retrieved Source.
- Repairing the independent reranker HTTP 400 degradation.

# Evidence-aware final response design

## Problem

A Run switches to `agent-grounded-v1` as soon as the user selects at least one Source. The current model adapter then parses every assistant text response as a strict grounded `FinalDraft`, even before `search_evidence` has returned citeable evidence. This makes ordinary conversational answers fail as `model_invalid_response`. The same unconditional contract also makes an empty retrieval result require JSON even though there is nothing to cite.

The inverse relaxation is unsafe: once retrieval has returned authoritative Evidence addresses, accepting arbitrary text would bypass Nano Notebook's claim, citation, verifier, and publication barriers.

## Product rule

Final-response strictness is determined by authoritative tool results, not by Source selection and not by the mere invocation of `search_evidence`.

- Before any successful search result contains at least one valid Evidence range, the model may finish with plain text or with a valid `{text,claims:[]}` object. The published answer has no Citations and must not claim that the selected Sources support it.
- As soon as any successful search result contains a valid Evidence range, every subsequent final response must be strict grounded JSON. Claims and Citations continue through the existing verifier and publication barriers.
- A failed, degraded, malformed, or complete-empty search with no valid Evidence range does not activate the strict final contract. Malformed server-controlled result payloads still fail closed instead of being treated as empty.
- An evidence-bearing result remains evidence-bearing when reranking is degraded. Degradation affects quality metadata, not whether returned Evidence addresses are authoritative.
- Runs without selected Sources keep the current bare-text behavior.

## State and request construction

`parseResearchState` remains the single interpreter of durable `search_evidence` Action Results. `evidenceSeen` becomes true only after a structurally valid Evidence range has been parsed. Empty Evidence arrays do not activate it.

Grounded model requests use two final formats:

1. **Optional grounded final** before `evidenceSeen`: tool calling remains available and assistant content may be plain text. If the model voluntarily returns an exact grounded object, the adapter decodes it so JSON is not displayed to the user.
2. **Required grounded final** after `evidenceSeen`: assistant content must decode as the existing typed `FinalDraft` and pass its validation.

The strict request also sends the OpenAI-compatible provider field:

```json
{"response_format":{"type":"json_object"}}
```

The grounded system prompt explicitly describes the transition: ordinary or unsupported questions may finish without Source claims; evidence-bearing searches require the typed claim-and-citation object. The prompt retains the word `JSON`, as required by Qwen JSON mode.

## Grounding and publication

For a selected-Source Run whose durable research state contains no citeable Evidence, a claim-free final is accepted without a second fallback model call. A new grounding-plan outcome, `source_free`, records this branch. It requires:

- at least one selected Source;
- zero declared claims and therefore zero Citations;
- no verifier model or verifier prompt;
- the actual `research_complete` and `retrieval_degraded` flags, including the no-search state.

This outcome is distinct from the existing `source_less` outcome, which remains limited to Runs admitted with zero Sources. Existing historical `zero_support` rows remain publishable for compatibility, but new no-evidence finals use `source_free` and preserve the model's accepted text. `supported` and `insufficient_evidence` behavior does not change.

The publication transaction verifies the new outcome shape, the selected evidence pins, the final-draft hash, and the absence of claims before copying the assistant message. No Citation rows can be produced on this branch.

## Failure behavior

- Plain text before citeable Evidence: accepted.
- Valid claim-free JSON before citeable Evidence: decoded and accepted.
- Malformed JSON-looking text before citeable Evidence: treated as ordinary text, because no structured contract is active.
- Plain text or malformed JSON after citeable Evidence: `model_invalid_response`; nothing is published.
- Valid JSON with invalid claims or addresses after citeable Evidence: rejected by existing draft and grounding validation.
- A tool result with an invalid Evidence address: `grounding_invalid`; it cannot downgrade itself into the source-free path.

## Tests

Implementation proceeds test-first with focused coverage for:

1. Bifrost optional mode accepting plain text and decoding voluntary claim-free JSON without sending `response_format`.
2. Bifrost required mode sending JSON mode and rejecting plain text.
3. Research-state/request-format selection for no search, empty search, degraded empty search, and evidence-bearing search.
4. Grounding acceptance and atomic publication of `source_free` answers with selected Sources and no Evidence.
5. Continued rejection of claim-free or plain finals after Evidence exists.
6. Existing grounded citation verification and bare chat regression suites.

## Non-goals

- Replacing claim-level verification with Open Notebook-style inline reference parsing.
- Treating Source selection as proof that an answer used Source content.
- Relaxing the supported-answer publication barrier.
- Repairing the independent reranker HTTP 400 degradation.

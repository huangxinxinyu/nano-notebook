# Grounded Query Contextualization

## Problem

For a Run with selected Sources, the Composer currently receives the normal bounded
Chat history and is forced to call `search_evidence`. That combines two different
jobs: deciding the retrieval query and answering the user. A long prior grounded
answer can anchor a small model strongly enough that a new request such as `你好` or
`你有哪些工具` produces the previous turn's degree-planner search query and then
repeats the previous answer.

The durable input Message, Run, and checkpoint ownership are correct. The failure
occurs before retrieval: the model-authored Action input is about the old turn.

## Decision

Selected-Source Runs keep the invariant that retrieval is always attempted, but the
first retrieval query is produced by an isolated Query Contextualizer rather than by
the general Composer request.

The Query Contextualizer receives:

- the current immutable user Message as the primary input;
- at most the three most recent completed user/assistant pairs, under a small total
  text budget;
- a dedicated instruction that history may only resolve references or omissions in
  the current Message and must not replace a self-contained current topic;
- only the `search_evidence` Action, required as one specific function call.

It does not receive retrieved Evidence, unrelated Actions, or permission to produce
the final answer. The generated `query` and `purpose` use the existing validated
`search_evidence` input contract and the accepted proposal/result checkpoints.
Therefore recovery resumes the same durable query and never regenerates it after a
checkpoint has been accepted.

If contextualization fails before an Action proposal is accepted, the Controller
uses a bounded form of the current user Message as the deterministic search query.
This fallback preserves always-RAG behavior and cannot resurrect an earlier topic.

Before accepting the proposal, the Controller ensures that the bounded current
Message remains present in the contextualized query. If the model rewrites or
translates away ambiguous wording, the current Message is prepended to the model's
expansion. This deterministic preservation keeps the user's actual search terms
while still allowing the model to append a resolved subject.

After retrieval, the Composer receives only the original current question and the
durable contextualized Action call/result, not prior Chat messages. The Action query
carries any resolved referent needed for a follow-up. Excluding prior answers keeps
the Composer from copying an earlier failure or treating an old answer as the
current task.

This design does not add a chat-versus-RAG router. Every selected-Source Run still
retrieves. It also does not change the plain-text Source-marker citation contract.

## Expected Behavior

| Prior turn | Current Message | Retrieval query behavior |
| --- | --- | --- |
| Degree-planner answer | `你好` | Query remains `你好`; old topic is not reused. |
| Degree-planner answer | `你有哪些工具` | Query remains about the current tool question. |
| Degree-planner answer | `Plan II 的研究课上限呢？` | Query expands the omitted subject using the prior turn. |
| None | Any question | The current Message is used directly or rewritten without invented history. |

Retrieved content can still be empty or irrelevant. In that case the Composer
answers the original request normally without a Source marker. If retrieved content
is used, the existing `[source:<source_id>]` contract applies.

## Persistence and Observability

No new database table or checkpoint kind is required. The contextualized query is
stored in the existing Action proposal checkpoint and its result in the existing
Action result checkpoint. Replay must make the isolated contextualizer request and
decision distinguishable from the later Composer request. Trace attributes record
the number of history pairs supplied and whether deterministic fallback was used;
raw sensitive text remains confined to Replay.

## Error Handling

- Invalid, missing, or wrong Action output from the contextualizer falls back to the
  bounded current Message before any proposal is accepted.
- Retrieval domain errors keep the existing degraded/failed Action semantics.
- Lease, deadline, and checkpoint authority checks remain unchanged.
- A recovered attempt uses accepted checkpoints and must not call the
  contextualizer or retrieval backend twice.

## Verification

- A request-builder test proves a prior Source-backed answer cannot replace a new
  self-contained current Message in the contextualizer input.
- A Controller test proves the accepted search query preserves the bounded current
  Message even when a model-authored expansion changes ambiguous wording.
- Controller tests prove contextualizer failure falls back to the current Message
  and recovery does not duplicate the accepted search.
- The Composer integration test proves prior Chat turns are excluded while the
  current Message and durable search result remain present.
- Integration tests cover a topic switch and a genuine follow-up in one Chat.
- Replay/Trace tests distinguish contextualization from answer composition without
  exposing new plaintext in metadata.
- A live selected-PDF journey sends a Source question, then `你有哪些工具`, then
  `你好`; the latter two responses must address their own current Messages and must
  not repeat the Source answer.

# Source Text Search and Range Read

## Context

The Research executor (`research.executor@38`) reads documents through two
modes only:

- **Semantic retrieval.** `search_evidence` returns reranked top-k chunks,
  optionally scoped to one `source_id` and `entry_id`.
- **Linear paging.** `read_url` returns a preview plus `result_ref` for a long
  page, and `read_tool_result` pages through it by byte offset.

`inspect_source` gives an outline with `entry_id` and page ranges, but the
only thing the model can do with that outline is a scoped top-k search. There
is no step between "guess with retrieval" and "read everything in order":

1. **Exact lookups fail.** Strings such as `Table 3`, `0.873`, `[23]`, a
   variable name, or an acronym are poorly served by Dense + BM25 + rerank.
   The mixed analyzer splits punctuation and numbers, and the relevance gate
   can drop a correct but semantically thin chunk.
2. **Exhaustive questions are unanswerable.** "Where does this term appear?"
   and "how many times?" have no top-k answer.
3. **Sources cannot be read in order.** No tool returns a section or page
   range of a Ready Source as contiguous text.
   `read_document_pages` reads pages only from an `rdoc_` handle. Only
   `research.executor@8` exposes it, and since v9 `read_url` returns
   `pdf_requires_source_import` for PDFs instead of creating a handle.
4. **Long web results are only linear.** Finding one paragraph in an
   externalized `read_url` result means paging from the beginning.

## Decision

Add two tools that work together: a locator and a reader.

```text
inspect_source(source_id)        -> outline: entry_id, page ranges
search_text(pattern, scope)      -> exact hits with anchors
read_source(source_id, locator)  -> contiguous Evidence Units around an anchor
search_evidence(query, ...)      -> unchanged semantic retrieval
```

### Revisiting the 2026-09-05 decision

`2026-09-05-source-scoped-evidence-search-design.md` rejected a raw
`read_source_section` tool because it would "create a second PDF evidence
path and weaken the existing citation boundary". That reasoning predates
claim cards. Today `record_claim` verifies a quote against the concatenated
`source_evidence_units.text_content` of the pinned revision
(`postgresResearchClaimBackend.SourceText`), not against the chunks that
`search_evidence` returned. The citation boundary is therefore "verbatim text
of the pinned Evidence Units", and both new tools return exactly those
units. They add no new evidence representation. They only add new ways to
reach the same units.

The model still never supplies a Notebook ID, Evidence Revision ID, chunk ID,
object key, or parser policy. Results echo `evidence_revision_id`, as
`inspect_source` and `search_evidence` already do, so claim and grounding
bookkeeping can name the pinned revision. A page range is a filter inside an already
authorized, pinned revision, validated against the Source Map's `page_count`.

## `search_text`

### Input

```json
{
  "pattern": "Table 3",
  "regex": false,
  "case_sensitive": false,
  "source_id": "src_123",
  "result_ref": "tr_abc..."
}
```

- `pattern` is required, 1–256 runes.
- `regex` defaults to `false`. When it is `false`, the pattern is a literal
  (`regexp.QuoteMeta`). When it is `true`, the pattern is compiled with Go
  `regexp`. RE2 runs in linear time, so model-supplied patterns cannot cause
  catastrophic backtracking. A compile error returns `invalid_pattern`.
- `case_sensitive` defaults to `false` and adds `(?i)`.
- Scope sets at most one of `source_id` and `result_ref`:
  - `source_id`: one Source in the Run's pinned Evidence Set.
  - `result_ref`: one externalized Tool Result of this Run that is still live.
    Only `read_url` results are searchable.
  - Neither: every Source in the pinned Evidence Set. This answers "which
    paper mentions X?"

### Matching

- Source scope matches **per Evidence Unit** over NFC-normalized
  `text_content`, in `ordinal` order. A match that spans two units is not
  found; the result contract states this.
- `result_ref` scope matches over the full externalized body.
- Units are already in memory, so the search always scans everything in scope.
  `total_matches` is exact. `truncated` is true when fewer matches are shown
  than exist.

### Output

```json
{
  "scope": {"source_id": "src_123"},
  "total_matches": 7,
  "truncated": false,
  "matches": [
    {
      "source_id": "src_123",
      "unit_id": "eu_...",
      "page": 6,
      "entry_id": "entry_results",
      "snippet": "... as shown in «Table 3», the ablation removes ..."
    }
  ],
  "sources": [
    {"source_id": "src_123", "evidence_revision_id": "evr_...", "matches": 7}
  ]
}
```

`sources` lists each Source with at least one match. It is what claim cards
and chat grounding read.

For `result_ref` scope, each match has `offset` instead of a unit anchor. The
offset is the byte offset of the start of the line containing the match, so it
can be passed directly to `read_tool_result`.

Limits:

- at most 30 matches;
- a snippet of ±160 runes around the match, with the match marked `«…»`;
- at most 3 matches per unit (the rest are counted only);
- 8 KiB total output for Research and 6 KiB for chat. Matches are dropped from
  the end until the output fits.

`entry_id` is the innermost Source Map entry whose page range contains the
match page. It is omitted when the Source has no Source Map.

## `read_source`

### Input

`source_id` is required, plus exactly one locator:

```json
{"source_id": "src_123", "entry_id": "entry_results"}
{"source_id": "src_123", "page_start": 6, "page_end": 8}
{"source_id": "src_123", "unit_id": "eu_...", "before": 2, "after": 6}
```

- `entry_id` resolves through the same Source Map path as scoped
  `search_evidence` (`resolveEntrySearchScope`).
- A page range is valid inside `[1, page_count]` and spans at most 10 pages.
  It is available only when units carry `pdf_region` coordinates. Otherwise
  the tool returns `page_range_unsupported`.
- `unit_id` must belong to the pinned revision. `before` and `after` default
  to 2 and 6, each at most 40. This locator works for every media type and is
  the follow-up to a `search_text` hit.

### Output

The tool returns units in `ordinal` order. Each unit carries `unit_id`,
`kind`, `page` (when it has a PDF coordinate), and its stored `text`
verbatim.

- The output is capped at 24 KiB for Research and 8 KiB for chat. It always
  ends on a unit boundary and never cuts a unit in half. A single unit longer
  than the cap is truncated with `truncated: true` on that unit.
- When more units remain, the result includes `next`. `next` is the original
  input plus `from_unit_id`, so passing it back resumes the same entry, page
  range, or window. A bare `unit_id` continuation would lose the original
  range.

The result is bounded by design, so it does not opt into
`CacheLongToolResults`.

## Authorization

Both tools reuse `EvidenceSearchService.loadPinnedScope` under the live
Attempt lease. They resolve `source_id` only inside the pinned set and load
units with `source_id`, `revision_id`, and `notebook_id` all pinned.

An absent, foreign, unpinned, stale, or mismatched `source_id`, `entry_id`,
or `unit_id` returns the same `evidence_scope_unavailable` error. The response
never reveals whether the target exists elsewhere.

`result_ref` scope reuses the `ToolResultReader` scope check (user, chat,
run). It returns `tool_result_expired` and `tool_result_unauthorized` exactly
as `read_tool_result` does.

## Claim cards

`searchedResearchSourceEvidence` decides which Sources `record_claim` can
verify. It currently counts only `search_evidence` results. Rename it to
`claimableResearchSourceEvidence` and also count:

- `read_source` results (the returned `source_id` and revision); and
- `search_text` results with at least one match, per matched `source_id`.

URL claims need no change. `readURLSourceText` already hydrates the full
externalized `read_url` body, so a quote found through `result_ref` grep
verifies the same way.

Update the `record_claim` description to "a `search_evidence`,
`search_text`, or `read_source` source_id". Also update the
`source_unavailable` note.

## Rollout

- Publish `research.executor@39` with both tools, `agent.deep-research-executor@16`,
  and `skill.source-reading@2`. Release `nano.default@52` pins it together
  with `chat.leader@7`. Update the workflow guidance:
  - Use `search_text` for exact strings, numbers, labels, and "every
    occurrence" questions.
  - Use `read_source` to read a section or the context around a hit before
    recording a claim.
  - Use `search_evidence` for concepts.
- Runtime subagents (readers) inherit the executor tool set, so they get the
  same access.
- Both tools are `ToolParallel` and `CrashReplaySafe`. Add them to the
  read-only capability set in `executor_registry.go`.
- Map public activity in `public_activity.go` onto existing kinds:
  `search_text` maps to `searching_sources` and `read_source` maps to
  `inspecting_source`. The web client needs no new labels.

### Open decision: chat.leader

`chat.leader@6` has only `search_evidence`, and no `inspect_source`. Exact
lookups ("what does the paper report in Table 2?") are common in chat. Two
options:

- **A (chosen):** Ship `chat.leader@7` with `search_text` and `read_source`,
  so chat can follow a hit to its context without the outline workflow. Chat
  gets the smaller output caps above. `read_source` accepts every locator, but
  chat has no `inspect_source`, so in practice it uses `unit_id` from
  `search_text` and page ranges.
- **B:** Keep chat unchanged until Research usage shows the tools pay off.

Chat grounding cites at Source level (`[source:<id>]`). `parseResearchState`
counts `read_source` and matching `search_text` Sources as evidence, so their
IDs are valid markers. `agent.chat-composer-grounded@6` says so.

Studio agents stay unchanged. Their structured-output loop is single-shot
retrieval.

### `read_document_pages`

Only `research.executor@8` lists it, and it cannot receive a handle on v9+.
Immutable v8 Runs may still replay, so keep the Action registered. Mark it
legacy in code comments, and delete it in a separate change once no v8 Run
can resume.

## Testing

- Schema tests:
  - `search_text` for the literal, regex, invalid-regex, both-scopes, and
    no-scope cases;
  - `read_source` for each locator, more than one locator, a page range
    spanning more than 10 pages, and a page range on a non-PDF Source.
- Matching tests:
  - literal metacharacters (`[23]`, `0.873`, `a+b`);
  - case folding;
  - NFC normalization;
  - the per-unit cap;
  - the match limit with `total_matches_lower_bound`;
  - snippet boundaries at unit start and end.
- `result_ref` tests:
  - the returned `offset` round-trips through `read_tool_result` to a page
    that contains the match;
  - expired and unauthorized refs.
- `read_source` tests:
  - ordinal order;
  - unit-boundary truncation;
  - the `next` continuation reaching the end of the range;
  - oversized single unit;
  - entry resolution matches scoped `search_evidence` page overlap.
- Authorization tests: foreign, unpinned, stale-revision, and other-Source
  `unit_id` all return `evidence_scope_unavailable`.
- Claim tests: a Source reached only through `read_source` or `search_text`
  yields `verified` cards. A Source with zero `search_text` matches stays
  `source_unavailable`.
- Catalog tests for `research.executor@39` and its release pin.
- An `agent-eval` slice of exact-lookup questions (table values, reference
  numbers, term counts) comparing v38 and v39 on answer accuracy, tool calls,
  and input tokens.

## Non-goals

- Cross-unit or cross-page matching.
- Fuzzy matching or edit distance. `search_evidence` covers approximate
  meaning.
- Grepping `research_*` workspace files. `read_research_file` already
  returns them whole.
- Exposing original PDF bytes, object keys, or chunk IDs.
- Changing indexing, chunking, reranking, or the relevance gate.

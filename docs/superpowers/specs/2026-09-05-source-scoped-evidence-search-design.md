# Source-scoped Evidence Search

## Context

`inspect_source(source_id)` already gives the Research executor a bounded PDF
outline with stable `entry_id` values, headings, page ranges, and representative
previews. `search_evidence`, however, accepts only a semantic `query` and
`purpose`, so it searches every Source pinned to the Run. Mentioning a paper or
section in the query is only a relevance hint; it is not an authoritative
scope.

Research needs an executable equivalent of an offset directory. The model
should be able to inspect a paper, select its Conclusion entry, and search only
the corresponding authoritative Evidence without guessing Redis byte offsets
or receiving object-storage keys.

## Decision

Extend `search_evidence` with two optional locators:

```json
{
  "query": "main conclusions, limitations, and future work",
  "purpose": "summarize the target paper's conclusion",
  "source_id": "src_123",
  "entry_id": "entry_conclusion"
}
```

- `query` remains required and controls semantic relevance.
- `purpose` remains required and records research intent.
- `source_id`, when present, is a hard restriction to one Source in the Run's
  pinned Evidence Set.
- `entry_id`, when present, is a hard restriction to one entry in that
  Source's immutable Source Map. `entry_id` requires `source_id`.

The mental model is:

```text
source_id: which document
entry_id: which document region
query: what evidence within that region
```

The existing unscoped input remains valid and retains its current behavior.

## Alternatives considered

1. Put the paper title and section name into `query`. This requires no API
   change, but cannot guarantee that results come from the intended Source.
2. Add a raw `read_source_section` Tool. This provides direct navigation but
   creates a second PDF evidence path and weakens the existing citation
   boundary.
3. Add optional authoritative scope to `search_evidence`. This preserves one
   evidence and citation path while making `inspect_source` navigation
   executable. This is the selected approach.

## Authority and resolution

The model never supplies a Notebook ID, Evidence Revision ID, page range,
object key, parser policy, or index version.

For every scoped request, the service must:

1. load the existing Run-pinned Evidence Set under the live Attempt lease;
2. resolve `source_id` only inside that set;
3. retain the pinned active Evidence Revision and verified Index Version;
4. when `entry_id` is present, load the immutable Source Map associated with
   that Source and revision;
5. resolve the entry's server-owned page range; and
6. derive the allowed Evidence Units and Retrieval Chunks from authoritative
   PostgreSQL rows and the pinned chunk configuration.

An absent, unauthorized, stale, or mismatched locator returns the same stable
`evidence_scope_unavailable` domain error. The response must not reveal whether
an out-of-scope Source or entry exists elsewhere.

## Retrieval behavior

Source filtering must happen before Dense/BM25 candidate limits and RRF:

- a `source_id` scope narrows the Qdrant `EvidenceRef` set to the pinned
  Source/revision pair;
- an `entry_id` scope resolves an allowed chunk-ID set from Evidence Units
  whose PDF page coordinates overlap the entry's page range;
- Dense and sparse retrieval both apply the same allowed scope before their
  top-k limits;
- PostgreSQL reload rechecks Source, revision, unit, page, and chunk authority
  before reranking and projection.

A chunk that overlaps the entry boundary is eligible when at least one of its
authoritative Unit references has a PDF coordinate inside the resolved page
range. Its returned preview and coordinates keep the existing bounded result
contract. No Source Map preview becomes citable merely because it supplied the
navigation scope.

If the Source Map uses low-confidence `page_samples`, its entries remain valid
page-range locators. The result does not claim a semantic section name beyond
the stored entry heading and confidence metadata.

## Tool and result contracts

The `search_evidence` schema adds:

```json
{
  "source_id": {"type": "string", "minLength": 1, "maxLength": 128},
  "entry_id": {"type": "string", "minLength": 1, "maxLength": 128}
}
```

Input validation rejects `entry_id` without `source_id`. The Action output
adds a compact optional scope echo so the model can verify what was enforced:

```json
{
  "scope": {
    "source_id": "src_123",
    "entry_id": "entry_conclusion",
    "page_start": 14,
    "page_end": 16
  }
}
```

The scope echo contains resolved public identities and page coordinates only;
it never contains object keys or internal storage paths.

The `inspect_source` result shape does not change. Its existing `source_id`,
`entry_id`, `heading`, `page_start`, and `page_end` fields become valid inputs
to the scoped search workflow. The Research workflow guidance should explicitly
teach this sequence:

```text
inspect_source(source_id)
-> select entry_id
-> search_evidence(query, purpose, source_id, entry_id)
```

## Compatibility and versioning

This is a backward-compatible input extension, but Research behavior and tool
guidance change. Publish a new immutable `research.executor` Definition and a
new release pin rather than mutating version 16. Older pinned Runs retain the
two-field `search_evidence` contract and unscoped retrieval behavior.

## Testing

Acceptance requires:

- Action-schema tests for unscoped, Source-scoped, entry-scoped, malformed,
  and `entry_id`-without-`source_id` inputs;
- service tests proving Source filtering occurs before Dense and sparse top-k;
- service tests proving an entry resolves only chunks overlapping its
  authoritative page range;
- authorization tests proving foreign, unpinned, stale, and mismatched
  locators all return `evidence_scope_unavailable` without existence leakage;
- regression tests proving the existing unscoped search result is unchanged;
- projection tests for the compact resolved scope echo;
- catalog tests for the new immutable executor/release pin; and
- an integration path from `inspect_source` Conclusion entry to scoped,
  citable `search_evidence` output.

## Non-goals

- Redis offset maps or changes to `read_tool_result`.
- Exposing original PDF bytes, normalized Markdown, or object-store paths.
- Making `inspect_source` previews independently citable.
- Letting the model choose arbitrary page ranges, revision IDs, chunk IDs, or
  parser behavior.
- Changing Source readiness, admission, indexing, or citation validation.

# Trace Detail Empty-Collection Contract

## Problem

Collector Trace Detail responses currently serialize some empty Go slices as JSON
`null`. The Dashboard treats `spans`, `events`, `links`, Span attributes, and Span
Replay references as arrays. A real Trace with no Links therefore crashes Timeline
rendering at `links.map`, leaving the detail route blank.

## Design

Collector Query is the canonical response boundary and must encode every repeated
Trace Detail field as a JSON array, using `[]` when empty. The Dashboard also
normalizes nullable repeated fields after decoding so an older Collector response or
partially migrated deployment cannot crash the page.

The fix does not change Trace Tree, Timeline, Inspector, or Replay interaction. It
only stabilizes their input contract.

## Error Handling

Missing collections degrade to empty collections. Missing required scalar identity,
summary, or projection fields continue through the existing unavailable/error path;
the normalizer does not fabricate Trace records or Span relationships.

## Verification

- A Collector HTTP integration test asserts empty repeated fields encode as `[]`.
- A Web regression uses the observed production-shaped `links: null` and nullable
  Span collections and proves Tree, Timeline, Attributes, and Replay remain usable.
- The real Trace Detail payload that previously produced a blank body is replayed in
  headless Chromium and must render without page errors.

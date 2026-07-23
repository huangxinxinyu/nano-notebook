# Source-less Grounding Hash Canonicalization

## Problem

A Run without selected Sources reaches the model and accepts its Final Draft in a
few seconds, but publication fails with `grounding evidence or citation is
invalid`. The worker leaves the Run recoverable, its lease expires after 30
seconds, and another attempt repeats the same deterministic failure. The user sees
35–60 seconds of apparent latency even though retrieval was never selected.

The source-less grounding plan hashes a `nil` reference slice as
`source_references: null`. Publication reloads zero reference rows into a non-nil
empty slice and hashes it as `source_references: []`. These values have the same
domain meaning but different bytes and therefore different SHA-256 values.

## Decision

`sourceGroundingPlanSHA256` is the canonical boundary for grounding-plan identity.
Before JSON encoding, any reference slice with length zero is normalized to `nil`.
Both `nil` and an allocated empty slice therefore encode as
`source_references: null` and produce the same hash.

Canonicalizing to `nil` rather than an empty array preserves compatibility with
source-less plan hashes already stored by the preparation path. No table, payload,
checkpoint, citation, or selected-Source behavior changes.

## Error and Recovery Behavior

Publication continues to reject genuine draft, outcome, research-state, or Source
reference mismatches. Only the representational difference between the two empty
slice forms is removed. A previously accepted source-less Final Draft can be
published on the same attempt or recovered by a later authoritative attempt
without invoking the model again.

## Verification

- A unit regression proves `nil` and allocated-empty reference slices produce the
  same grounding-plan hash, while a non-empty reference still produces a distinct
  hash.
- A PostgreSQL integration regression executes a real Controller Run with zero
  selected Sources and the configured Grounding Service, then proves the Run and
  Job complete, the Assistant Message is published, and only one model decision is
  needed.
- The full Agent package and relevant application integration tests remain green.

## Scope

This fix does not change retrieval routing, query contextualization, Source marker
parsing, Provider selection, lease duration, or retry policy.

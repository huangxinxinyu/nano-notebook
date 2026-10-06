---
identity: skill.source-reading
version: 2
name: Source Reading
description: Use when reading a page or paper for the report — how to pull out claim cards with verbatim quotes, their conditions, and how to cite them so claims stay attributable.
---
# Source Reading

Read a source to answer the plan's questions, not to summarize it. As you read, take a claim card with `record_claim` for each fact you may rely on in the report: a number, an experimental result, a design decision, an author's stated limitation, a date, or a capability.

## Finding the passage in a Notebook Source

- `search_evidence` finds passages about a concept.
- `search_text` finds exact wording: a number, a name, a table or figure label, a reference marker such as `[23]`. It also counts every occurrence. Each hit has a `unit_id`.
- `read_source` reads in order: a section by `entry_id` from `inspect_source`, up to 10 pages, or the units around a `unit_id`. Read around a hit before quoting it, so the card carries the sentence's real context and conditions.

## A good card

- `source`: the exact URL you read with `read_url`, or the Notebook `source_id` returned by `search_evidence`, `search_text`, or `read_source`.
- `quote`: copy the wording from the source text you were shown. Keep it to the sentence or two that carries the fact; use `...` to skip an irrelevant middle part. Do not translate, paraphrase, or tidy it.
- `claim`: what the quote establishes, in the report's language, no stronger than the quote.
- `conditions`: the setting that bounds it — model and version, benchmark, dataset, hardware, threat model, date, or "author claim, not independently measured".

One card holds one fact from one source. When two sources support the same point, take a card from each.

## Attribution checks while reading

- Name the paper or product the fact belongs to. In a comparison, a result table or defense from one work must never be credited to another.
- Distinguish what the authors measured from what they claim or speculate. Put "author claim" in `conditions` when there is no measurement.
- Note the evaluation setting; a result on one benchmark or model does not transfer to others.

## When a card comes back unverified

`near_match` or `not_found` means the quote does not appear in the text the Run actually read. Compare with `nearest_excerpt`. If the source does say it, record a corrected card with the real wording. If it does not, drop the claim or mark it as inference in the report. `source_unavailable` means you have not read that source in this Run yet.

## Using cards in the report

Read `claims.md` before drafting. Cite cards inline at the sentence they support, like `……提升了 12 个百分点 [c4]` or `[c4, c7]`; the published report turns them into numbered source links. Write your own inference and synthesis without a card, and make it read as inference. Direct Markdown links to read pages remain allowed.

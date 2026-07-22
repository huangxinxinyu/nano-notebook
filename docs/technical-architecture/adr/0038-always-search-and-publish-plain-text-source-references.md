# Always search and publish plain-text Source references

A Run with selected Sources must durably attempt the specific `search_evidence` Action before it may finalize. After that attempt, the Composer always returns ordinary text. It may declare use of retrieved material with `[source:<source_id>]`, or omit markers when the retrieved material was empty, irrelevant, or unused. The server retains only markers for Sources represented by structurally valid Evidence in accepted results, removes invalid markers, and publishes ordered Source-level Citations through the existing transactional authority and deletion fence.

New Runs do not create model-authored claims, require duplicate substring matching, enable provider JSON mode for conversational finals, or invoke a runtime Claim Support Verifier. A valid Source reference establishes retrieval provenance and current authorization, not sentence-level entailment or an exact Evidence coordinate. Historical precise Citations and claim-support records remain readable. Offline Eval replaces claim coverage and Evidence-range citation precision with Source-reference precision while retaining retrieval, answer-quality, forbidden-fact, latency, cost, and publication-invariant gates.

This decision supersedes ADR 0033, ADR 0035, and ADR 0037 for new Runs.

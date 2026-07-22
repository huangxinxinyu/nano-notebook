# Switch the final contract when citeable Evidence exists

A Run with selected Sources may publish a claim-free `source_free` Answer while no accepted `search_evidence` result contains a valid Evidence range. This includes ordinary conversational requests that do not search and searches that return no citeable Evidence, whether complete or degraded. The Answer carries no Citations and cannot claim that selected Sources support it.

Once any accepted search result contains a valid Evidence range, the Composer's final response must use provider-enforced grounded JSON. Every material claim then requires retrieved Evidence addresses and continues through deterministic address validation, independent support verification, and the Publication Barrier. Degraded retrieval that returns Evidence remains on this strict path. A later empty result cannot downgrade the Run to `source_free`, and partially supported Answers cannot fill gaps with model knowledge.

This decision supersedes the zero-support fallback portion of ADR 0033 and all of ADR 0035: complete, non-degraded zero-support research and a fresh fallback Model Call are no longer required. Historical `zero_support` grounding rows remain valid for publication compatibility, but new no-Evidence Runs persist `source_free` with their actual research-complete and retrieval-degraded flags.

---
identity: skill.research-workflow
version: 2
name: Research Workflow
description: Use when executing an accepted multi-phase Research plan and coordinating evidence, gaps, verification, and report readiness.
---
# Research Workflow

Use the checkpoint-backed TODO as a concise control surface for the accepted Research Plan. It is not memory, evidence storage, or report content.

At the start of execution, call `rewrite_todo_list` once when the current TODO does not already represent the plan. Create short items for meaningful stages such as key questions, Source readiness, evidence gaps, section drafting, and citation verification. Keep only one item in progress unless independent work is genuinely concurrent.

At a meaningful phase boundary, call `update_todo_status` to complete the finished item and start the next one. Update the list when a material contradiction or evidence gap changes the remaining work. Do not churn TODO state after every search or read.

TODO items may contain stage names, research questions, readiness checks, unresolved gaps, report sections, and citation-verification work. Never put excerpts, search-result text, URL lists, raw Tool output, Source Map bodies, report prose, credentials, or hidden reasoning in TODO.

Treat the accepted Research Plan as immutable. TODO refines execution state; it does not authorize scope expansion or plan mutation. Source readiness, evidence eligibility, citation validity, final-report barriers, and context compaction remain Harness-enforced rules.

`inspect_source` is navigation only; claims still require citable Evidence from `search_evidence`. When an inspection identifies a useful paper region, copy the returned `source_id` and `entry_id` into the next `search_evidence` call while keeping a focused semantic `query` and honest `purpose`. The Harness resolves those locators against the Run-pinned Source and immutable Source Map. Do not invent IDs, page ranges, object paths, or offsets. If no exact region is needed, omit `entry_id`; if no exact Source is needed, omit both locator fields.

Before returning Final, ensure required plan stages are complete, material gaps are either resolved or explicitly represented, and citation verification is complete. Do not mark work complete merely because an Action succeeded.

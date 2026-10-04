---
identity: agent.deep-research-task-memory-compactor
version: 3
contract: nano.research-task-memory.v1
---
Create one deep Research Task Memory for the exact supplied archival range.

Return only one strict JSON object. Do not use Markdown fences or explanatory prose. The object must contain exactly these fields:

`{"schema_version":"nano.research-task-memory@1","first_decision_no":1,"last_decision_no":2,"start_checkpoint_seq":1,"end_checkpoint_seq":4,"goal":"Non-empty research goal","phase":"Non-empty current phase","conclusions":[],"decisions":[],"constraints":[],"durable_refs":[],"contradictions":[],"failed_paths":[],"verification":[],"report_state":[],"follow_up":[]}`

Copy `"first_decision_no"`, `"last_decision_no"`, `"start_checkpoint_seq"`, and `"end_checkpoint_seq"` exactly from the supplied archival range. `"goal"` and `"phase"` must both be non-empty, trimmed strings grounded in the accepted plan and current phase. Use only the field names shown above. Every array value must be an array of strings, never objects. Include every array field even when it is empty.

Preserve the research goal and phase, qualified conclusions, decisions and reasons, constraints, contradictions, material failed paths, verification and report-section state, durable references, and unresolved follow-up.

Do not copy raw excerpts, long Tool output, logs, repeated URL lists, report prose, TODO lists, Agent Status, or hidden reasoning.

# Bound the Durable Runtime to product Jobs

The PostgreSQL Job Runtime implements leases, heartbeats, at-least-once attempts, retry/backoff, cancellation, checkpoints, concurrency control, and crash recovery only for registered Nano Notebook Job kinds. A separate Agent Controller owns bounded model/Action iteration; the system excludes a general Workflow SDK, arbitrary DAGs, deterministic code replay, multi-Agent execution, and exactly-once claims.

Sprint 3 retains one Agent Job for an entire Agent Run and discovers application-defined Actions through an immutable startup Registry rather than Controller conditionals. The Registry supports later built-in Actions through code registration while excluding dynamic plugins, MCP discovery, user installation, and a reusable Action SDK.

The Controller accepts Provider-neutral Model Decisions and persists only append-only Action Proposal, Action Result, and Final Draft Checkpoints. These Checkpoints are runtime authority for first-incomplete-step recovery, remain distinct from Durable Agent Trace, and use fenced idempotent writes; Provider responses and read-only Action executions lost before Checkpoint acceptance may be repeated.

Every Run has bounded model, Action, result-size, and absolute-time budgets. Expected Action domain errors are durable Results that the model may repair, while cancellation, lease loss, and infrastructure failures retain their runtime semantics. Checkpoint recovery applies only to infrastructure interruption within the same active Run: user Stop is terminal, and Retry creates a new Run with no inherited Checkpoints.

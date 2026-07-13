# Build a bounded PostgreSQL-backed Durable Runtime

Nano Notebook will implement its Source and Agent Job runtime over PostgreSQL and independent Go Workers instead of introducing Temporal, Redis, or SQS. This intentionally exposes the Agent-infrastructure behaviors the project exists to study while keeping local operation small; it is bounded to product Jobs rather than becoming a general workflow engine, and an external queue or orchestrator is introduced only from measured contention, throughput, or workflow-complexity evidence.

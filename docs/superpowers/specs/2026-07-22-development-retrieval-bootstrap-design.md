# Development retrieval bootstrap design

## Outcome

A clean local Nano Notebook database starts with one usable baseline Retrieval Index Version, so the first uploaded Source can complete extraction, projection, and verification. The bootstrap is explicitly local-development behavior. Environments configured for deployment readiness never auto-promote an index and refuse to start a Source worker without active retrieval authority.

The repair also makes runtime loss of retrieval authority terminal and explicit. A Source must not wait for three lease expirations and surface `processing_interrupted` when projection cannot begin because no active Retrieval Index Version exists.

## Bootstrap mode

The worker owns a two-value `NANO_RETRIEVAL_BOOTSTRAP_MODE` setting:

- `development` is the repository-local default. When no Retrieval Index Version rows exist, worker startup creates `riv_dev_baseline_v1` as active from the pinned configuration at `evals/rag/pinned-config-v1.json`. When an active version already exists, startup is a no-op. When version rows exist but none is active, startup fails instead of promoting an in-progress or rejected candidate.
- `required` never creates or promotes a version. Worker startup requires an existing active Retrieval Index Version and fails with an actionable error when it is absent.

The explicit `required` setting is the deployment contract. Existing local defaults already select development-only credentials and services, so a development bootstrap default is consistent with the repository's current local-first runtime. Production launch configuration must select `required` alongside its other launch gates.

## Baseline provenance and invariants

The baseline uses the immutable pinned `index` configuration from `evals/rag/pinned-config-v1.json`; worker configuration does not duplicate chunking, analyzer, embedding, fusion, or reranking constants.

Bootstrap runs under the worker database role in one transaction protected by the existing retrieval-promotion advisory lock. It:

1. returns the current active version without mutation when one exists;
2. refuses to act when any version history exists without an active version;
3. inserts exactly one active `riv_dev_baseline_v1` row for an empty version history;
4. records `dev-bootstrap-v1` as provenance so the bypass is visible and cannot be confused with a normal Eval Run identifier.

The development baseline is not an evaluated production release. A later passing candidate promotion retires it through the existing promotion transaction. No Qdrant vector data is manufactured during bootstrap; ordinary Source projection builds vectors against the active baseline.

## Source failure behavior

`sourceprojection.Projection.Build` translates absence of an active Retrieval Index Version into a typed Source-processing prerequisite error. The Source processor treats that error as terminal, marks the Job and Source failed immediately, and exposes a safe `retrieval_unavailable` failure reason.

The member-facing message states that search indexing is unavailable and that an administrator must configure it. Provider timeouts and infrastructure errors keep their existing retry-by-lease behavior; this change only classifies the deterministic missing-authority condition as terminal.

## Startup and recovery flow

The worker performs retrieval bootstrap/validation after opening PostgreSQL and before starting any Source processing goroutine. Therefore no new lease can be claimed before retrieval authority is available.

For the currently stranded local Source, restarting the updated worker creates the baseline and the normal retry action can resume from its persisted `segmenting` checkpoint. The implementation does not directly mutate that user Source during migration. After deployment, the existing retry endpoint remains the explicit recovery command for already failed Sources.

## Testing

Tests are introduced before implementation and cover:

- development bootstrap creates the pinned baseline on empty version history;
- repeated bootstrap is idempotent when active authority exists;
- development bootstrap refuses ambiguous candidate-only history;
- required mode rejects missing active authority and accepts an existing active version;
- worker config defaults to development and validates the two allowed modes;
- projection maps missing active authority to the typed prerequisite error;
- Source processing fails the lease immediately with `retrieval_unavailable` rather than leaving it running for lease expiry;
- a fresh-database integration path can bootstrap and complete a text Source through ready state with controlled model/vector adapters.

Focused package tests run first, followed by the full Go test suite. A live local verification restarts the worker, retries the affected PDF, and confirms that it advances beyond `segmenting` or reports an independent provider error without returning to `retry_exhausted`.

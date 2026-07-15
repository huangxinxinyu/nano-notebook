# Use a standalone Bifrost Model Gateway

Nano Notebook Workers will call a separately versioned Bifrost Gateway for provider protocol normalization, streaming transport, bounded call-level retries, and provider fallback rather than embedding provider SDKs or the Bifrost SDK in each process. Bifrost starts locally in Docker Compose with file-based configuration and environment-provided secrets; its UI, config database, semantic cache, Agent features, and durable workflow ownership remain disabled because Nano Notebook owns routing intent, Agent state, retrieval, and durable recovery.

Agent Job recovery does not retry a model call after Bifrost returns a terminal failure. It re-executes the current single-call Agent Job only when its Worker disappears and the lease expires without a terminal outcome. This keeps Provider retry policy inside Bifrost instead of multiplying it across gateway and Job attempts; after an explicit model failure, a product retry creates a new Agent Run.

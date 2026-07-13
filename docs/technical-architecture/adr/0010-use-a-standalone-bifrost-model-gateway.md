# Use a standalone Bifrost Model Gateway

Nano Notebook Workers will call a separately versioned Bifrost Gateway for provider protocol normalization, streaming transport, bounded call-level retries, and provider fallback rather than embedding provider SDKs or the Bifrost SDK in each process. Bifrost starts locally in Docker Compose with file-based configuration and environment-provided secrets; its UI, config database, semantic cache, Agent features, and durable workflow ownership remain disabled because Nano Notebook owns routing intent, Agent state, retrieval, and durable recovery.

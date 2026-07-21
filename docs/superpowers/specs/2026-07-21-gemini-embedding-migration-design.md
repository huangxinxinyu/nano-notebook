# Gemini Embedding migration design

## Outcome

Nano Notebook will replace its placeholder dense retrieval model with Gemini Embedding 2 at 768 dimensions. Gemini remains behind the standalone Bifrost gateway, Retrieval owns versioned embedding semantics, and a new Qdrant collection is built before any candidate can be evaluated or promoted.

This change does not alter the generation Provider. Aliyun/Qwen continues to serve Agent generation, reranking, composition, and verification unless their independently versioned configurations say otherwise.

## Provider boundary and credential

Bifrost remains the only Provider protocol boundary. Its repository configuration will add Provider `gemini` with key value `env.GEMINI_API_KEY` and an allowlist for `gemini-embedding-2`. Product code continues to call Bifrost's OpenAI-compatible `/v1/embeddings` endpoint with gateway model ID `gemini/gemini-embedding-2` and `dimensions: 768`.

The real key is developer-local configuration in ignored `infra/compose/.env`. It must never appear in a tracked file, command output, application log, durable trace, Eval artifact, or chat message. Missing credentials may disable the opt-in live smoke but must not make deterministic tests depend on the public Provider.

## Versioned embedding semantics

The Index Version configuration will identify both the embedding model/dimensions and an embedding input profile named `gemini-retrieval-v1`. This profile makes the asymmetric retrieval formatting explicit and reproducible:

- Query input: `task: search result | query: {query}`
- Document input with title: `title: {title} | text: {chunk}`
- Document input without title: `title: none | text: {chunk}`

Whitespace normalization and title selection must be deterministic. The application applies this formatting before calling Bifrost because Gemini Embedding 2 does not accept the older `task_type` parameter for retrieval. Unknown profile IDs fail configuration validation rather than silently falling back.

The model, dimensions, and profile are part of immutable Index Version identity. Changing any of them requires a fresh candidate build and cannot mutate an existing version.

## Batch compatibility

Gemini Embedding 2 can aggregate multiple parts into one vector unless individual inputs are represented as separate contents or sent through an appropriate batch endpoint. Before enabling production projection batching, a controlled Bifrost `v1.6.3` contract test must prove that an OpenAI-compatible array of N text inputs returns N ordered vectors.

If the locked gateway does not preserve that contract, Nano Notebook will initially issue bounded single-input embedding calls through the same capability adapter. Upgrading Bifrost is a separate, explicitly verified change; application code must never accept one aggregate vector for multiple chunks.

## Qdrant migration

Qdrant collection dimensions are immutable for the stored vector schema. The existing `nano-source-evidence` collection remains untouched. The new default collection will have a versioned name that identifies Gemini and 768 dimensions, and the worker must verify its vector size before writes or queries.

All ready Sources with active Evidence Revisions are projected into the new candidate collection. A candidate is eligible for promotion only after every required Source build is verified. Rollback selects the previous promoted Index Version and collection; it does not convert vectors between model spaces.

## Evaluation and release gate

The frozen Sprint 6 Eval candidate configuration will pin:

- `embedding_model: gemini/gemini-embedding-2`
- `embedding_dimensions: 768`
- `embedding_profile_id: gemini-retrieval-v1`
- the new Qdrant collection through the Eval invocation/runbook

Deterministic tests use controlled Bifrost-compatible upstreams. After the developer supplies the local key, one bounded live smoke embeds a known query and document, checks vector count and dimensionality, and performs no content logging. The candidate then runs through the existing product-path offline Eval gates for retrieval, citations, grounding, latency, and known cost. Promotion remains prohibited when any existing invariant fails.

## Failure behavior

- Missing or rejected Gemini credentials fail the embedding call safely and do not fall back to a different vector space.
- A vector count or dimension mismatch fails the projection/query before Qdrant mutation.
- An unknown embedding profile fails Index Version validation.
- The old collection is never automatically deleted.
- Provider errors remain bounded by Bifrost retry policy; Source Jobs do not multiply explicit terminal Provider failures.

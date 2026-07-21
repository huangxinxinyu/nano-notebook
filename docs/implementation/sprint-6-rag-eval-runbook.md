# Sprint 6 RAG offline evaluation runbook

The promotion-authorizing path is `rag-eval -live-product-sources`. It uses the frozen Suite and pinned configuration, builds the candidate projection, runs each Case through the production Agent Controller, `search_evidence`, grounding verifier, publication barrier, and Citation tables, then promotes only a passing candidate.

Precomputed `-observations` remain useful for evaluator development, but the CLI rejects them when an Eval Run would be recorded or a Retrieval Index Version promoted. `-product-runs` audits already completed product Runs without executing new ones and cannot authorize promotion. `-executor-command` remains the bounded CI adapter for a trusted, separately packaged product runner.

## Prerequisites

- PostgreSQL, Qdrant, Bifrost, Source workers, object storage, and the document renderer are ready.
- `GEMINI_API_KEY` is configured in ignored `infra/compose/.env`, and Bifrost has compliant network egress from a Gemini API supported region.
- Every Suite fixture has been admitted through the normal Source API and reached `ready`.
- The admitted Source `content_sha256` equals the SHA frozen in `evals/rag/sprint6-v1.json`.
- The Eval principal is a member of the fixture Notebook.
- Image and audio fixtures use the configured vision and transcription model paths; PDF and PPTX use the isolated renderer.

`internal/rageval.ResolveFixture` is the repository authority for every `fixture://sprint6/...` payload. Provisioning code should resolve those bytes and upload or snapshot them through the same Source interfaces used by the product; do not insert normalized Evidence directly.

## Live Source manifest

The manifest connects immutable Suite fixture identities and semantic golden Evidence labels to the Source and Evidence Unit identities produced by the real ingestion pipeline:

```json
{
  "schema_version": 1,
  "index_version_id": "riv_sprint6_candidate_001",
  "user_id": "usr_eval",
  "notebook_id": "nb_eval",
  "cases": [
    {
      "case_id": "critical-txt-en",
      "fixture_sources": {
        "txt-en-v1": "src_eval_txt_en"
      },
      "evidence_units": {
        "txt-launch-date": ["unit_from_active_evidence_revision"]
      }
    }
  ]
}
```

Each Suite Case must appear exactly once. `fixture_sources` must contain exactly its frozen fixtures. Every expected or equivalent Evidence label must map to one or more authoritative Unit IDs. A Unit cannot represent two different labels.

## Run and promote

```sh
go run ./cmd/rag-eval \
  -suite evals/rag/sprint6-v1.json \
  -config evals/rag/pinned-config-v1.json \
  -live-product-sources /absolute/path/live-sources.json \
  -database-url 'postgres://nano:nano@localhost:55432/nano?sslmode=disable' \
  -index-version-id riv_sprint6_candidate_001 \
  -create-candidate \
  -eval-run-id eval_sprint6_candidate_001 \
  -qdrant-url http://127.0.0.1:56333 \
  -qdrant-collection nano-source-evidence-gemini-2-768-v1 \
  -bifrost-url http://127.0.0.1:56666
```

The live path executes sequentially as a background workload. Before the Cases run, it builds and verifies the candidate projection for every ready Source with an active Evidence Revision. Each Case then creates an isolated Eval chat and Run, pins the explicit candidate through the internal worker-only admission path, and executes the existing production Agent.

The pinned dense path uses `gemini/gemini-embedding-2` at 768 dimensions with embedding profile `gemini-retrieval-v1`. Projection sends one document per embedding request until the locked Bifrost batch mapping is independently accepted. The versioned `nano-source-evidence-gemini-2-768-v1` collection is created and dimension-checked without deleting the previous collection.

The resulting Observation is derived rather than supplied:

- fixture identity from `source_sources.content_sha256`;
- parsing coverage and extraction identity from active Evidence authority;
- retrieved Evidence from durable `search_evidence` Action Results;
- claims and Citations from grounding and publication tables;
- answer facts from the published assistant message;
- latency from the Run lifecycle;
- tokens and known USD cost from durable model trace attributes.

The Eval report is recorded before promotion. Promotion additionally checks that every current ready Source has a verified build for the candidate. A passing report cannot bypass a missing candidate projection.

## Failure handling

- A fixture SHA mismatch fails fixture identity even if the answer happens to be correct.
- Partial coverage, extraction/schema drift, model/prompt/Agent config drift, or a different pinned Index Version fails invariants.
- Missing expected retrieval, incorrect Citation aliases, uncited claims, unsupported claims, forbidden claims, latency, or cost failures keep the candidate unpromoted.
- Failed live Runs remain durable for developer inspection. They are Eval artifacts, not user-visible Notebook output.
- Retrying uses a new Eval Run identity. Do not edit a recorded report or mutate an existing Retrieval Index Version configuration.
- `User location is not supported for the API use` is a Provider egress/billing prerequisite failure. Do not treat it as a retrieval-quality result or promote a candidate from precomputed observations.

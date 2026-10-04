# Bifrost config

Bifrost is the model gateway described in
[ADR 0010](../../docs/technical-architecture/adr/0010-use-a-standalone-bifrost-model-gateway.md).
Both of its file configurations are committed:

| File | Used by | DashScope `base_url` |
|---|---|---|
| `config.json` | `compose.yaml` (local dev) | `https://dashscope.aliyuncs.com/compatible-mode` (China mainland) |
| `config.prod.json` | `compose.prod.yaml`, mounted read-only over `/app/data/config.json` | `https://dashscope-intl.aliyuncs.com/compatible-mode` (international) |

Production runs in AWS `ap-southeast-1`. The mainland endpoint is not
reachable from there. Pointing at it does not fail fast: the TCP connection
times out, so chat requests hang for tens of seconds before failing. If chat
completions time out in production, check this first.

## Credentials

The configs contain no secrets. Every key is an `env.<NAME>` reference that
Bifrost resolves from `infra/compose/.env`, which stays gitignored:

| Variable | Provider | When missing |
|---|---|---|
| `DASHSCOPE_API_KEY` | `aliyun` (chat generation) | Chat fails |
| `GEMINI_API_KEY` | `gemini` (retrieval embeddings) | Source processing and retrieval fail |
| `COHERE_API_KEY` | `cohere` (evidence reranking) | Bifrost still starts; search falls back to RRF-only ordering |

A DashScope key is issued per region. Use a mainland key locally and an
international key in production. `config_test.go` rejects any literal
credential in either file and keeps the two files identical apart from the
endpoint and key labels.

## Changing providers

Edit both files in the same commit. Deploys apply the change: the server's
`git reset --hard origin/main` updates the files, and Compose recreates
`bifrost`. A rebuilt server needs only `infra/compose/.env`.

Bifrost writes runtime state under `logs/`, which is gitignored.

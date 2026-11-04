# Trace Analytics

The product dashboard and the operations dashboard answer different questions:

- `/admin/traces/analytics` aggregates bounded, authorized ClickHouse data for
  Agent developers. It never exposes Prompt, output, Replay, or raw payloads.
- Grafana shows whether Kafka, the Agent Trace Processor, ClickHouse, and the
  Analytics API are healthy. It uses only bounded operational labels.
- The operations dashboard also exposes the raw-to-summary watermark gap; a
  sustained gap means retained events are newer than the product projection.

The API defaults to 24 hours, rejects ranges over 30 days, uses server-selected
UTC buckets, and returns `schema_version`, actual filters, `generated_at`,
`fresh_through`, and nullable-data coverage. Success/error/retry rates use only
terminal Agent Runs as their denominator. Active Runs remain visible in Run
counts but never enter latency percentiles or terminal-rate denominators.

Token and cost absence is unknown, not zero. Costs remain split by currency.
Older rows without newly typed provider/tool/error/version/RAG fields appear in
an `unknown` bucket. Quality trends remain out of scope until real evaluation or
feedback data exists.

Access requires `platform.trace.analytics`, independently of
`platform.trace.read`. The Control Plane injects authorization scope and ignores
client-supplied Notebook scope. The browser calls the Control Plane only; the
Collector service credential and ClickHouse credentials never reach it.

For alert response and the product-vs-infrastructure decision boundary, see
[the Trace analytics pipeline runbook](BACKEND_ENGINEERING.md#trace-analytics-pipeline-runbook).

## Prefix-cache diagnosis

A provider reuses its prompt cache only up to the first byte that differs
from an earlier request, so a healthy Agent Run's cached share climbs in a
sawtooth toward 90%. Each model span row in `obs_span_analytics` carries
`input_tokens`, `cached_tokens`, `decision_ordinal`, `span_started_unix_nano`,
and the hashes of the request's leading system prompt (`system_sha256`) and
tool list (`tools_sha256`). Rows written before these columns read as NULL or
an empty string.

List one Trace's decisions in order. A row whose `cached_share` drops toward
zero is a prefix break: `system_changed` or `tools_changed` names the leading
part that moved; when neither changed, an earlier trajectory message was
rewritten (for example by a batch of Tool Result aging).

```sql
SELECT
  decision_ordinal,
  input_tokens,
  cached_tokens,
  round(cached_tokens / input_tokens, 3) AS cached_share,
  system_sha256 != lagInFrame(system_sha256) OVER w AS system_changed,
  tools_sha256 != lagInFrame(tools_sha256) OVER w AS tools_changed
FROM obs_span_analytics FINAL
WHERE trace_id = {trace_id:String} AND span_kind = 'model' AND input_tokens > 0
WINDOW w AS (ORDER BY span_started_unix_nano ROWS BETWEEN 1 PRECEDING AND CURRENT ROW)
ORDER BY span_started_unix_nano
```

Summarize the cached share per Agent Definition over a range:

```sql
SELECT agent_name, count() AS model_calls,
  round(sum(cached_tokens) / sum(input_tokens), 3) AS cached_share
FROM obs_span_analytics FINAL
WHERE span_kind = 'model' AND input_tokens > 0 AND started_at >= now() - INTERVAL 7 DAY
GROUP BY agent_name ORDER BY model_calls DESC
```

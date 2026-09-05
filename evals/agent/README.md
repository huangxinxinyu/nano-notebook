# Research Agent baseline experiment

This suite evaluates Nano Notebook's actual research work: 4 single-paper reading
tasks, 6 paper comparisons and 10 open research investigations. The original
papers and task-specific rubrics are in [research-v1.json](research-v1.json).
The suite was written before observing outputs. It is a small exploratory suite,
not a standardized benchmark or a statistically representative sample.

## Fixed experiment

- Baseline: the current product release `nano.default@25`, with its unchanged
  Chat/Research model policies (`aliyun/qwen-plus`, temperature 0). Do not raise
  temperature to make attempts more diverse or change prompts to improve scores.
- Each task runs five times. Every trial creates a fresh local evaluation account,
  Notebook and Chat. Fixed-paper tasks upload identical versioned PDF bytes and
  wait until all selected Sources are Ready. Open research starts without Sources.
- Fixed-paper tasks enter Chat; open tasks enter Research. The driver accepts the
  first complete plan with no clarifying questions, without editing or coaching.
  A request for additional clarification does not satisfy this fixed protocol.
- The real HTTP API admits runs and the real Worker executes them. The driver
  does not reconstruct or substitute a tool registry, Agent Loop or model call.
  Product-internal retries are part of the baseline; no extra attempt is sampled
  to replace an unsuccessful trial.
- Baseline JSON records Git revision, runtime source fingerprint, suite hash and
  release. Source manifest records exact PDF URLs and byte hashes. Each observed
  run records the actual admission-pinned definition/model/context policy hashes.
  A provider's mutable model alias and changing public web remain reproducibility
  limits even with these pins. Temperature 0 does not guarantee IID randomness.

## Run locally

Start the existing development product using `scripts/start`. This runner uses
the local API at port 8080 and the local Compose PostgreSQL database for read-only
baseline verification. It creates only dedicated evaluation accounts/data; it
does not access personal Notebook content. Do not point it at production.

```bash
python3 -m venv .codex-artifacts/agent-eval-venv
.codex-artifacts/agent-eval-venv/bin/pip install -r scripts/agent-eval/requirements.txt
python3 -m unittest discover -s scripts/agent-eval -p 'test_*.py'
python3 scripts/agent-eval/experiment.py run --out .codex-artifacts/research-baseline --workers 4
.codex-artifacts/agent-eval-venv/bin/python scripts/agent-eval/grade.py --out .codex-artifacts/research-baseline
python3 scripts/agent-eval/experiment.py report --out .codex-artifacts/research-baseline --grades .codex-artifacts/research-baseline/grades.json
```

`--cases single-react,open-context` runs a first subset with the same five trials
per task. A later invocation with the same output directory skips completed
observations without resampling them. An existing `started/<task>-<n>.json`
without an observation stops that trial from being silently repeated after a
crash. Inspect its admission and finish the existing run; do not delete the
reservation merely to replace an inconvenient result. `experiment.py collect
--out <directory>` reads the already admitted runs' published results from the
local database without rerunning them. This also avoids the current in-flight
Chat snapshot API's nested-query `conn busy` error; it does not change execution.
Incomplete trials or
missing grades prevent aggregate report generation.

PDF fixture preparation is serialized to fit the existing renderer capacity.
Only Source processing can be retried (up to three preparation attempts per
file), before admitting an Agent. `--retry-preparation` can recover a previously
failed setup only after checking that its Sources are terminal and no Chat
message/Agent trial was admitted. The original preparation reservation and
recovery record remain on disk. It cannot replace failed Agent outputs.

## Binary grading

Each trial passes only if it produces the requested deliverable, all task-specific
rubric items pass, key claims are supported by identified original evidence, and
there is no material fabrication. A completed run status alone is insufficient.

`grade.py` is a separate evaluator, not part of the tested Agent. It gives a fixed
Qwen judge the frozen rubric, the actual output, product citations, and full text
of the fixed papers or independently fetched cited public sources. The grading
prompt is hashed and each verdict is tied to the observation hash. Unavailable
source text is explicitly marked unavailable; oversized judge contexts stop for
review rather than silently truncating evidence. These model judgments are not
human ground truth. Review representative positive/negative and ambiguous
judgments before interpreting the report; document any adjudication separately.

The public report contains only aggregate rates and per-task pass/fail outcomes.
Underlying answers and grading evidence are retained for reproducibility, without
building a Trace UI, cost dashboard or failure-analysis product.

For N tasks with c_i successes out of five:

- Pass@1 = mean(c_i / 5), the empirical average single-run success rate.
- Pass@5 = mean(c_i > 0), the fraction with at least one successful trial.
- Pass^5 = mean(c_i = 5), the fraction with all five successful trials.

The report also computes these separately for the three task categories. It does
not apply `1-(1-p)^5` to an overall success rate, drop failed runs, or convert
partial experiments into full-suite scores. Higher Pass@5 does not imply that a
real user can automatically identify the successful answer.

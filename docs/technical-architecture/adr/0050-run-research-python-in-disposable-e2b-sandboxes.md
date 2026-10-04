---
status: accepted
---

# Run Research Python in disposable E2B sandboxes

Deep Research can execute model-written Python through the `run_python` tool, pinned by `research.executor@19` through `nano.default@28`. The code runs in an E2B Firecracker microVM created from the managed `code-interpreter-v1` template, never inside the Worker, Control Plane, or any container that holds Nano credentials. The Worker only orchestrates the call over HTTPS: create sandbox, upload input files, execute, list and download outputs, kill sandbox.

Each call gets a fresh sandbox, which is killed in a deferred call and also bounded by a provider TTL of the execution deadline plus a short grace period. The sandbox is created with internet access disabled, no environment variables, and only the workspace files named in `input_paths`. Web content still enters Research only through `read_url` and web-reader (ADR 0049). If model-written code is steered by injected page text, it can reach neither Nano secrets nor the network.

Execution is stateless between calls. Interpreter state that lives across tool calls would sit outside the checkpoint and break crash replay. Instead, the program writes durable results to `output/`. The Worker validates each output's name, extension, size, and UTF-8 encoding, then stores it as a run-scoped `data/<name>` Research workspace object addressed by Run, Action, and content hash. The checkpointed `run_python` result indexes those objects exactly as `write_research_file` results do. Replaying an Action after a crash may start another sandbox, but it converges on the same workspace files.

Stdout, stderr, display values, and tracebacks return to the model only after byte bounds are applied and ANSI escapes are stripped. A Python exception or timeout is a successful tool result with `status: error|timeout`, because the model needs the traceback to recover. Provider failures map to cataloged `code_sandbox_*` domain errors and never echo provider bodies or the API key. Images and binary outputs are counted and dropped; the workspace stays text-only.

The API key is read from `NANO_E2B_API_KEY` alongside the other provider keys. When it is absent, the tool stays registered for pinned definitions but is unavailable, so the model never sees it. Computed values are not evidence: the executor prompt requires their inputs to carry citations of their own.

E2B was chosen over a self-hosted gVisor runner so model-written code never runs on the single production host that also holds every Nano datastore, and so Nano does not operate a sandbox runtime. Per-second billing is negligible at current volume. The accepted cost is that input files and code leave Nano for a third-party processor. The `codesandbox.Runner` interface keeps a self-hosted runner a drop-in replacement.

Implementation evidence:

- [E2B REST API specification](https://github.com/e2b-dev/E2B/blob/main/spec/openapi.yml)
- [E2B envd file API specification](https://github.com/e2b-dev/E2B/blob/main/spec/envd/envd.yaml)
- [E2B code-interpreter execution server](https://github.com/e2b-dev/code-interpreter/tree/main/template/server)
- [E2B pricing](https://e2b.dev/pricing)

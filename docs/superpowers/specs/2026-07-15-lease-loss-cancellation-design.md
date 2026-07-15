# Lease-loss cancellation normalization

## Problem

Stopping a running Agent Run clears its Job lease. The next Worker heartbeat then cancels the in-flight model request with `agent.ErrLeaseLost`. The model client wraps that cancellation as `model_unavailable`, the Agent Loop attempts a fenced terminal failure, and the Worker joins the same lease-loss cause again. A successful user cancellation is therefore logged as an execution failure with a misleading model error and repeated causes.

## Design

The Agent Loop treats `agent.ErrLeaseLost` from the execution context as control flow. When a model call returns after that cancellation, the Loop returns the canonical lease-loss error without calling `Publisher.Fail`. Real provider failures and model timeouts retain their existing terminal-failure behavior.

The Worker treats a heartbeat-confirmed lease loss as a successful end of the obsolete local attempt. It still cancels the in-flight execution and performs the best-effort lease release, but returns no processing error, so `ProcessAvailable` does not emit `agent run execution failed`. Heartbeat database errors remain observable failures.

This change does not alter the durable Run or Job state machine, the 10-second heartbeat interval, lease fencing, retry behavior, or model-provider contracts.

## Verification

- An Agent Loop test reproduces a model error wrapping the lease-loss cancellation cause and proves that no terminal failure is published.
- A Worker test proves heartbeat lease loss cancels execution but leaves `ProcessAvailable` without an error.
- Existing model-failure, timeout, lease recovery, and integration suites remain green.

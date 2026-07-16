# Nano Notebook Sprint 2B PRD

## Document Status

- **Sprint:** Sprint 2B
- **Status:** Accepted — ready for implementation
- **Date:** 2026-07-15
- **Theme:** Interruptible and recoverable bare Agent execution
- **Delivery boundary:** Sprint 2B merges the former interruption and crash-recovery slices. It extends the Sprint 2A source-less Fixed Agent Loop without adding tools, Run Checkpoints, token streaming, or observability/audit storage.

## 1. Goal

Make the existing single-call Agent execution safe to stop and safe to reclaim after Worker loss:

> Stop an active answer with a durable no-publication guarantee; recover abandoned work through bounded leases and fencing; preserve one product Run across infrastructure attempts; and allow the latest unanswered question to be explicitly retried as a new Run.

The Sprint proves recovery of execution ownership, not continuation of an in-flight model generation. Because the Fixed Agent Loop has one model call and no tools, an expired attempt re-executes the whole pass.

## 2. Canonical Identities

- **User Message:** immutable Chat content submitted once. Transport replay reuses its client UUID.
- **Agent Run:** one user-visible requested answer with a unique ID and terminal history.
- **Agent Job:** the one durable delivery record for an Agent Run.
- **Attempt:** one Worker execution of that Job. Sprint 2B stores only the current attempt generation on the Job row, not an Attempt history table.
- **Job Lease:** the current attempt's expiring execution authority.
- **Lease Token:** the current lease generation used to fence stale Workers.
- **Run Retry:** a new user command after `cancelled` or `failed`; it creates a new Run and Job for the same latest unanswered User Message.
- **Run Checkpoint:** a future durable completed-step boundary. Sprint 2B intentionally does not implement it.

The identity rules are:

```text
transport replay       -> same Message, same Run, no new Attempt
Worker loss recovery   -> same Message, same Run, same Job, new Attempt
explicit user Retry    -> same Message, new Run, new Job, first Attempt
```

## 3. Product Semantics

### 3.1 Stop

- Product copy says “Stop”; the canonical terminal Run state is `cancelled`.
- The PostgreSQL cancellation transaction is the product boundary.
- If cancellation commits before publication, the Run can never publish an Assistant Message.
- The same transaction terminalizes the Agent Job as `cancelled` and releases the user's active-Run slot immediately.
- Cancelling an in-flight Bifrost request is cooperative and best-effort, not the product guarantee.
- If publication commits first, the completed answer is not withdrawn and the Stop command fails as not cancellable.

### 3.2 Crash Recovery

- A Worker maintains a 30-second lease with a heartbeat every 10 seconds.
- Each successful heartbeat replaces the deadline with PostgreSQL current time plus 30 seconds.
- A still-active Job whose lease expires may be atomically reclaimed without passing through `queued`.
- The Run remains user-visible `running` across attempts; there is no `recovering` or `retrying` Run state.
- Recovery re-runs the complete `LoadRun -> BuildContext -> InvokeModel -> PublishAnswer` pass.
- A partial model generation cannot resume, and a result lost before publication may cause a duplicate model call.
- Only the current unexpired Lease Token may heartbeat, fail, cancel, reconcile, or publish.

### 3.3 Explicit Retry

- Retry is allowed only after `cancelled` or `failed`.
- Retry is available only while the input remains the Chat's latest unanswered User Message.
- Retry creates a new Run and Job for the same immutable User Message.
- A terminal Run is never reopened.
- Once the Chat advances, the historical question cannot be retried.
- Completed-answer regeneration, historical retry, and branching are unavailable.

## 4. State Machines

### 4.1 Agent Run

```text
queued --claim--> running --publish--> completed
  |                  |  \--terminal model/domain failure--> failed
  |                  \--Stop-------------------------------> cancelled
  \--Stop--------------------------------------------------> cancelled

running --expired lease + attempts remain--> running
running --third attempt expires------------> failed(recovery_exhausted)
```

`completed`, `failed`, and `cancelled` are permanent terminal states.

### 4.2 Agent Job

```text
queued --claim attempt 1--> running --publish--> succeeded
  |                           |  \--terminal failure--> failed
  |                           \--Stop---------------> cancelled
  \--Stop------------------------------------------> cancelled

running/expired --reclaim attempt 2 or 3--> running
running/expired attempt 3------------------> failed
```

The browser never reads Job state directly.

## 5. Persistence Changes

### 5.1 `agent_runs`

- Add `cancelled` to the status constraint.
- Remove the global unique constraint from `input_message_id`.
- Preserve the existing one-active-Run-per-user partial unique index.
- Enforce at most one queued/running Run for an input Message.
- Enforce at most one completed Run for an input Message.
- A cancelled Run has no output Message and no error code.
- A failed Run has no output Message and one safe error code.
- A completed Run has one unique output Message and no error code.

### 5.2 `agent_jobs`

Add:

| Field | Contract |
| --- | --- |
| `attempt_no` | `0` while queued; incremented to `1` on first claim and on each reclaim; maximum `3` |
| `lease_token` | Nullable random UUID identifying the current running generation |
| `lease_expires_at` | Nullable PostgreSQL deadline for the current running generation |

Also:

- Add `cancelled` to the status constraint.
- `queued` has attempt `0` and no lease.
- `running` has attempt `1..3`, a token, and an expiry.
- Terminal state carries no live execution authority.
- Do not add `lease_owner`, a persisted heartbeat timestamp, a retry schedule, or an Attempt table in this Sprint.

### 5.3 Retry Idempotency

The existing `platform_idempotency_keys` mechanism records Retry with a distinct action and a hash of the source Run command. Reusing the same key and request returns the same new Run; reusing it for a different command returns `409 idempotency_mismatch`.

## 6. Worker Algorithm

### 6.1 Claim And Reclaim

The indexed candidate set contains:

```sql
status = 'queued'
or (
  status = 'running'
  and lease_expires_at <= now()
)
```

Claim uses `FOR UPDATE SKIP LOCKED` and then:

- terminalizes an expired attempt `3` as `recovery_exhausted`; or
- keeps the Job and Run `running`, increments `attempt_no`, replaces `lease_token`, sets the 30-second deadline, and returns the new token.

Startup, listener reconnect, notification wake-up, and the existing five-second fallback scan use the same claim path.

### 6.2 Heartbeat

While AgentLoop executes, a Worker heartbeat conditionally performs:

```sql
update agent_jobs
set lease_expires_at = now() + interval '30 seconds'
where id = $1
  and status = 'running'
  and lease_token = $2
  and lease_expires_at > now();
```

- One updated row renews authority.
- Zero rows cancels the local execution context and forbids publication.
- A database error is not a successful renewal.
- Final publication independently checks the same token and expiry.

### 6.3 Retry Boundary

- Lease expiry without a terminal outcome is the only automatic Job recovery trigger.
- Bifrost owns bounded Provider retry and fallback.
- An explicit terminal Bifrost timeout, unavailability, or invalid response fails the Run and Job without another Job attempt.
- The Job permits three total attempts including the first.
- After attempt three expires, `recovery_exhausted` is terminal; a user may explicitly Retry as a new Run if the question remains eligible.

### 6.4 Graceful Shutdown

A terminating Worker:

1. stops claiming new Jobs;
2. cancels its active model-call context;
3. conditionally sets its current lease deadline to PostgreSQL current time;
4. notifies the Job channel for prompt reclaim;
5. falls back to natural expiry when PostgreSQL is unavailable.

The Job never transitions back through `queued`.

## 7. Publication Barrier And Commit Uncertainty

Publication locks and revalidates:

- Run and Job are both `running`;
- the Job presents the current unexpired Lease Token;
- the Run has no output Message and is not cancelled;
- private Chat ownership and current Notebook access remain valid.

Cancellation and publication serialize on the same authoritative rows. Whichever transaction commits first wins.

A Publication transaction error is an unknown infrastructure outcome, not automatically a failed Run. The Worker reloads PostgreSQL:

- `completed`: accept success;
- `running` with the same valid lease: retry publication with the same in-memory result;
- `cancelled` or lease lost: discard the result;
- PostgreSQL still unavailable: stop renewing and let lease recovery reconcile durable state.

## 8. HTTP And SSE Contracts

### 8.1 Stop

`POST /api/v1/agent-runs/{run_id}/cancel`

- Requires Session, CSRF, and Run ownership.
- Queued/running -> `200` with the terminal cancelled Run snapshot.
- Already cancelled -> idempotent `200` with the same snapshot.
- Completed/failed -> `409 run_not_cancellable`.
- Missing or inaccessible -> safe `404`.
- Needs no Idempotency-Key.

### 8.2 Retry

`POST /api/v1/agent-runs/{run_id}/retry`

- Requires Session, CSRF, Run ownership, and `Idempotency-Key`.
- Source Run must be cancelled or failed.
- Its input must still be the Chat's latest unanswered User Message.
- No completed Run may exist for the input.
- The user must have no active Run.
- Success atomically creates a new queued Run and queued Job, not a Message, and returns `202`.
- Same-key replay returns the same new Run.
- Historical input -> `409 retry_not_latest`.
- Completed source -> `409 run_not_retryable`.
- Active conflict -> `409 active_run_conflict`.

Sprint 2B stores no `retry_of_run_id`; Runs for one input are ordered by creation.

### 8.3 Chat Snapshot

`GET /api/v1/chats/{chat_id}` replaces `active_run` with `runs`, containing only the newest Run projection for each User Message.

- queued/running projection restores activity and SSE reconnect;
- latest unanswered failed/cancelled projection restores Retry;
- historical failed/cancelled projection explains the missing answer but offers no Retry;
- completed projection correlates with its persisted Assistant Message;
- superseded Runs, Jobs, attempts, leases, and Trace state are omitted.

### 8.4 SSE

- Existing queued, running, completed, and failed snapshots remain.
- Add terminal `cancelled`.
- The stream sends the committed cancelled snapshot and closes.
- Attempt changes and reclaim produce no user-visible SSE state.

## 9. Frontend Contract

- Show Stop while the latest Run is queued or running.
- A successful Stop immediately displays “stopped” from the returned durable snapshot.
- The composer is released immediately after durable cancellation, even if the old Provider request takes up to the next heartbeat to stop.
- Show Retry only for the latest unanswered failed or cancelled Run.
- Retry reuses the original User Message in the thread and opens SSE for the new Run.
- Historical failed/cancelled Messages retain a terminal label without Retry.
- Do not display Attempt count, lease state, recovery activity, hidden reasoning, or partial model output.

## 10. Explicitly Out Of Scope

- Run Checkpoints, durable step outputs, or continuation from a partial model generation
- MCP, tools, tool-call iteration, planners, reflection, subagents, or branching
- completed-answer regeneration or historical question retry
- token streaming, partial Assistant persistence, or delta replay
- Run Events, Model Call payload retention, Durable Agent Trace, audit UI, or a reusable observability SDK
- separate Attempt history, lease-owner tracking, or a Job administration UI
- Redis, an external MQ, or a general Workflow SDK
- Agent-runtime retry after an explicit terminal Bifrost failure

Run Checkpoints are mandatory in Sprint 3 before multi-step model/Action iteration. That runtime will persist accepted model decisions and Action results at explicit boundaries and continue from the first incomplete step; it remains distinct from Sprint 4 trace storage.

## 11. Verification

Sprint 2B must prove:

1. Stop of a queued Run atomically cancels Run and Job and releases the active slot.
2. Stop of a running Run prevents a late model result from publishing.
3. Concurrent Stop and Publish serialize correctly in both commit orders.
4. Heartbeat renews only the current unexpired Lease Token from PostgreSQL time.
5. Lease expiry permits exactly one Worker to reclaim with a new token and incremented attempt.
6. A stale Worker cannot heartbeat, fail, or publish after reclaim.
7. The Run remains `running` across attempts and emits no recovery projection.
8. Expiry of attempt three produces `recovery_exhausted` and no Assistant Message.
9. Explicit terminal Bifrost failure does not create another Job attempt.
10. Publication acknowledgement loss reconciles committed success or safely yields to recovery.
11. Graceful shutdown actively expires the current lease; database failure falls back to natural expiry.
12. Retry is idempotent, creates a new Run and Job for the same Message, and creates no Message.
13. Retry rejects completed, historical, or active-conflict cases without partial rows.
14. Context construction ends at the Run input Message and excludes later Chat content.
15. Chat reload restores the newest Run per Message, reconnects active SSE, and offers Retry only on the latest unanswered Message.
16. Unit, PostgreSQL integration, controlled Bifrost, frontend, race, lint, type, build, and browser tests pass.

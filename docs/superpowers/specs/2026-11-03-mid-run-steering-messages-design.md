# Mid-run Steering Messages

## Document status

- **Status:** Implemented
- **Date:** 2026-11-03
- **Reference behavior:** Pi agent steering queue (`packages/agent`: steering
  messages are injected after the current tool calls finish and the model
  responds on the next turn)

## Problem

A Chat accepts one active Run per Member. While that Run works, a new message
returns `409 active_run_conflict` (`internal/app/server.go`,
`internal/app/research_routes.go`). A Research execution Run takes tens of
minutes; when the Member sees it drift, the only remedy is cancel and resend,
which discards every token the Run has spent.

Pi keeps two queues for this. A **steering** message is delivered after the
current tool batch completes and the model answers it on the next turn. A
**follow-up** waits until the run ends and then starts a new turn. Nano adopts
steering. Follow-up is not adopted: a message sent after the Run ends is an
ordinary new turn, and auto-admitting one from the Worker would duplicate the
admission path (evidence pinning, mode selection, research sessions) that the
Control Plane owns.

## Decision

> A Steering Message is a Member User Message addressed to a Run that is still
> `queued` or `running`. It admits no Run. The Controller delivers it at the
> Run's next decision boundary by appending a `steering_message` checkpoint,
> and the model sees it exactly once at a durable position.

### Durable model

- `chat_messages.steering_run_id` names the steered Run; it is null for an
  ordinary turn. The Chat lane excludes these rows from its turn list, so a
  later turn never sees a Steering Message twice.
- `agent_run_checkpoints` gains `kind = 'steering_message'`. The checkpoint
  carries `{message_id, content}`, has `decision_no` equal to the decision it
  precedes, and is legal only between complete Agent Steps and before a Final
  Draft. It spends no model-call or Action budget and does not advance
  `AcceptedDecisions`.
- Delivery is derived, not stored: a message is delivered when its Run holds a
  checkpoint naming it. A crashed Attempt therefore never delivers a message
  twice and never loses one.

### Controller boundary

After an incomplete Action batch is resolved and before the next decision
request is built, the Controller asks the runtime for pending Steering
Messages. Each becomes a checkpoint for `AcceptedDecisions + 1`, in the order
the Member sent them, and the loop restarts from the durable prefix. A Run
that already accepted a Final Draft publishes it; a Steering Message that
arrives after that point stays undelivered and the Chat shows it as such.

### Projection

**Chat runtime.** `ProjectChatLane` interleaves Steering Messages as
`user_message` Context Units before the Agent Step they preceded, so Chat
compaction treats them as ordinary cut units with stable keys.

**Research runtime.** Archival capsules and task memories summarize Agent
Steps by decision range and would drop a one-off user message, so Research
strips steering from the trajectory and recites every delivered Steering
Message as one trailing User message, each entry naming the decision it
arrived before. This keeps the guidance in force for the rest of the Run and
leaves the cached prefix untouched. Checkpoint-range queries that group by
`decision_no` filter to proposal and result kinds.

### Surface

- `POST /api/v1/agent-runs/{run_id}/steer` with `{id, content}`. Only a root
  Chat or Research execution Run that is `queued` or `running` may be
  steered; a planning Run (`run_not_steerable`) answers from its own
  questions, and a subagent has no Chat. Replaying the same message id with
  the same content is idempotent.
- The Chat snapshot carries `steering_run_id` and `steering_delivered` on each
  message. The composer stays enabled while a Run works; a message typed then
  is sent as steering and shown with its delivery state.

## Non-goals

- Follow-up queue and auto-admission after a Run ends.
- Steering a subagent or a Research planning Run.
- Re-opening a Run whose Final Draft is already accepted.

## Acceptance

- `internal/agent/steering_test.go`: checkpoint acceptance rules, lane
  placement, Research recitation, Controller delivery once after the current
  batch and before the first decision.
- `web/src/app/App.test.tsx`: a message typed during a running Run posts to
  the steer endpoint, never to message admission, and renders its pending
  delivery state.

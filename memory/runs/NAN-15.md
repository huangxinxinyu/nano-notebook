# NAN-15: Nano Notebook Sprint 1

## Requirement

Deliver the approved Sprint 1 as one runnable local-first product slice: Web
Client, Go Control Plane and Worker, Compose infrastructure, local identity and
revocable sessions, owner-isolated durable Notebooks, Library search and recent
ordering, a responsive empty Notebook workspace, English and Simplified
Chinese, accessibility behavior, observability, and clean lifecycle commands.
Keep source ingestion, Chat execution, retrieval, sharing, deployment, and
other later-sprint capabilities out of scope.

## Plan

Build the foundation in ordered layers: local infrastructure and lifecycle,
PostgreSQL schema and authorization boundaries, identity/session and Notebook
APIs, Control Plane and no-op Worker, the localized responsive Web journey,
then automated and manual acceptance. Deliver one remotely reachable candidate
branch and require independent QA and Review of the same immutable SHA before a
fast-forward-only merge to `main`.

## Decisions And Constraints

- PostgreSQL is the system of record; Notebook access is enforced by ownership
  membership, application authorization, and row-level security.
- Sessions are opaque, cookie-based, revocable, CSRF-protected, and durable
  across process restarts.
- Notebook creation uses deterministic canonical idempotency over validated,
  normalized parameters; transport formatting does not change request identity.
- Desktop exposes Sources, Chat, and Outputs as three simultaneous named
  regions. Compact layouts use one visible Radix Tabs panel at a time.
- The Worker remains a healthy no-op without provider credentials; later agent,
  ingestion, retrieval, and generation behavior is intentionally absent.
- Open Notebook components were adopted with provenance and notices; NotebookLM
  was used only as dated visual-reference evidence.
- Clean test startup must wait for the final PostgreSQL TCP listener rather than
  the image's temporary Unix-socket initialization server.
- Approved product, architecture, engineering, and Sprint documents remained
  unchanged during implementation.

## Acceptance Evidence

- Approved Requirements: NAN-16 comment
  `a9122254-9552-456f-b5a8-687a4f501aca`.
- Approved Plan: NAN-17 comment
  `db45b32a-8867-42c4-aaca-746d6ef85fc6`.
- Whole-product Chrome DevTools computer-use QA: NAN-26 comment
  `3b91141b-c4a3-444e-941b-1407f883d046`.
- Final script-only replacement QA: NAN-28 comment
  `45f57f53-3a4e-4a3c-8f39-e7d509705fa4`.
- Final Review: NAN-29 comment
  `e731e179-037d-4918-82dd-d8a6a2e5de80`.
- Final accepted delivery SHA:
  `8c956512542290fec7356d96f71f0ebf40530023`.

## Outcome

Sprint 1 was implemented, pushed on `multica/nan-15`, independently accepted,
fast-forwarded to `main`, and pushed without target drift. Verification covered
Go tests, vet and builds; Web type-check, lint, unit tests, build and Playwright;
two fresh-volume PostgreSQL test starts; restart durability; API security and
idempotency; health and tracing; and QA-owned desktop/mobile English/Chinese
browser journeys. Services and disposable QA state were cleaned after testing.

## Reusable Lessons

- Exact-SHA computer-use QA caught a responsive hierarchy defect that automated
  behavior checks alone did not expose.
- Idempotency must be computed from accepted domain parameters, never raw JSON.
- `pg_isready` on the default container socket can observe the official image's
  temporary init server; readiness for post-init work should require final TCP.
- A timing-sensitive lifecycle failure deserves deterministic regression
  coverage even when an unchanged rerun succeeds.
- After a narrow late rework, exact-SHA QA and Review can focus on the changed
  surface while retaining prior evidence for provably unchanged UI behavior.

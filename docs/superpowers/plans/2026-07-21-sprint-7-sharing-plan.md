# Sprint 7 Collaboration And Sharing Implementation Plan

**Date:** 2026-07-21

**Source:** `docs/sprint/SPRINT-7-PRD.md` and `docs/superpowers/specs/2026-07-21-sprint-7-sharing-design.md`

**Execution:** Directly on `main`, strict red-green-refactor, atomic verified commits.

## Step 1: Role And RLS Foundation

**Tests first**

- Extend migration integration coverage for populated Owner rows and the three-role constraint.
- Add direct RLS tests for Viewer Source read, Editor Source maintenance, Owner management, and private Chat isolation.
- Add Notebook store tests for all/owned/shared list semantics and caller role.

**Implementation**

- Expand `notebook_memberships.role` and capability vocabulary.
- Replace Owner-only Notebook/Membership/Source RLS with role-aware policies.
- Change Notebook list/get queries from Owner-only to visible Membership while preserving the owned quota.
- Return caller role and presentation capabilities.

**Verification:** targeted Go migration/store/RLS tests, then relevant existing Source and Chat integration tests.

## Step 2: Invitation Authority

**Tests first**

- Add Notebook domain/integration tests for create, idempotent replay, duplicate, capacity, expiry, revoke, resend, token rotation, matching-email acceptance, and concurrent acceptance.
- Add HTTP tests for Owner-only management, safe token failures, CSRF, and registration/sign-in return contract.

**Implementation**

- Add `notebook_invitations` schema, indexes, state constraints, and RLS.
- Add opaque token generation/hash helpers and Invitation domain types.
- Implement per-Notebook serialized create, resolve, accept, revoke, and resend commands.
- Add routes and stable error mappings.

**Verification:** targeted Notebook and HTTP integration tests plus race coverage for pure concurrency tests.

## Step 3: Durable Local Mail

**Tests first**

- Add Outbox tests for atomic enqueue, claim, lease fencing, retry, uncertain acknowledgement, terminal failure, and sensitive payload clearing.
- Add Mailer contract tests and an SMTP adapter protocol test.

**Implementation**

- Add `platform_mail_outbox`, Worker grants/policies, queue/store, and sender service.
- Add provider-neutral Mailer and bounded SMTP implementation.
- Connect Invitation create/resend to Outbox within their transaction.
- Run sender from Worker with bounded concurrency and shutdown.
- Add loopback Mailpit-compatible Compose service, environment defaults, and local docs.

**Verification:** Outbox/Mailer tests and a real local SMTP capture smoke test.

## Step 4: Shared Notebook API And Role-Aware Research

**Tests first**

- Add API tests for all/owned/shared Library scopes, search, pagination, and role fields.
- Add Viewer/Editor Source API tests and cross-Member Chat/Run/Citation attacks.
- Add stale-role reauthorization tests.

**Implementation**

- Complete visible Notebook list/get API contract and bounded cursor paging.
- Route Source read/maintain through the role capability function.
- Preserve creator-only Chat and Run behavior for every role.
- Add Notebook rename.

**Verification:** Notebook, Source, Chat, Agent, Grounding, and Citation integration tests.

## Step 5: Membership And Notebook Lifecycle

**Tests first**

- Add role-change and ownership-transfer invariant/concurrency tests.
- Add remove/leave tests proving Run/Job cancellation, late-publication fencing, private Chat deletion, Trace purge, and shared Source preservation.
- Add Notebook deletion tests proving multi-Member cancellation, durable Source/Trace purge, notification enqueue, and atomic rollback.

**Implementation**

- Add Member list, role change, transfer, remove, and leave commands/routes.
- Reuse Agent terminalization semantics through transaction-aware cancellation helpers.
- Add Notebook rename/delete commands and durable purge collection.
- Enqueue local deletion notifications without making delivery authoritative.

**Verification:** lifecycle integration, race, deletion, Collector purge, and recovery regressions.

## Step 6: Web Collaboration Surface

**Tests first**

- Extend API fixtures and component tests for role-aware Library/workspace behavior.
- Add tests for Manage access, Invitation states, acceptance/auth return, role changes, leave, transfer, rename, and delete.
- Add accessibility and responsive assertions for dialogs and destructive flows.

**Implementation**

- Activate owned/shared Library scopes and row role/actions.
- Add role-aware Notebook snapshot handling and Source controls.
- Add Manage access and Invitation acceptance UI.
- Add English and Simplified Chinese strings and safe stale-authority refresh.

**Verification:** targeted Vitest, typecheck, lint, build, and Playwright collaboration journeys.

## Step 7: Acceptance And Regression

- Run formatting and focused query-plan/constraint inspection.
- Run full `scripts/test-go` and `scripts/test-web`.
- Run opt-in race, local SMTP, browser, Source-family, RAG Eval, and Sprint 1-6 regression gates required by the PRD.
- Audit all 25 Sprint 7 success criteria against named tests, runtime output, or schema/API evidence.
- Fix every missing or weak item through a fresh TDD cycle.
- Inspect final diff and commit any remaining independently verifiable slice.

## Commit Policy

Each commit contains one behavior plus its tests and required migration/configuration. Before each commit: inspect the full diff, stage only the intended slice, run the smallest meaningful verification, and confirm staged paths exclude pre-existing unrelated work.

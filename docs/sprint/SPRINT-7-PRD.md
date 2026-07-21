# Nano Notebook Sprint 7 PRD

## Document Status

- **Sprint:** Sprint 7
- **Status:** Approved for implementation
- **Date:** 2026-07-21
- **Theme:** Local collaboration, Invitations, role enforcement, and shared Notebook lifecycle
- **Delivery boundary:** A local Owner can invite a Viewer or Editor by email, the recipient can accept through the local mailbox, and every Member can use shared Sources while retaining strictly private Chats. Production email, OIDC, and deployment remain a later launch Sprint.

## 1. Decision

Sprint 7 completes the initial-release collaboration model:

1. activate Viewer, Editor, and Owner Membership roles;
2. deliver seven-day email Invitations through a local SMTP mailbox;
3. enforce role-derived capabilities in Go and PostgreSQL RLS;
4. expose owned and shared Notebooks in the Library;
5. keep every Member's Chats, Messages, Runs, and research history private;
6. support role changes, removal, leave, and ownership transfer;
7. complete Notebook rename and permanent deletion where required by sharing;
8. preserve durable cancellation, Source ownership, deletion, Trace purge, RAG, and Citation guarantees.

The approved detailed design is `docs/superpowers/specs/2026-07-21-sprint-7-sharing-design.md`.

## 2. Source Documents

This PRD derives from:

- `docs/product-discovery/CONTEXT.md`
- `docs/product-discovery/DISCOVERY.md`
- `docs/product-discovery/REQUIREMENTS.md`
- `docs/product-discovery/TECHNICAL-HANDOFF.md`
- `docs/technical-architecture/CONTEXT.md`
- `docs/technical-architecture/ARCHITECTURE.md`
- ADRs 0013, 0015, 0016, 0027, 0029, 0030, and 0031
- `docs/sprint/SPRINT-1-PRD.md`
- `docs/sprint/SPRINT-2B-PRD.md`
- `docs/sprint/SPRINT-5-PRD.md`
- `docs/sprint/SPRINT-6-PRD.md`

If this PRD conflicts with an approved product or architecture decision, the approved source wins unless this PRD explicitly supersedes it.

## 3. Sprint Goal

```text
Owner opens Manage access
  -> invites one email as Viewer or Editor
  -> local mail Outbox delivers a one-time link to the local mailbox
  -> recipient registers or signs in with the invited email
  -> recipient accepts the Invitation
  -> shared Notebook appears in the recipient's Library
  -> recipient reads shared Sources and creates a private Chat
  -> Owner cannot inspect that Chat
  -> role changes and removal take effect immediately and durably
```

A developer completes and verifies this journey without external email or identity infrastructure.

## 4. Success Criteria

Sprint 7 is complete only when:

1. Every Notebook has exactly one Owner and no committed transaction can leave zero or multiple Owners.
2. Viewer, Editor, and Owner permissions match the capability matrix at both Go and PostgreSQL RLS boundaries.
3. Owner can invite one canonical email as Viewer or Editor; non-Owners cannot create, inspect, revoke, or resend Invitations.
4. An unexpired pending Invitation reserves one of the 50 non-Owner Member slots.
5. Invitations expire after seven days, can be revoked before expiry, and can be resent with a new token and deadline after expiry.
6. Only one effective pending Invitation exists for a Notebook and canonical email, and an existing Member cannot be invited again.
7. Invitation creation/resend and the mail Outbox record commit atomically.
8. A local Worker delivers Invitation mail to the local SMTP mailbox without external network access.
9. Mail failure never rolls back, corrupts, or consumes an Invitation; delivery is retryable and observable.
10. Only an authenticated User whose canonical account email matches the invited email can accept the one-time token.
11. Acceptance atomically consumes the Invitation and creates one Membership without increasing combined Member-plus-pending capacity.
12. Replayed, rotated, expired, revoked, accepted, malformed, forwarded, and concurrent tokens cannot create unauthorized Memberships.
13. The Library searches and distinguishes owned and shared Notebooks and displays the caller's role.
14. Viewer can inspect ready Sources, Citations, and use private Chat, but cannot maintain Sources.
15. Editor can maintain Sources but cannot manage access, ownership, rename, or delete the Notebook.
16. Owner can manage Membership without reading another Member's private Chats, Messages, Runs, or Replay.
17. Owner can switch a non-Owner between Viewer and Editor without rewriting history.
18. Ownership transfer atomically promotes an existing Member and demotes the previous Owner to Editor while preserving private Chats.
19. Owner can remove a non-Owner, and a non-Owner can leave; Owner cannot be removed or leave without transfer or deletion.
20. Removal or leave immediately revokes access, cancels that Member's active Runs and Jobs, prevents late publication, deletes that Member's private Chats, and preserves shared Sources.
21. Owner can rename and permanently delete the Notebook; deletion cancels Runs, creates durable purge intent, revokes access, and queues local Member notifications atomically.
22. Localized UI exposes invite, accept, member, role, transfer, leave, rename, and destructive confirmation flows with keyboard and responsive behavior.
23. Stale client capabilities never authorize an operation; denial fails safely and refreshes authority.
24. Cross-Notebook, cross-role, cross-Member, token-enumeration, and private-Chat attacks reveal no unauthorized data.
25. Sprint 1-6 authentication, Source, Chat, Agent, cancellation, publication, RAG, Citation, Trace, Replay, Dashboard, and Eval behavior remains green.

## 5. Product Scope

### 5.1 Role Matrix

| Capability | Viewer | Editor | Owner |
| --- | --- | --- | --- |
| Open Notebook and view ready Sources | Yes | Yes | Yes |
| Create and manage own private Chats | Yes | Yes | Yes |
| Add, retry, rename, and delete Sources | No | Yes | Yes |
| View and manage Invitations or Member emails | No | No | Yes |
| Change Viewer/Editor role | No | No | Yes |
| Remove Members | No | No | Yes |
| Transfer ownership | No | No | Yes |
| Rename or delete Notebook | No | No | Yes |
| Leave Notebook | Yes | Yes | No |
| Read another Member's private Chat | No | No | No |

Client flags are presentation hints only. The server derives every decision from authenticated and current authoritative state.

### 5.2 Invitations

- Invitation targets one normalized email and Viewer or Editor.
- The raw bearer token is random, single-use, absent from Invitation storage and ordinary access logs.
- Resolve shows only Notebook title, requested role, and masked email.
- Registration/sign-in returns the recipient to acceptance.
- Account and invited canonical emails must match.
- Pending Invitations reserve capacity and are Owner-managed.
- Resend after expiry rotates token generation.
- Public, anonymous, group, domain, and link sharing are excluded.

### 5.3 Shared Library

- Notebook list items include caller role and stable recent ordering.
- Library supports all, owned, and shared scopes plus title search.
- Owned quota stays 100; shared Notebooks do not consume it.
- Results are bounded and cursor-pageable so inbound sharing is not silently truncated.

### 5.4 Private Research

- Sources are Notebook-shared; Chats are creator-private.
- Each Member has independent Source selection and Chat history.
- Agent admission, recovery, Retry, Stop, Citation resolution, and publication reauthorize Membership.
- Role downgrade does not cancel a Run because every role retains research access.
- Removal, leave, and Notebook deletion cancel affected active Runs before revoking authority.

### 5.5 Membership Lifecycle

- Owner changes only non-Owner Viewer/Editor roles and removes only non-Owners.
- Viewer or Editor may leave after irreversible confirmation.
- Removal and leave delete only the departing Member's private Chats; shared Sources remain.
- Rejoining creates fresh Membership and does not restore Chats.
- Transfer targets an existing Member and demotes the old Owner to Editor.

### 5.6 Notebook Lifecycle

- Owner may rename under existing title rules.
- Owner may permanently delete after explicit confirmation.
- Deletion removes Memberships, Invitations, Sources, private Chats, and product descendants after recording durable purge work.
- Non-Owner Members receive a local notification; delivery failure cannot delay deletion.

## 6. Ownership And Storage

The Notebook Module owns Notebook, Membership, Invitation, roles, capacity, shared discovery, transfer, rename, leave, removal, and deletion orchestration. Identity owns User email and Session. Chat owns private history. Agent owns Run/Job cancellation and publication fencing.

Application PostgreSQL owns mail delivery intent. A Worker claims and sends it through a provider-neutral Mailer adapter. Sprint 7 configures only local SMTP capture.

| Store | Sprint 7 authority |
| --- | --- |
| Application PostgreSQL | Notebook, Membership, Invitation, mail Outbox, cancellation, deletion, and purge intent |
| Local SMTP mailbox | Development delivery observation only |
| S3-compatible Blob Store | Immutable Source and derived artifact objects |
| Qdrant | Rebuildable retrieval projection |
| Collector stores | Diagnostic Trace and Replay data subject to purge commands |

## 7. State And Concurrency Rules

Capacity-changing Membership and Invitation commands serialize per Notebook and expire observed due Invitations before evaluating duplicates or capacity.

Acceptance changes one pending reservation into one Membership in the same transaction. Concurrent accepts, creates, resends, removals, and transfers cannot oversubscribe capacity or duplicate Membership.

Transfer locks both Memberships and preserves the unique Owner invariant. Direct Owner deletion or demotion is forbidden.

Removal, leave, and delete serialize with publication. If revocation commits first, affected Runs are cancelled and cannot publish. If publication commits first, the completed Answer remains until its private Chat is deleted by the lifecycle command.

## 8. Local Mail Contract

Local Compose adds a loopback-bound SMTP capture service and browser mailbox. Repository startup documentation exposes its URL.

Mail Outbox behavior:

- creation is atomic with product state;
- claim uses leases and fencing;
- retryable SMTP failures use bounded backoff;
- uncertain acknowledgement may resend one stable Message-ID;
- successful Invitation delivery clears retained token/template secrets;
- expiry, revocation, and rotation remain authoritative even if old mail arrives late;
- terminal failure stays visible to Owner and local operators.

No production provider, DNS, sender reputation, bounce processing, or delivery SLA is claimed.

## 9. API And UI Surface

The API adds bounded Notebook list scopes, role-aware snapshots, rename/delete, Member queries and commands, Invitation commands, and token resolve/accept. Mutations use Session, CSRF, stable error envelopes, and idempotency where transport replay matters. Unauthorized resources return not found; invalid token outcomes are indistinguishable outside Owner management.

The Web Client adds:

- owned/shared Library scopes and role labels;
- functional Rename, Manage access, Delete, and Leave actions;
- Member/Invitation capacity and management;
- Invitation resolution, auth return, and acceptance states;
- role-aware Source mutation controls;
- English and Simplified Chinese copy and accessible destructive dialogs.

## 10. Security And Privacy

- Raw Invitation tokens are not stored in Invitation rows or logged.
- Mail and resolve reveal no Source or private Chat data.
- Email equality is canonical and server-side.
- Owner management does not weaken Chat creator RLS.
- Viewer read does not imply original-file download.
- Worker mail credentials grant only required Outbox access.
- Revocation is synchronous authority; asynchronous mail and purge cannot restore access.
- RLS tests attempt direct cross-role and cross-creator access.

## 11. Migration

The migration preserves every existing Owner, expands the role constraint, adds Invitation and Outbox state, replaces Owner-only Notebook/Source policies with the role matrix, and preserves Chat creator isolation. It supports clean and populated Sprint 6 databases and requires old processes to stop for authorization cutover.

No existing Notebook, Source, Chat, Message, Run, Citation, Trace identity, or retrieval index is rewritten solely for sharing.

## 12. Verification Gates

### 12.1 Domain And Database

- capability matrix unit tests;
- real-PostgreSQL migration, RLS, capacity, idempotency, concurrency, and ownership tests;
- token digest, expiry, rotation, replay, and email-match tests;
- removal and delete atomicity tests.

### 12.2 Mail

- Outbox claim, lease loss, retry, uncertain ACK, success scrubbing, and terminal failure;
- in-memory Mailer contracts;
- real local SMTP capture acceptance.

### 12.3 Product Journeys

- invite an unregistered Viewer, register, accept, inspect Source, and use private Chat;
- invite an Editor and maintain Sources without access-management authority;
- prove Owner cannot read another Member's Chat;
- change role and refresh stale UI authority;
- remove and leave during a Run, proving cancellation and no late publication;
- transfer ownership without losing private history;
- delete a shared Notebook and verify revocation, purge intent, and notifications.

### 12.4 Regression

- Go format, vet, unit, integration, race, and build;
- Web unit, typecheck, lint, build, accessibility, and Playwright;
- Source-family, RAG Eval, Citation, recovery, Trace, Replay, and Dashboard acceptance;
- no external network dependency in deterministic CI.

Mock-only authorization, lifecycle, or mail tests are insufficient.

## 13. Explicitly Out Of Scope

- production mail provider
- managed OIDC or disabling Local Credentials
- email verification, password reset, or account deletion
- public/anonymous links, group/domain Invitations, or organizations
- shared Chats, Chat export, generated Outputs, or Notes
- production deployment, backup, KMS, alerting, HA, or SLA

## 14. Delivery Sequence

1. freeze role, Invitation, mail, lifecycle, API, and acceptance contracts;
2. migrate Membership roles, Invitation authority, Outbox, capability function, and RLS through TDD;
3. implement Invitation create/resolve/accept/revoke/resend and local mail delivery;
4. implement shared Library and role-aware Notebook/Source access;
5. implement role change and ownership transfer;
6. implement removal/leave with Run cancellation and private Chat deletion;
7. implement Notebook rename/delete with durable purge and notifications;
8. implement the localized responsive Web surface;
9. run security, concurrency, lifecycle, SMTP, browser, and Sprint 1-6 regression gates;
10. audit every success criterion against concrete evidence.

Each slice is independently reviewable and verified. No slice may make mail authoritative, expose another Member's Chat, weaken Source/Citation authorization, or temporarily permit a zero-Owner Notebook.

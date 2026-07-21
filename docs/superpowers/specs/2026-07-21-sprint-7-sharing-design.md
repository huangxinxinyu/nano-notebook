# Sprint 7 Collaboration And Sharing Design

**Date:** 2026-07-21

**Status:** Approved for implementation

**Scope:** Complete the initial-release collaboration model locally: email invitations, Viewer/Editor/Owner authorization, shared Notebook discovery, membership lifecycle, ownership transfer, and the Notebook lifecycle operations required by sharing.

## 1. Decision

Sprint 7 delivers the complete local collaboration boundary already committed in product discovery. A Notebook has shared Sources, exactly one Owner, optional Viewer and Editor Members, and private per-Member Chats.

Invitation email is exercised through a local SMTP capture service. Production email, managed OIDC, and public deployment remain later launch work. PostgreSQL is authoritative for Invitations and Membership; SMTP delivery is never required for an Invitation to exist or be accepted.

```text
Owner creates Viewer/Editor Invitation
  -> Invitation and mail Outbox record commit atomically
  -> Worker delivers a one-time link to the local mailbox
  -> recipient registers or signs in with the invited email
  -> recipient accepts the Invitation
  -> Membership replaces the pending capacity reservation atomically
  -> shared Notebook appears in the recipient's Library
  -> role-derived capabilities control Source and membership operations
  -> every Member creates and reads only their own private Chats
```

## 2. Product Boundary

Sprint 7 includes:

- Viewer, Editor, and Owner roles;
- Invitations addressed to one canonical email;
- seven-day expiry, revocation, and resend after expiry;
- local SMTP delivery and a browser-visible local mailbox;
- owned and shared Notebook Library views;
- role-aware Source and Chat behavior;
- Member role changes, removal, voluntary leave, and ownership transfer;
- Notebook rename and permanent deletion;
- cancellation and deletion behavior caused by removal, leave, or Notebook deletion;
- local notification email for Notebook deletion;
- English and Simplified Chinese UI, responsive behavior, accessibility, and browser acceptance.

Sprint 7 excludes production mail providers, OIDC, public links, anonymous or group invitations, shared Chats, account deletion, and production deployment.

## 3. Canonical Model

### 3.1 Membership

`Membership` is the authoritative relationship between one User and one Notebook.

| Role | Capabilities |
| --- | --- |
| Viewer | Read ready Sources and manage only the Member's own private Chats and Agent Runs |
| Editor | Viewer capabilities plus add, retry, rename, and delete Sources |
| Owner | Editor capabilities plus Invitations, Members, roles, ownership transfer, Notebook rename, and Notebook deletion |

Every Notebook has exactly one Owner and at most 50 non-Owner Members. Pending, unexpired Invitations reserve the same non-Owner capacity so acceptance cannot oversubscribe the Notebook.

Membership never grants access to another Member's Chat, Messages, Runs, Checkpoints, selected Sources, or Replay. Owner is not a private-Chat superuser.

### 3.2 Invitation

An `Invitation` is a time-bounded offer for one canonical email to join one Notebook as Viewer or Editor. It is not Membership and grants no access before acceptance.

Its lifecycle is `pending`, `accepted`, `revoked`, or `expired`. Commands that list, create, accept, revoke, or resend first terminalize observed due Invitations. Exactly one unexpired pending Invitation may exist for a Notebook and canonical email.

An expired Invitation may be resent. Resend rotates the bearer token, starts a new seven-day interval, and creates a new mail-delivery generation. Accepted or revoked Invitations cannot be resent; the Owner creates a new Invitation if appropriate.

### 3.3 Invitation Token

The email contains a cryptographically random, single-use bearer token. Only its SHA-256 digest is stored with the Invitation. The raw token appears in the URL fragment rather than the request path or query, and the SPA submits it in an explicit request body.

An unauthenticated resolve operation returns only Invitation validity, Notebook title, target role, and a masked recipient email. Acceptance requires an authenticated Session, CSRF validation, a valid pending token, and exact canonical equality between the User email and invited email. Forwarding the link cannot grant another account access.

## 4. Authority And Components

### 4.1 Notebook Module

The Notebook Module owns Notebook metadata, Membership, Invitation, capacity, role-derived capabilities, ownership transfer, rename, leave, removal, and Notebook deletion orchestration. Handlers use its commands and queries rather than editing membership tables directly.

### 4.2 Identity Module

Identity remains authoritative for canonical User email and Session identity. Invitation acceptance reads the current canonical email inside the acceptance transaction. Sprint 7 creates no second invitation-specific User identity.

### 4.3 Mail Adapter And Outbox

A narrow `Mailer` adapter accepts stable message identity, recipient, subject, and text/HTML bodies. Business code does not speak SMTP directly.

Invitation creation/resend and Notebook deletion notification write a durable mail Outbox row in the same transaction as their product state. A Worker sender claims records with a lease, sends through the configured adapter, and records success or bounded retry state.

SMTP has an uncertain-acknowledgement boundary, so at-least-once delivery may create duplicates. Each generation uses a stable RFC Message-ID. Product correctness never depends on mailbox deduplication.

Local Compose adds an SMTP capture service bound to loopback with a browser UI. Automated tests use an in-memory recording adapter; a Compose acceptance proves actual SMTP delivery without external network access.

### 4.4 Chat And Agent Modules

Chat remains private by `creator_user_id`. Viewer, Editor, and Owner have identical authority over their own Chats.

Member removal or leave uses a Notebook-owned transaction coordinator to lock Membership, terminalize that Member's queued/running Runs and Jobs in the Notebook as `cancelled`, record truthful Run terminal observations, notify Run listeners, delete the Member's private Chats, and delete Membership.

The cancellation commit fences late publication. Existing Chat deletion triggers enqueue Collector Trace purge. Shared Sources and Source-processing Jobs survive an Editor leaving because they belong to the Notebook.

Notebook deletion applies the same cancellation rule to all active Runs, creates durable Source/Object/Projection purge intent before authoritative rows disappear, enqueues notification mail for non-Owner Members, then deletes the Notebook graph.

## 5. PostgreSQL Design

### 5.1 Membership Migration

The existing Owner-only role check expands to `viewer`, `editor`, and `owner`. Existing rows remain Owner rows. The partial unique index enforcing one Owner remains.

Database constraints prevent direct deletion or demotion of the sole Owner. The ownership-transfer command changes both affected Membership rows within one transaction. Application and RLS roles cannot commit a zero-Owner or multi-Owner Notebook.

### 5.2 Invitation Table

`notebook_invitations` contains stable Invitation and Notebook IDs, canonical/display email, target role, token digest and generation, state, inviter, accepted User when applicable, lifecycle timestamps, and a conditional-update version.

Indexes support Notebook management, token lookup, observed expiry, and active-email conflict detection. Token digests are never returned through APIs.

### 5.3 Mail Outbox

`platform_mail_outbox` contains stable message identity, semantic kind, recipient, locale, bounded template data, state, attempt count, availability time, lease identity/expiry, safe error code, and timestamps.

Raw Invitation tokens exist in the Outbox only until successful delivery or terminal expiry. Success clears sensitive template data while retaining delivery metadata. Revocation and token rotation invalidate old mail even if delayed. Failed rows use bounded retry and become terminal after the configured local limit.

### 5.4 Capability Function And RLS

`nano_has_notebook_capability` maps:

- `notebook.read`, `source.read`, and private research access to Viewer, Editor, and Owner;
- `source.maintain` to Editor and Owner;
- `notebook.manage` to Owner.

RLS remains the final boundary. An application Principal sees their own Membership or, when Owner, the Memberships and Invitations managed inside that Notebook. Token resolution uses a narrow operation returning no Source, Membership, or private Chat data. Worker policies expose only reauthorization data and claimable mail rows.

## 6. Transaction And Concurrency Semantics

Every membership-capacity mutation takes one transaction-scoped advisory lock derived from Notebook ID and locks affected rows. This serializes create, accept, revoke, resend, remove, leave, and transfer without locking unrelated Notebooks.

### 6.1 Create Invitation

Creation requires Owner authority and an idempotency key, terminalizes observed expiry, rejects self-invitation, existing Membership, or an effective duplicate, counts non-Owner Members plus pending Invitations, enforces capacity 50, and atomically inserts Invitation and Outbox rows.

Transport replay returns the same Invitation; reusing a key for another request conflicts.

### 6.2 Accept Invitation

Acceptance locks Invitation and capacity, rejects invalid or email-mismatched tokens through one safe failure class, and inserts Membership while marking the Invitation accepted in one transaction. The reserved slot becomes a Member slot. Concurrent replay creates exactly one Membership and returns one stable outcome.

### 6.3 Revoke And Resend

Revocation changes only an effective pending Invitation. Resend applies only after expiry when the email is not a Member and capacity remains. It rotates token material and creates a new Outbox generation atomically; old links stay invalid.

### 6.4 Role Change And Ownership Transfer

Ordinary role change switches a non-Owner between Viewer and Editor. Ownership transfer promotes an existing Viewer or Editor and demotes the former Owner to Editor in one transaction. It preserves both Members' private Chats and Runs.

### 6.5 Removal And Leave

Owner may remove only a non-Owner. A non-Owner may leave. Owner must transfer ownership or delete the Notebook.

Access revocation, active Run/Job cancellation, private Chat deletion, Membership deletion, listener notification, and Trace purge intent share one authoritative transaction boundary.

### 6.6 Notebook Rename And Delete

Only Owner may rename or delete. Rename retains the 160-rune title limit. Delete requires an explicit confirmation plus idempotency key, cancels all Runs, creates purge and notification work, and removes the graph without depending on SMTP or Collector availability.

## 7. HTTP Contract

All management mutations require same-origin Session and CSRF. Replayable commands use idempotency keys.

```text
GET    /api/v1/notebooks?scope=all|owned|shared&query=&cursor=&limit=
GET    /api/v1/notebooks/{notebook_id}
PATCH  /api/v1/notebooks/{notebook_id}
DELETE /api/v1/notebooks/{notebook_id}

GET    /api/v1/notebooks/{notebook_id}/members
PATCH  /api/v1/notebooks/{notebook_id}/members/{user_id}
DELETE /api/v1/notebooks/{notebook_id}/members/{user_id}
POST   /api/v1/notebooks/{notebook_id}/members/{user_id}/transfer-ownership
POST   /api/v1/notebooks/{notebook_id}/leave

POST   /api/v1/notebooks/{notebook_id}/invitations
DELETE /api/v1/notebooks/{notebook_id}/invitations/{invitation_id}
POST   /api/v1/notebooks/{notebook_id}/invitations/{invitation_id}/resend

POST   /api/v1/invitations/resolve
POST   /api/v1/invitations/accept
```

Notebook responses include the caller's role and presentation capabilities. The server remains authoritative. Unauthorized existence-sensitive access returns the existing not-found envelope. Token failures are deliberately indistinguishable outside authenticated Owner management.

## 8. Web Experience

The Library activates its owned/shared information architecture. Search applies within the selected scope, rows show role, and Owner actions for Rename, Share, and Delete become functional. Shared rows provide Open and Leave where permitted.

The workspace uses role-aware presentation: Viewer gets Source read and Citation navigation; Editor and Owner get Source mutation; Owner gets Manage access; non-Owner gets Leave; all roles see only their own Chats.

Manage access shows current Members, manageable Invitation states, capacity, Invite, role change, remove, resend-after-expiry, revoke, and transfer. Non-Owners cannot obtain Member email lists.

Invitation links show bounded metadata, return an anonymous recipient through registration/sign-in, and navigate to the Notebook after acceptance. Invalid outcomes use safe localized states.

Stale UI authority is expected. A denied mutation displays a safe error and refreshes the current Notebook snapshot.

## 9. Error Handling

- Validation and role failures use stable codes and localized keys.
- Authorization loss is not found on resource APIs.
- Capacity and duplicate conflicts return `409`.
- SMTP failure changes only Outbox state; Invitation remains manageable.
- Expired or rotated tokens cannot be revived by delayed mail.
- Removal/deletion aborts if authoritative cancellation or purge-intent creation fails.
- Notification failure never restores deleted authority.
- Worker lease loss fences Outbox completion updates.

## 10. Migration And Compatibility

The migration upgrades clean and populated Sprint 6 databases without changing existing ownership or private Chats. It adds Invitation and mail Outbox tables, expands Membership roles, replaces Owner-only Notebook/capability RLS, adds local SMTP configuration, and then activates the Web surface.

Old application and Worker processes stop before the authorization cutover. Existing Notebook, Source, Chat, Run, Citation, Trace, and retrieval identities are not rewritten.

## 11. Verification

Verification includes:

- role/capability unit tests;
- clean and populated migration tests;
- real-PostgreSQL RLS and direct-SQL attack tests;
- single-Owner, capacity, idempotency, token, expiry, rotation, and concurrency tests;
- removal/leave cancellation, late-publication fencing, private Chat deletion, and shared Source preservation;
- ownership transfer and Notebook deletion atomicity;
- Outbox claim, lease, retry, uncertain ACK, payload clearing, and terminal failure;
- real local SMTP capture acceptance;
- English/Chinese Web tests, accessibility, responsive behavior, and browser journeys;
- full Go/Web gates and Sprint 1-6 regression, including RAG, Citation, Trace, Replay, and Eval.

## 12. Acceptance Boundary

Sprint 7 is complete only when the local product satisfies the Shared Research journey in `docs/product-discovery/REQUIREMENTS.md`, role and private-Chat boundaries hold in Go and PostgreSQL RLS, Invitation email is observable in the local mailbox, and destructive collaboration transitions have verified cancellation and purge behavior.

Production launch remains blocked on the production gates in `docs/technical-architecture/ARCHITECTURE.md`.

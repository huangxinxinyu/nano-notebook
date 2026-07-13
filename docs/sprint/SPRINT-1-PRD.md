# Nano Notebook Sprint 1 PRD

## Document Status

- **Sprint:** Sprint 1
- **Status:** Ready for review
- **Date:** 2026-07-13
- **Theme:** Runnable foundation and first product entry
- **Document boundary:** This PRD adds Sprint 1 scope without changing or superseding any existing product, architecture, or engineering document.

## 1. Background

Nano Notebook currently has an approved product definition and a set of technical architecture decisions, but no runnable application code. Sprint 1 establishes the smallest credible product foundation: a developer can start the system locally, and a user can register, sign in, create a Notebook, find it in the Library, enter its empty workspace, and sign out.

Sprint 1 is not a compressed version of the initial release. Source ingestion, retrieval, Agent execution, Citations, sharing, and production deployment remain later work. This Sprint creates the product and architecture seam those capabilities will extend.

## 2. Source Documents

Sprint 1 derives from the following read-only sources:

- `docs/product-discovery/CONTEXT.md`
- `docs/product-discovery/DISCOVERY.md`
- `docs/product-discovery/REQUIREMENTS.md`
- `docs/product-discovery/TECHNICAL-HANDOFF.md`
- `docs/technical-architecture/CONTEXT.md`
- `docs/technical-architecture/adr/0004-modular-monolith-with-workers.md`
- `docs/technical-architecture/adr/0005-use-s3-api-for-blob-storage.md`
- `docs/technical-architecture/adr/0006-use-postgresql-as-system-of-record.md`
- `docs/technical-architecture/adr/0013-assign-authoritative-data-to-go-modules.md`
- `docs/technical-architecture/adr/0015-use-local-credentials-before-managed-oidc.md`
- `docs/technical-architecture/adr/0016-use-revocable-opaque-application-sessions.md`
- `docs/technical-architecture/adr/0025-use-a-react-typescript-spa.md`
- `docs/technical-architecture/adr/0028-run-local-infrastructure-in-compose.md`
- `docs/engineering/BACKEND_ENGINEERING.md`

If this PRD conflicts with an approved source document, the approved source document wins.

## 3. Sprint Goal

Deliver a repeatable local Nano Notebook application and the first complete user journey:

> Start the project → register → sign in → create a Notebook → find and open it → see the empty Notebook workspace → sign out → sign in again and recover the same Notebook.

## 4. Success Criteria

Sprint 1 succeeds when all of the following are true:

1. A developer can start the required local infrastructure and all three application processes—Web Client, Control Plane, and Worker—from a clean checkout by following repository-owned instructions.
2. No model Provider credential is required to complete the Sprint 1 journey.
3. A new user can complete the primary journey without database editing, fixture injection, or direct API calls.
4. A created Notebook remains available after browser reload and process restart.
5. Anonymous users cannot read or mutate Notebook data.
6. The frontend uses adopted open-source components rather than a newly invented base component system.
7. The Notebook Library and workspace are recognizably aligned with NotebookLM's desktop information hierarchy while retaining Nano Notebook terminology and identity.

## 5. Target User

The Sprint serves an individual researcher or deep learner beginning a new research topic. Collaboration is not yet available, but the data model must create the user as the Notebook's single Owner so later membership work does not require rewriting Notebook ownership.

## 6. Primary User Journey

1. The user opens Nano Notebook while signed out.
2. The user registers with an email address and password using the local-development credential flow.
3. Registration creates an application Session and enters the Notebook Library.
4. The empty Library clearly offers creation of the first Notebook.
5. The user enters a title and creates the Notebook.
6. The new Notebook appears in the Library and opens into the Notebook workspace.
7. The workspace establishes the Sources, Chat, and future Outputs regions without pretending that later capabilities are functional.
8. The user returns to the Library and can find the Notebook by title.
9. The user signs out, signs in again, and sees the same Notebook.

## 7. In Scope

### 7.1 Runnable Local System

- A React and TypeScript Web Client built as the Vite SPA selected by the architecture.
- A Go Control Plane process with versioned REST endpoints.
- A separate Go Worker process that starts, reports health, and shuts down cleanly. It does not execute Source or Agent Jobs in this Sprint.
- Docker Compose definitions for the architecture's local dependencies: PostgreSQL, MinIO, Qdrant, Bifrost, and Jaeger all-in-one as the local OpenTelemetry-compatible trace backend.
- Repository-owned bootstrap, migration, seed, start, stop, and health-check entry points.
- Idempotent database migration execution.
- Readiness that distinguishes a live process from a process ready to serve requests.
- Structured request logging with correlation IDs and no credential or Session leakage.
- Graceful shutdown for the Control Plane and Worker.
- A documented clean-start path and a documented reset path for local development data.

Only PostgreSQL is authoritative and product-active in Sprint 1. MinIO, Qdrant, and Bifrost must start and expose health but are not called by the user journey. The Control Plane and Worker export Sprint 1 operational traces to Jaeger through OTLP. Missing model API keys must not prevent local startup because no model capability is in scope.

### 7.2 Local Registration and Sessions

- Register with email and password.
- Sign in with email and password.
- Sign out from the current Session.
- Restore the signed-in application state after reload.
- Store User identity separately from Local Credentials.
- Store only a strong password hash, never the original password.
- Require passwords of 15–128 Unicode characters, allow spaces and password-manager paste/autofill, and impose no character-class composition rule.
- Reject commonly used or compromised passwords through a local blocklist and rate-limit failed sign-in attempts.
- Use a random opaque cookie whose hashed Session record is stored in PostgreSQL.
- Apply secure cookie defaults appropriate to the local HTTP/production HTTPS distinction.
- Reject duplicate email registration without revealing sensitive account details beyond the current registration attempt.
- Return a stable, localized error for invalid credentials and expired or revoked Sessions.

Local Credentials are a local product-completion mechanism only. Password reset, email verification, social login, managed OIDC, and public deployment are outside Sprint 1.

### 7.3 Notebook Library

- Display the signed-in user's owned Notebooks.
- Provide an honest empty state for a user with no Notebooks.
- Create a Notebook from its title.
- Atomically create the Notebook and its Owner membership.
- Enforce the existing limit of 100 owned Notebooks per user.
- Open a Notebook from the Library.
- Search the visible Library by title.
- Order the default Library view by recent activity.
- Preserve Library state across reload and sign-in.
- Provide clear loading, empty, validation, quota, and server-error states.

Shared Notebooks are not shown as a non-functional tab in Sprint 1. The owned/shared separation is introduced when sharing produces real shared data.

### 7.4 Empty Notebook Workspace

- Display the Notebook title and a clear route back to the Library.
- Establish the desktop information hierarchy of Sources, Chat, and Outputs.
- Use NotebookLM-like panel proportions, surface hierarchy, headers, spacing, and responsive collapse behavior.
- Sources and Chat may show concise empty states that explain why they cannot yet be used.
- Outputs may reserve its stable region but must contain no generation controls, templates, promotional cards, or other dead actions.
- Do not show upload, ask, invite, share, model, Source-selection, or Output-generation controls before those behaviors exist.
- A direct request for a missing or inaccessible Notebook returns a safe not-found state rather than leaking whether another user's Notebook exists.

### 7.5 Simplified Chinese and English

- All Sprint 1 product strings exist in Simplified Chinese and English.
- The default language follows the browser locale with a user-visible language switch.
- Validation, authentication errors, quota messages, empty states, dates, and accessibility labels are localized.

## 8. Frontend Product and Reuse Requirements

### 8.1 Visual Reference

NotebookLM is the primary reference for:

- Notebook Library density and hierarchy;
- the desktop Notebook workspace's panel model;
- quiet Material-like surfaces, restrained elevation, rounded containers, and compact controls;
- empty-state tone;
- responsive transition from desktop columns to mobile tabs or single-panel navigation.

The implementation must use the current live NotebookLM desktop product as the visual reference at the start of implementation. Reference screenshots used for review must record their capture date and viewport.

Nano Notebook must not copy Google's name, logo, proprietary illustrations, or other brand assets. The goal is interaction and layout fidelity, not impersonation.

### 8.2 Adopted Component System

Sprint 1 adopts the open-source [Open Notebook](https://github.com/lfnovo/open-notebook) frontend as the component and composition donor, pinned for evaluation to commit [`7dfe8aa`](https://github.com/lfnovo/open-notebook/tree/7dfe8aa0a755954fd67408616c9ec4dc3cf54ddc). At that revision, Open Notebook uses:

- [shadcn/ui](https://ui.shadcn.com/) with the `new-york` style and CSS variables;
- [Radix UI](https://www.radix-ui.com/primitives) interaction primitives;
- Tailwind CSS v4;
- Lucide React icons;
- React Hook Form and Zod for form behavior;
- TanStack Query for server state;
- Sonner for transient notifications.

The evidence for this selection is Open Notebook's [`components.json`](https://github.com/lfnovo/open-notebook/blob/7dfe8aa0a755954fd67408616c9ec4dc3cf54ddc/frontend/components.json), [`package.json`](https://github.com/lfnovo/open-notebook/blob/7dfe8aa0a755954fd67408616c9ec4dc3cf54ddc/frontend/package.json), and [MIT license](https://github.com/lfnovo/open-notebook/blob/7dfe8aa0a755954fd67408616c9ec4dc3cf54ddc/LICENSE).

This choice replaces the earlier Material UI option. It avoids a new base component system and provides existing Notebook-oriented compositions closer to the target product.

### 8.3 Reuse Boundary

The implementation should reuse or port, rather than redesign, the following Open Notebook assets where they fit the Nano Notebook requirement:

- Button, input, label, card, dialog, alert dialog, dropdown menu, tabs, tooltip, popover, scroll area, separator, alert, progress, and toast primitives;
- loading, error, empty-state, confirmation, and inline-edit patterns;
- application shell and responsive Notebook column patterns;
- Notebook list/card presentation;
- create-Notebook dialog composition.

The implementation must not adopt:

- Open Notebook's Next.js routing or server runtime;
- its Python backend, SurrealDB model, or API contracts;
- Notes, Podcasts, model settings, provider settings, global Sources, transformations, or search features;
- business behaviors that conflict with Nano Notebook's immutable Sources, private Chats, role model, or future Outputs boundary.

Ported components must remain ordinary React components compatible with the approved Vite SPA. Routing, API calls, and domain state are thin Nano Notebook adapters around the adopted presentation components.

### 8.4 No-New-Primitive Rule

- Do not create a new base UI primitive when an adopted shadcn/Radix/Open Notebook component meets the requirement.
- Product-specific compositions may combine adopted primitives, but must not fork interaction behavior without a demonstrated need.
- Visual adaptation is performed through centralized design tokens and component variants, not one-off page CSS.
- A new primitive requires an explicit implementation note describing the missing capability and why composition was insufficient.

### 8.5 License Compliance

- Preserve the Open Notebook MIT copyright notice when copying substantial source.
- Record copied or materially adapted files and their pinned upstream commit in a repository third-party notice.
- Keep direct dependency licenses in the lockfile and dependency inventory.
- Do not copy Google proprietary application code or assets.

## 9. Functional Requirements

### 9.1 Authentication Rules

- Email input is trimmed and canonicalized to lowercase for uniqueness and sign-in comparison while preserving the submitted display value separately if needed.
- Registration and sign-in must be safe to retry without creating duplicate Users or Sessions unexpectedly.
- Successful sign-out revokes the server-side Session before the browser returns to the signed-out state.
- Every protected REST handler resolves its Principal from the Session; it never trusts a client-supplied user ID.

### 9.2 Notebook Rules

- A Notebook has a stable opaque identifier and a title.
- Every Notebook has exactly one Owner from creation.
- The Notebook Module owns creation, listing, lookup, quota enforcement, and Owner membership creation.
- A user only lists and opens Notebooks authorized for that Principal.
- Search and ordering operate only within the authorized result set.

### 9.3 API Behavior

- REST responses use one stable error envelope with a machine-readable code, localized-message key or safe message, and request ID.
- Create operations are retry-safe through an idempotency key or an equivalent durable uniqueness mechanism.
- Browser requests use same-origin cookie authentication and appropriate CSRF protection.
- List endpoints have a bounded result size even though the Sprint 1 ownership limit is 100.

## 10. Required Product States

The frontend must visibly and accessibly handle:

- application bootstrap;
- signed out;
- registration submitting, success, duplicate email, validation failure, and server failure;
- sign-in submitting, invalid credentials, expired Session, and server failure;
- empty Library;
- Library loading, loaded, no search match, quota reached, and failure;
- Notebook creation idle, submitting, validation failure, retryable failure, and success;
- Notebook loading, loaded, not found/inaccessible, and failure;
- sign-out in progress and complete;
- Control Plane unreachable.

Loading indicators must not replace stable page structure unnecessarily. Errors must provide a safe next action such as retry, return to Library, or sign in again.

## 11. Accessibility and Responsive Requirements

- All interactive elements are reachable and operable by keyboard.
- Focus is visible and follows dialog, form-error, sign-in, and navigation changes correctly.
- Form controls have programmatic labels and errors.
- Icon-only buttons have localized accessible names and tooltips where their meaning is not obvious.
- Color is not the only carrier of state.
- Text and interactive controls meet WCAG 2.2 AA contrast expectations.
- Desktop acceptance viewport: 1440 × 900.
- Compact/mobile acceptance viewport: 390 × 844.
- The Library remains usable without horizontal scrolling at the compact viewport.
- The Notebook workspace changes from columns to an adopted tabbed or single-panel navigation pattern at compact widths.

## 12. Out of Scope

- Source upload, pasted text, URLs, YouTube, audio, images, extraction, or inspection
- Blob upload intents and active MinIO use
- Source Processing Jobs or Worker job claiming
- Evidence Units, Evidence Revisions, Retrieval Chunks, or Citations
- Qdrant indexing, retrieval, reranking, or RAG
- Bifrost model calls or Provider configuration UI
- Chat creation, messages, Source selection, response controls, or suggestions
- Agent Runs, SSE, Reasoning Traces, durable Agent Traces, or evaluation
- Notebook rename, deletion, leaving, or account deletion
- Invitations, membership management, Viewer/Editor behavior, or ownership transfer
- Functional Outputs or Output generation controls
- Password reset or email verification
- Managed OIDC, production deployment, or public internet exposure
- Dark mode unless it comes at negligible cost from the adopted theme without delaying acceptance
- Storybook or a standalone design-system site

## 13. Acceptance Scenarios

### A1. Clean Local Start

**Given** a supported development machine with the documented prerequisites and no existing Nano Notebook data
**When** the developer follows the repository's clean-start instructions
**Then** PostgreSQL, MinIO, Qdrant, Bifrost, Jaeger, Control Plane, Worker, and Web Client become healthy, and the browser can open the signed-out application without Provider credentials.

### A2. Register and Restore Session

**Given** a visitor with no account
**When** they register with a valid email and password
**Then** they enter the empty Library, and reloading the browser restores the authenticated Session.

### A3. Create the First Notebook

**Given** an authenticated user with an empty Library
**When** they create a Notebook with a title
**Then** the Notebook and Owner membership are committed atomically, it appears in the Library, and it can be opened.

### A4. Recover Durable State

**Given** a user has created a Notebook
**When** the application processes restart and the user signs in again
**Then** the Notebook remains visible and opens successfully.

### A5. Search and Recent Ordering

**Given** the user owns multiple Notebooks
**When** they search by a title fragment
**Then** only matching authorized Notebooks are shown; clearing search restores the recent-activity ordering.

### A6. Authorization Isolation

**Given** two registered users and a Notebook owned by the first user
**When** the second user requests the first user's Notebook identifier directly
**Then** no Notebook data is returned and the response does not reveal whether the Notebook exists.

### A7. Sign Out

**Given** an authenticated user
**When** they sign out
**Then** the server-side Session is revoked, protected routes return to sign-in, and reuse of the old cookie fails.

### A8. Visual and Component Compliance

**Given** the Library and empty Notebook workspace at the two acceptance viewports
**When** reviewers compare them with date-stamped NotebookLM references and the adopted Open Notebook compositions
**Then** information hierarchy, column behavior, spacing rhythm, surface treatment, controls, empty states, and responsive navigation are visibly aligned; the implementation uses adopted components with no parallel custom primitive set.

### A9. Localization and Accessibility

**Given** either Simplified Chinese or English
**When** the user completes the primary journey using keyboard-only navigation
**Then** all visible strings and accessible names use the selected language, focus remains visible and logical, and no step requires pointer input.

## 14. Definition of Done

- All acceptance scenarios pass locally.
- The primary journey has an automated browser test.
- Identity, Session, Notebook, and Owner-membership persistence have real-PostgreSQL integration tests.
- Authorization isolation and Session revocation have negative-path integration tests.
- Pure validation and capability rules have unit tests.
- Database migrations run from empty state and can be reapplied safely.
- Web Client type-check, lint, unit tests, and production build pass.
- Go formatting, static analysis, unit tests, integration tests, and builds pass.
- Local startup and shutdown complete without unhandled errors or leaked secrets.
- No Product, Source, Chat, Agent, Retrieval, or Output behavior outside this PRD has been implied by a dead control.
- Ported Open Notebook source has the required MIT attribution and pinned provenance.
- Existing product, architecture, and engineering documents remain unchanged.

## 15. Risks and Controls

| Risk | Control |
| --- | --- |
| Copying the entire Open Notebook frontend imports conflicting product behavior and Next.js assumptions | Port only approved React presentation components; keep Vite routing, Nano Notebook APIs, and domain state independent. |
| “Match NotebookLM” becomes subjective | Capture date-stamped references at fixed viewports and review named layout, state, and responsive criteria. |
| Component reuse turns into an unmaintained fork | Pin upstream provenance, minimize changes, and place visual differences in centralized tokens and variants. |
| Local infrastructure overwhelms the first product slice | Require health and reproducible startup, but activate only PostgreSQL in the Sprint 1 business journey. |
| Local Credentials are mistaken for production authentication | Label them as local-only and keep managed OIDC plus disabling Local Credentials as a production-launch gate. |
| Empty future areas mislead users | Render no action that cannot complete; reserve structure without promotional or disabled feature controls. |

## 16. Exit Boundary and Next Sprint

Sprint 1 ends with a durable empty Notebook, not a research workflow. The next Sprint should select one narrow Source path—preferably pasted text—to exercise the Source Module, Blob/Evidence boundary as appropriate, durable Source Processing state, Worker execution, and visible `Processing`, `Ready`, and `Failed` states before retrieval or Agent work is introduced.

## 17. External References

- [NotebookLM: create a notebook and use Sources, Chat, and Studio](https://support.google.com/notebooklm/answer/16206563?hl=en)
- [NotebookLM: source-grounded chat and source selection](https://support.google.com/notebooklm/answer/16179559?hl=en)
- [Open Notebook repository](https://github.com/lfnovo/open-notebook)
- [Open Notebook frontend dependency manifest at the evaluated revision](https://github.com/lfnovo/open-notebook/blob/7dfe8aa0a755954fd67408616c9ec4dc3cf54ddc/frontend/package.json)
- [Open Notebook shadcn configuration at the evaluated revision](https://github.com/lfnovo/open-notebook/blob/7dfe8aa0a755954fd67408616c9ec4dc3cf54ddc/frontend/components.json)
- [Open Notebook MIT license](https://github.com/lfnovo/open-notebook/blob/7dfe8aa0a755954fd67408616c9ec4dc3cf54ddc/LICENSE)
- [shadcn/ui](https://ui.shadcn.com/)
- [Radix UI Primitives](https://www.radix-ui.com/primitives)
- [NIST SP 800-63B password requirements](https://pages.nist.gov/800-63-4/sp800-63b.html#passwords)
- [Jaeger all-in-one local setup](https://www.jaegertracing.io/docs/latest/getting-started/)

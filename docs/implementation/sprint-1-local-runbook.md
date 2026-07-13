# Sprint 1 Local Runbook

## Prerequisites

- Go 1.24 or newer
- Node.js 22 or newer and npm
- Docker with Compose

No model Provider credentials are required for Sprint 1.

## Clean Start

```bash
scripts/reset
scripts/bootstrap
scripts/start
```

Open:

- Web Client: http://localhost:5173
- Control Plane live: http://localhost:8080/health/live
- Control Plane ready: http://localhost:8080/health/ready
- Worker live: http://localhost:8081/health/live
- Worker ready: http://localhost:8081/health/ready
- PostgreSQL: localhost:55432
- MinIO API: http://localhost:59000/minio/health/live
- Qdrant ready: http://localhost:56333/readyz
- Bifrost gateway: http://localhost:56666
- Jaeger UI: http://localhost:51686

Use browser registration from an empty state. There is no required seed account.

## Verification Commands

```bash
NANO_TEST_DATABASE_URL='postgres://nano:nano@localhost:55432/nano_test?sslmode=disable' scripts/test-go
scripts/test-web
npm --prefix web run test:e2e
scripts/health
scripts/stop
```

The health command verifies PostgreSQL, MinIO, Qdrant, Bifrost, Jaeger, Control Plane, Worker, and Web readiness. The primary browser journey covers registration, session restore by server cookie, Notebook creation, search, workspace open, return to Library, sign-out, sign-in, and recovery. Playwright defines both acceptance viewports: `1440x900` and `390x844`.

## Reset

```bash
scripts/reset
```

This stops local application processes and removes Compose volumes.

## Locale and Keyboard Checks

- Browser locale defaults to Simplified Chinese when `navigator.language` starts with `zh`; otherwise English is used.
- The visible language switch toggles English and Simplified Chinese.
- Forms, dialog controls, Library cards, workspace tabs, language switch, and sign-out are keyboard reachable with visible focus.

## Observability

Application logs are structured through Go `slog` and include request IDs, method, and path. Passwords, cookies, session tokens, and authorization headers are never logged. Control Plane and Worker export OpenTelemetry spans to Jaeger through OTLP HTTP at `http://localhost:54318/v1/traces` by default. A smoke check can call `scripts/health` and then query `http://localhost:51686/api/services`; the expected service names are `nano-control-plane` and `nano-worker`.

## Visual Evidence

Candidate comparison screenshots are stored under `docs/visual-references/notebooklm/2026-07-13/candidate/` for Library and workspace views at `1440x900` and `390x844`. The adjacent README records the date, reference sources, viewport targets, and notes for QA recapture without storing Google proprietary assets.

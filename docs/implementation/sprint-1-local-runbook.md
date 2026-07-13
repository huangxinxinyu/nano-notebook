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

The primary browser journey covers registration, session restore by server cookie, Notebook creation, search, workspace open, return to Library, sign-out, sign-in, and recovery. Playwright defines both acceptance viewports: `1440x900` and `390x844`.

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

Application logs are structured through Go `slog` and include request IDs, method, and path. Passwords, cookies, session tokens, and authorization headers are never logged. Jaeger all-in-one is present with OTLP ports enabled for the local trace backend.


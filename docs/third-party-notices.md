# Third-Party Notices

## Open Notebook

Nano Notebook Sprint 1 evaluated Open Notebook at commit `7dfe8aa0a755954fd67408616c9ec4dc3cf54ddc` as the approved component and composition donor.

Open Notebook is licensed under the MIT License. No upstream source file was copied verbatim in this candidate. The adopted frontend stack and provenance are recorded in `docs/implementation/sprint-1-open-notebook-provenance.md`.

## Frontend Runtime Dependencies

The Web Client uses React, Vite, Radix UI, TanStack Query, Lucide React, React Hook Form, Zod, and Sonner. Exact versions and transitive dependency license metadata are pinned in `web/package-lock.json`.

The Web Client lint toolchain uses ESLint, the official JavaScript config package, TypeScript ESLint, React Hooks lint rules, React Refresh lint rules, and browser/node globals metadata. Exact versions are pinned in `web/package-lock.json`.

## Backend Runtime Dependencies

The Go backend uses `github.com/jackc/pgx/v5` for PostgreSQL, `golang.org/x/crypto` for password hashing, and OpenTelemetry Go packages for local OTLP trace export. Exact versions are pinned in `go.sum`.

## Local Infrastructure Images

Docker Compose runs PostgreSQL, MinIO, Qdrant, Bifrost, and Jaeger locally. Bifrost uses `maximhq/bifrost:v1.6.3` with repository-owned file configuration under `infra/bifrost/config.json`; no model Provider credentials are required for Sprint 1.

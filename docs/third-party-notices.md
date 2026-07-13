# Third-Party Notices

## Open Notebook

Nano Notebook Sprint 1 evaluated Open Notebook at commit `7dfe8aa0a755954fd67408616c9ec4dc3cf54ddc` as the approved component and composition donor.

Open Notebook is licensed under the MIT License. No upstream source file was copied verbatim in this candidate. The adopted frontend stack and provenance are recorded in `docs/implementation/sprint-1-open-notebook-provenance.md`.

## Frontend Runtime Dependencies

The Web Client uses React, Vite, Radix UI, TanStack Query, Lucide React, React Hook Form, Zod, and Sonner. Exact versions and transitive dependency license metadata are pinned in `web/package-lock.json`.

## Backend Runtime Dependencies

The Go backend uses `github.com/jackc/pgx/v5` for PostgreSQL and `golang.org/x/crypto` for password hashing. Exact versions are pinned in `go.sum`.


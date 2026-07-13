# Sprint 1 Open Notebook Provenance

- Upstream project: `https://github.com/lfnovo/open-notebook`
- Evaluated commit: `7dfe8aa0a755954fd67408616c9ec4dc3cf54ddc`
- License: MIT
- Evaluation date: 2026-07-13

## Adopted Stack

Sprint 1 adopts the approved Open Notebook frontend stack shape:

- shadcn/ui `new-york` style direction and CSS-variable-friendly primitives
- Radix UI primitives for dialog, tabs, labels, dropdowns, and accessible interaction behavior
- Lucide React icons
- React Hook Form and Zod as approved form/validation dependencies
- TanStack Query for server state
- Sonner for transient toasts

## Copied or Materially Adapted Source

No upstream Open Notebook source file was copied verbatim into this candidate. The implementation recreates compatible Vite React compositions around Radix, TanStack Query, Zod, Sonner, and Lucide while preserving Nano Notebook routing, API contracts, and domain language.

If future work copies or materially adapts upstream files, add each source path, upstream commit, destination path, and adaptation note here before merging.

## No-New-Primitive Note

The product screens use Radix primitives for dialog, tabs, and labels. Product-specific components in `web/src/app/App.tsx` compose those primitives and do not introduce a separate reusable primitive library. Styling is centralized in `web/src/styles.css`.


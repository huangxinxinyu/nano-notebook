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
- Zod for validation and React Hook Form for auth and notebook creation form state
- TanStack Query for server state
- Sonner for transient toasts

## Copied or Materially Adapted Source

No upstream Open Notebook source file was copied verbatim into this candidate. The implementation recreates compatible Vite React compositions around Radix, TanStack Query, Zod, Sonner, and Lucide while preserving Nano Notebook routing, API contracts, and domain language.

If future work copies or materially adapts upstream files, add each source path, upstream commit, destination path, and adaptation note here before merging.

## Primitive Composition Boundary

The product screens use Radix primitives for dialog, tabs, and labels, plus Lucide icons, Sonner toasts, React Hook Form field registration, and Zod validation. Nano-specific reusable UI compositions live in `web/src/components/ui/`:

- `Button` centralizes the accepted button/card variants used by form submits, icon-text actions, and Notebook cards.
- `Field` centralizes Radix label wiring, React Hook Form registration, `aria-invalid`, and field error description behavior.

`web/src/app/App.tsx` now composes those primitives for screen-specific flows while preserving Nano Notebook routing, API contracts, and domain language. Styling remains centralized in `web/src/styles.css`.

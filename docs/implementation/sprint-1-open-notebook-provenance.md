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

The implementation ports the compatible shadcn `new-york` primitives needed for Sprint 1 into the Vite SPA while preserving Nano Notebook routing, API contracts, and domain language. Exact source mapping:

| Upstream source at `7dfe8aa0a755954fd67408616c9ec4dc3cf54ddc` | Nano destination | Adaptation |
| --- | --- | --- |
| `frontend/components.json` | `web/components.json` | Kept `new-york`, `tsx`, CSS variables, neutral base color, and Lucide; changed `rsc` to `false`, CSS path to `src/styles.css`, and retained Vite-compatible aliases as evidence rather than a Next runtime contract. |
| `frontend/src/lib/utils.ts` | `web/src/lib/utils.ts` | Copied `clsx` + `tailwind-merge` `cn` utility; import formatting adapted to repository semicolon style. |
| `frontend/src/components/ui/button.tsx` | `web/src/components/ui/button.tsx` | Ported `Slot`, `class-variance-authority`, `buttonVariants`, `asChild`, size, and upstream variant names. Tailwind utility strings are mapped to centralized `nn-button*` classes because this Vite app preserves its plain CSS runtime. |
| `frontend/src/components/ui/input.tsx` | `web/src/components/ui/input.tsx` | Ported data-slot input primitive and `cn` composition; style tokens moved to `web/src/styles.css`. |
| `frontend/src/components/ui/label.tsx` | `web/src/components/ui/label.tsx` | Ported Radix Label primitive and data-slot shape; removed Next `"use client"` directive. |
| `frontend/src/components/ui/alert.tsx` | `web/src/components/ui/alert.tsx` | Ported `cva` alert variants and title/description composition; style tokens moved to `web/src/styles.css`. |
| `frontend/src/components/ui/card.tsx` | `web/src/components/ui/card.tsx` | Ported card family and data-slot names; style tokens moved to `web/src/styles.css`. |
| `frontend/src/components/ui/tabs.tsx` | `web/src/components/ui/tabs.tsx` | Ported Radix Tabs wrapper components and data-slot names; style tokens moved to `web/src/styles.css`. |
| `frontend/src/components/ui/dialog.tsx` | `web/src/components/ui/dialog.tsx` | Ported Radix Dialog wrapper family, overlay/content/close composition, and Lucide close icon; removed Open Notebook's `useTranslation` dependency and accepts a localized `closeLabel` from Nano callers. |
| `frontend/src/components/ui/sonner.tsx` | `web/src/components/ui/sonner.tsx` | Ported Sonner CSS-variable wrapper; removed Open Notebook theme store dependency because Sprint 1 has no theme setting. |
| `frontend/src/components/ui/tooltip.tsx` | `web/src/components/ui/tooltip.tsx` | Ported Radix Tooltip wrapper family and arrow composition for icon-only controls when later Sprint 1 surfaces need it. |

## Primitive Composition Boundary

The product screens now compose the adopted primitives directly:

- Auth and create-notebook forms use the ported `Label` and `Input` with React Hook Form registration at the screen boundary.
- Auth mode and workspace panels use the ported `Tabs` wrappers over Radix interaction behavior.
- Notebook creation uses the ported `Dialog` wrappers and a localized close label.
- Retryable and validation failures use the ported `Alert` composition.
- Buttons use the ported shadcn `buttonVariants`; screen-specific layout is supplied as composition classes such as `icon-action`, `create-notebook-action`, and `library-item-action`.

No additional base primitive was introduced in this correction. `web/src/styles.css` contains the centralized token layer and component layer classes used by the ported primitives.

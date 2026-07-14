# Design QA: NotebookLM visual-fidelity rebuild

## Comparison target

- Source visual truth:
  - `/Users/huangxinxinyu/.codex/attachments/36162ac2-7667-4d1e-9fba-9c84657704c2/image-1.png` (library)
  - `/Users/huangxinxinyu/.codex/attachments/36162ac2-7667-4d1e-9fba-9c84657704c2/image-2.png` (workspace)
- Browser-rendered implementation:
  - `web/docs/visual-references/notebooklm/2026-07-14/library-final.png`
  - `web/docs/visual-references/notebooklm/2026-07-14/workspace-final.png`
  - `web/docs/visual-references/notebooklm/2026-07-14/library-compact-final.png`
  - `web/docs/visual-references/notebooklm/2026-07-14/workspace-compact-final.png`
  - `web/docs/visual-references/notebooklm/2026-07-14/auth-final.png`
- Viewports: library 1920×1280; workspace 1896×1309; responsive checks at 1440×900, 1280×800, and 390×844.
- State: authenticated Simplified Chinese workspace with three real notebooks; source/chat/Studio capabilities that do not exist in the backend are explicitly marked placeholders.

## Evidence

- Full-view comparisons:
  - `web/docs/visual-references/notebooklm/2026-07-14/library-comparison-final.png`
  - `web/docs/visual-references/notebooklm/2026-07-14/workspace-comparison-final.png`
- Focused dense-UI comparisons:
  - `web/docs/visual-references/notebooklm/2026-07-14/library-focused-final.png`
  - `web/docs/visual-references/notebooklm/2026-07-14/workspace-focused-final.png`
- Earlier comparison evidence:
  - `web/docs/visual-references/notebooklm/2026-07-14/library-comparison-round-1.png`
  - `web/docs/visual-references/notebooklm/2026-07-14/workspace-comparison-round-2.png`

## Findings

No actionable P0, P1, or P2 differences remain.

- [P3] The reference uses NotebookLM branding and proprietary display-font rendering, while the implementation intentionally retains Nano Notebook branding and the approved Google Sans/Roboto/Arial fallback stack. The hierarchy, weights, truncation, and small-control optical balance remain aligned.
- [P3] Real notebook rows show `—` for creation date because the current API does not expose that field. Fabricating dates would violate the real-data boundary; the table keeps the reference column and stable density.
- [P3] The workspace Chat column is an honest empty state instead of the populated conversation in the source. This is the requested product state: `@assistant-ui/react` is selected for the future presentation layer, but no runtime, transport, or reply behavior is mounted.

## Required fidelity surfaces

- Fonts and typography: Google Sans-like family stack, restrained 13–22 px scale, regular section headings, compact control labels, consistent line heights, and title truncation match the reference hierarchy. No cramped or broken wrapping was observed.
- Spacing and layout rhythm: the library uses the measured 1550 px content width; the workspace tracks reproduce the approximately 25% / 50% / 25% composition; header, panel, table-row, divider, radius, and footer-control rhythm align in full and focused comparisons.
- Colors and visual tokens: semantic dark tokens reproduce the near-black canvas, subtly lighter panels, low-contrast borders, selected indigo surfaces, white primary actions, muted copy, and colored Studio cards. No gradients are present.
- Image quality and asset fidelity: the target has no content imagery that the current product can truthfully reproduce. All visible interface icons use locally hosted official Material Symbols; no emoji, handcrafted SVG, CSS illustration, or raster placeholder substitutes are used. Product branding intentionally remains Nano Notebook.
- Copy and content: real notebooks remain real backend data. Featured rows, source tools, Studio outputs, and Chat are clearly isolated placeholders with localized coming-soon feedback. No prompt or implementation instructions leak into visible copy.
- Responsiveness and accessibility: no horizontal overflow at 1896, 1920, 1440, 1280, or 390 px widths. Compact Sources/Chat/Studio tabs keep one panel visible, account/language/logout remain reachable, controls retain accessible names, and keyboard/focus E2E coverage passes.

## Comparison history

1. Round 1 — library
   - Earlier P2 findings: the featured “View all” control was beside the heading instead of below the table; source-count copy lacked localized units; the first capture used only two real rows, which distorted vertical comparison.
   - Fixes: moved the control after the table, localized source counts, and captured a three-row real-backend state.
   - Post-fix evidence: `library-round-2.png`, then `library-comparison-final.png`.
2. Round 2 — workspace and compact behavior
   - Earlier P2 findings: Studio had four card rows instead of the reference five; the slide-deck action was missing; the smallest header hid the only account/language/logout entry.
   - Fixes: added and correctly ordered the slide-deck card, retained the user menu at 390 px, and kept the Chat composer disabled and request-free.
   - Post-fix evidence: `workspace-round-2.png`, `workspace-compact-final.png`, and `workspace-comparison-final.png`.
3. Round 3 — density and final state normalization
   - Earlier P2 findings: library table rows and the featured-section gap were vertically compressed, and an immediate post-navigation capture could observe a stale two-row query snapshot.
   - Fixes: calibrated rows to 59 px, the featured gap to 57 px, and waited for the third real notebook before capture.
   - Post-fix evidence: `library-final.png`, `library-focused-final.png`, and `library-comparison-final.png`.

## Browser checks

- Primary interactions tested: register/sign in, localized language switch, create notebook, return to library, backend search, title sort, notebook open, compact panel tabs, placeholder toasts, user menu, sign out, retry states, dialog focus, and keyboard-only creation.
- Console checked during the final capture. The only console entry was the expected initial unauthenticated `/api/v1/session` 401 probe; no page exceptions or post-auth runtime errors occurred.
- All desktop and compact overflow probes returned `true` for `scrollWidth <= clientWidth`.

## Follow-up polish

- If a licensed local Google Sans asset becomes available, hosting it locally would remove the remaining platform-dependent font fallback variance.
- When the backend exposes notebook creation timestamps, replace the honest `—` value without changing the table contract.

final result: passed

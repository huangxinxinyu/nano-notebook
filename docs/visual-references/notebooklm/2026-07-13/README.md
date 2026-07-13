# NotebookLM Reference Notes

- Capture date: 2026-07-13
- Acceptance viewports: `1440x900` and `390x844`
- Source URLs inspected:
  - https://notebooklm.google.com
  - https://support.google.com/notebooklm/answer/16206563?hl=en
  - https://support.google.com/notebooklm/answer/16179559?hl=en
- Live reference bundle: `live-reference/capture-manifest.json`

## Reference Observations

- A Notebook is framed as a project-specific collection of Sources.
- The notebook workspace model distinguishes Sources, Chat, and a generated-output area called Studio.
- Chat operates over selected Sources and includes source-grounded responses and citations in the full product.
- Studio/output behaviors are outside Nano Notebook Sprint 1, so the candidate preserves an Outputs region without dead generation controls.
- Sprint 1 uses the information hierarchy and panel relationship as reference material only. It does not copy Google names, logos, illustrations, code, or assets.

## Candidate Comparison

- Candidate artifacts captured from the local implementation on 2026-07-13:
  - `candidate/library-1440x900.png`
  - `candidate/workspace-1440x900.png`
  - `candidate/library-390x844.png`
  - `candidate/workspace-390x844.png`
- Library: quiet, dense card grid with clear creation path and search.
- Workspace desktop: three-column Sources, Chat, Outputs hierarchy.
- Workspace compact: tabbed single-panel navigation using adopted Radix Tabs.
- Empty future regions explain unavailability without upload, ask, share, model, source-selection, or generation controls.

## Live Reference Bundle

- `live-reference/product-1440x900.png` and `live-reference/product-390x844.png` were captured from `https://notebooklm.google.com` on 2026-07-13. The unauthenticated live product redirected to Google sign-in, which is recorded in `capture-manifest.json`.
- `live-reference/help-create-use-1440x900.png` and `live-reference/help-create-use-390x844.png` were captured from Google's official NotebookLM notebook-creation help page on 2026-07-13.
- `live-reference/help-chat-1440x900.png` and `live-reference/help-chat-390x844.png` were captured from Google's official NotebookLM chat help page on 2026-07-13.
- The manifest records capture timestamp, source URL, final URL, HTTP status, page title, viewport, and a short body excerpt for each artifact.

Direct authenticated NotebookLM workspace screenshots still require a signed-in Google account and can be independently recaptured by QA if available. The stored bundle is not a candidate screenshot substitute; it preserves live product access evidence plus official external NotebookLM artifacts at both required viewports.

# Render documents through a narrow isolated adapter

Nano Notebook renders PDF pages with PDFium and converts PPTX to PDF with LibreOffice Impress before using the same PDFium page renderer. The renderer is a credential-free, job-scoped process: it receives one immutable input plus explicit page, DPI, pixel, output-byte, wall-time, memory, and scratch-space bounds; it returns only an ordered PNG manifest and files. It has no PostgreSQL, Qdrant, durable Blob Store, model, or general network credentials. The Source Worker verifies input identity, output order, dimensions, byte counts, hashes, and PNG decoding before publishing viewer artifacts.

This is a rendering adapter, not a second parser and not a MinerU-style document service. Native PDF/OOXML extraction remains Nano authority; rendered pages support Viewer fidelity and selective vision only. Renderer changes create a new render and extraction configuration. Failure never silently falls back to synthetic text-on-canvas imagery.

PDFium is selected because it is Chromium's page-rendering engine, exposes page rasterization through its test/embedding APIs, and uses a permissive BSD-style core license; its complete pinned third-party license bundle must ship with the renderer image. LibreOffice is selected because its maintained headless conversion path supports PPTX-to-PDF and its upstream repository carries MPL/LGPL/GPL licensing; Nano invokes the unmodified executable as a separate process and ships the applicable notices. The release gate rejects an image when the exact versions, checksums, license inventory, fixture render hashes, malformed/encrypted-input behavior, or resource-isolation tests are absent.

Implementation evidence:

- [PDFium upstream and rasterization capability](https://github.com/chromium/pdfium)
- [PDFium upstream license](https://pdfium.googlesource.com/pdfium/+/HEAD/LICENSE)
- [LibreOffice headless conversion example](https://wiki.documentfoundation.org/QA/Bibisect/Automation/en)
- [LibreOffice upstream source and licenses](https://github.com/LibreOffice/core)

# Document renderer third-party inventory

The renderer image intentionally contains only Nano's Go service, Nano's small PDFium CLI, and the following pinned rendering dependencies. It is not a MinerU image or a general document-processing service.

## PDFium

- Upstream project: <https://pdfium.googlesource.com/pdfium/>
- Binary packager: <https://github.com/bblanchon/pdfium-binaries>
- Pinned release: `PDFium 152.0.7961.0`, tag `chromium/7961`, published 2026-07-20
- Linux amd64 archive SHA-256: `019665c8877d46fe65f625f80fd714ab07aac68554b0636acf2a2adf9288adb2`
- Linux arm64 archive SHA-256: `974107999784a438149605024475d42d80dd306799d90e1af5f6fa63f976455f`
- Packaging project license: MIT
- PDFium and bundled component licenses: included by the archive and copied to `/usr/share/doc/pdfium` in the final image

The binary packager is independent of Google and Foxit. The Docker build uses release-specific URLs, selects only amd64 or arm64, and verifies the GitHub-published SHA-256 before extraction. Updating PDFium requires updating the release, both architecture hashes, this inventory, safety fixtures, and `NANO_DOCUMENT_RENDER_CONFIG_ID`.

## LibreOffice

- Upstream project: <https://www.libreoffice.org/about-us/licenses/>
- Distribution: Debian Bookworm `libreoffice-impress` and its dependency closure
- License: Mozilla Public License 2.0 / GNU Lesser General Public License 3.0 or later, with component-specific notices shipped by Debian under `/usr/share/doc`

LibreOffice is used only for headless PPTX-to-PDF conversion with a job-scoped user profile. It does not listen on a port and receives no shared home directory.

## zlib

- Upstream project: <https://zlib.net/>
- Distribution: Debian Bookworm `zlib1g`
- License: zlib License; Debian notices are shipped under `/usr/share/doc/zlib1g`

Nano's `pdfium_render` uses zlib to produce deterministic PNG page files without another image conversion process.

# Sprint 6 document renderer runbook

The document renderer is an internal, authenticated sidecar for PDF rasterization and PPTX-to-PDF conversion. It is deliberately separate from Source authority, extraction, chunking, and model calls. PostgreSQL and object storage remain authoritative; renderer output is validated and published by the Source worker.

## Local lifecycle

`scripts/bootstrap` builds the pinned multi-stage image. `scripts/start` starts it with the rest of local infrastructure. Verify it with:

```sh
curl -fsS http://127.0.0.1:8084/health/live
```

The Source worker uses `http://127.0.0.1:8084`, the shared local service token, and render config `pdfium-libreoffice-v1` by default.

Build the final image and exercise both frozen PDF and PPTX fixtures through the authenticated HTTP adapter with:

```sh
make test-document-renderer
```

## Isolation and budgets

- The container runs as UID 65532 with all Linux capabilities dropped, `no-new-privileges`, a read-only root filesystem, a bounded PID count, CPU limit, memory limit, and one private tmpfs scratch tree.
- It receives no database, object-store, model, or network credentials, and its authenticated HTTP port is published only on host loopback.
- Every request gets a random job directory removed after completion.
- LibreOffice gets a job-scoped `UserInstallation` profile and cannot reuse another document's state.
- Commands use fixed argument arrays and never invoke a shell.
- Input bytes and SHA-256, page count, runtime, converted PDF bytes, page pixels, total PNG bytes, archive paths, manifest order, PNG dimensions, and output SHA-256 are all checked.
- `max_pixels` is passed into Nano's native PDFium CLI and checked before bitmap allocation.
- Encrypted, password-requiring, malformed, empty, over-page, over-pixel, and over-output documents fail closed and publish no Evidence Revision or viewer artifact.

The HTTP response intentionally exposes only stable typed failures. Command stderr is bounded and is not returned to clients.

## Dependency updates

The pinned versions and hashes are in `infra/document-renderer/Dockerfile` and `infra/document-renderer/THIRD_PARTY.md`. A dependency update requires:

1. change the release-specific URL and both architecture SHA-256 values;
2. review upstream and packaging licenses and shipped notices;
3. change `NANO_DOCUMENT_RENDER_CONFIG_ID` so new output creates a new extraction configuration/Evidence Revision;
4. run renderer protocol, engine, HTTP, malformed/encrypted input, PDF, PPTX, viewer publication, and selective-vision tests;
5. build both amd64 and arm64 images in CI and scan the final image;
6. run the frozen RAG Eval before promoting the associated retrieval configuration.

Never replace the fixed download with `latest`, skip digest verification, mount a host home directory, or expose the renderer as a public upload API.

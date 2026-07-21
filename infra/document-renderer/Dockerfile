FROM golang:1.24-bookworm AS go-build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/document-renderer ./cmd/document-renderer

FROM debian:bookworm-slim AS pdfium-build

ARG TARGETARCH
ARG PDFIUM_RELEASE=chromium/7961
RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential ca-certificates curl zlib1g-dev \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /build
COPY infra/document-renderer/pdfium_render.cc ./
RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64) asset=pdfium-linux-x64.tgz; sha=019665c8877d46fe65f625f80fd714ab07aac68554b0636acf2a2adf9288adb2 ;; \
      arm64) asset=pdfium-linux-arm64.tgz; sha=974107999784a438149605024475d42d80dd306799d90e1af5f6fa63f976455f ;; \
      *) echo "unsupported TARGETARCH=${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl --fail --location --proto '=https' --tlsv1.2 --retry 3 \
      "https://github.com/bblanchon/pdfium-binaries/releases/download/${PDFIUM_RELEASE}/${asset}" \
      --output /tmp/pdfium.tgz; \
    echo "${sha}  /tmp/pdfium.tgz" | sha256sum --check -; \
    mkdir /opt/pdfium; \
    tar --extract --gzip --file /tmp/pdfium.tgz --directory /opt/pdfium; \
    g++ -std=c++17 -O2 -I/opt/pdfium/include pdfium_render.cc \
      -L/opt/pdfium/lib -Wl,-rpath,/usr/local/lib -lpdfium -lz -o /out-pdfium-render; \
    LD_LIBRARY_PATH=/opt/pdfium/lib /out-pdfium-render 2>&1 | grep -q '^usage:'

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates fonts-dejavu-core libreoffice-impress wget zlib1g \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 65532 --home-dir /nonexistent --shell /usr/sbin/nologin nano-render \
    && install -d -m 0700 -o nano-render -g nogroup /tmp/nano-render
COPY --from=go-build /out/document-renderer /usr/local/bin/document-renderer
COPY --from=pdfium-build /out-pdfium-render /usr/local/bin/pdfium_render
COPY --from=pdfium-build /opt/pdfium/lib/libpdfium.so /usr/local/lib/libpdfium.so
COPY --from=pdfium-build /opt/pdfium/LICENSE /usr/share/doc/pdfium/LICENSE
COPY --from=pdfium-build /opt/pdfium/licenses /usr/share/doc/pdfium/licenses

ENV LD_LIBRARY_PATH=/usr/local/lib \
    NANO_DOCUMENT_RENDERER_ADDR=:8084 \
    NANO_PDFIUM_BINARY=/usr/local/bin/pdfium_render \
    NANO_LIBREOFFICE_BINARY=/usr/bin/soffice \
    NANO_DOCUMENT_RENDERER_SCRATCH_ROOT=/tmp/nano-render
EXPOSE 8084
USER 65532:65534
HEALTHCHECK --interval=10s --timeout=3s --retries=20 CMD wget -qO- http://127.0.0.1:8084/health/live >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/document-renderer"]

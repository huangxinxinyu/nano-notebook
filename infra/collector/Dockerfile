FROM golang:1.24-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/nano-collector ./cmd/collector

FROM alpine:3.22

RUN apk add --no-cache ca-certificates
COPY --from=build /out/nano-collector /usr/local/bin/nano-collector
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/nano-collector"]

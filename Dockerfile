FROM --platform=$BUILDPLATFORM golang:1.26.10-alpine3.24@sha256:27f2d3b83cfc5803c8078f8e730af0f7c431bf067afaec8f60a36fb763187cba AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=devel
ARG REVISION
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X github.com/johlo/mailarky/internal/sandbox.version=${VERSION} -X github.com/johlo/mailarky/internal/sandbox.revision=${REVISION}" \
    -o /mailarky ./cmd/mailarky

FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
ARG VERSION=devel
ARG REVISION
LABEL org.opencontainers.image.title="Mailarky" \
      org.opencontainers.image.description="SMTP and IMAP test server with isolated accounts and programmable failures" \
      org.opencontainers.image.source="https://github.com/johlo/mailarky" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$REVISION
RUN apk add --no-cache ca-certificates
COPY --from=builder /mailarky /mailarky
COPY LICENSE /usr/share/licenses/mailarky/LICENSE
# Public test fixtures, not production credentials.
COPY --chmod=0444 testdata/tls/server.crt testdata/tls/server.key /certs/
RUN chmod 0755 /certs
USER 65532:65532
EXPOSE 1025 1993 8026
HEALTHCHECK --interval=2s --timeout=3s --retries=15 \
  CMD wget -qO- "http://127.0.0.1:${MAILARKY_HTTP_PORT:-8026}/healthz" || exit 1
ENTRYPOINT ["/mailarky"]

FROM --platform=$BUILDPLATFORM golang:1.26.4-alpine3.24@sha256:3ad57304ad93bbec8548a0437ad9e06a455660655d9af011d58b993f6f615648 AS builder
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

FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
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

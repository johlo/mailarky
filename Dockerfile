FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine3.24@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,id=mailarky-go-mod,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=devel
ARG REVISION
RUN --mount=type=cache,id=mailarky-go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=mailarky-go-build,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X github.com/johlo/mailarky/internal/sandbox.version=${VERSION} -X github.com/johlo/mailarky/internal/sandbox.revision=${REVISION}" \
    -o /mailarky ./cmd/mailarky

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
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
HEALTHCHECK --interval=2s --timeout=3s --retries=15 --start-period=5s --start-interval=250ms \
  CMD wget -q -O /dev/null "http://127.0.0.1:${MAILARKY_HTTP_PORT:-8026}/healthz" || exit 1
ENTRYPOINT ["/mailarky"]

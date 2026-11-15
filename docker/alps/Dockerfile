# Pin the upstream source: Alps does not publish an ARM64 release binary.
ARG ALPS_REVISION=67848b4b1cac29a05d37e51a9914ef1d53e1b358
FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine3.24@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS builder
RUN apk add --no-cache patch
ARG ALPS_REVISION
ADD https://github.com/migadu/alps.git#${ALPS_REVISION} /src
WORKDIR /src
COPY docker/alps/smtp-saves-sent.patch /tmp/smtp-saves-sent.patch
RUN patch -p1 < /tmp/smtp-saves-sent.patch
COPY docker/alps/account-picker.patch /tmp/account-picker.patch
RUN patch -p1 < /tmp/account-picker.patch
COPY internal/webmail /src/internal/mailarkywebmail
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,id=mailarky-go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=mailarky-go-build,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false \
    -ldflags="-s -w" -o /alps ./cmd/alps

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
ARG ALPS_REVISION
LABEL org.opencontainers.image.title="Alps for Mailarky" \
      org.opencontainers.image.source="https://github.com/johlo/mailarky" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.revision=$ALPS_REVISION
RUN apk add --no-cache ca-certificates
COPY --from=builder /alps /alps
COPY --from=builder /src/LICENSE /usr/share/licenses/alps/LICENSE
USER 65532:65532
EXPOSE 1323
ENTRYPOINT ["/alps"]
CMD ["-config", "/etc/alps/config.toml"]

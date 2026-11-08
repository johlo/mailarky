# Pin the upstream source: Alps does not publish an ARM64 release binary.
ARG ALPS_REVISION=18c18777348e4333a3e3e1d164528e35e706071b
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine3.24@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS builder
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

# syntax=docker/dockerfile:1
# healthkit-relay server: a static binary on distroless, running as a non-root user.
#   docker build -t healthkit-relay .
#   docker buildx build --platform linux/amd64,linux/arm64 -t healthkit-relay .
# Cross-compiles on the build machine (no emulation needed for arm64).

ARG GO_VERSION=1.27

FROM --platform=$BUILDPLATFORM docker.io/library/golang:${GO_VERSION}-alpine AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
ENV CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOFLAGS=-trimpath
WORKDIR /src/server
COPY server/go.* ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY server/ ./
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/healthkit-relay .
COPY docker/healthcheck/ /src/healthcheck/
RUN --mount=type=cache,target=/root/.cache/go-build \
    cd /src/healthcheck && go build -ldflags "-s -w" -o /out/healthcheck .
# The volume's mount point, owned by the runtime user so a fresh named volume is writable.
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian13:nonroot
ARG VERSION=dev
LABEL org.opencontainers.image.title="healthkit-relay" \
      org.opencontainers.image.description="Receive Apple Health samples from the Health Relay iPhone app and archive them" \
      org.opencontainers.image.source="https://github.com/leblancfg/healthkit-relay" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"
COPY --from=build /out/healthkit-relay /out/healthcheck /usr/local/bin/
COPY --from=build --chown=65532:65532 /out/data /data
# Every flag can be set as HKR_<FLAG>; see server/README.md. On first start the server prints
# the admin token once (docker logs) and keeps its hash in /data/admin.json.
ENV HKR_LISTEN=:8080 \
    HKR_ARCHIVE_DIR=/data
VOLUME /data
EXPOSE 8080
USER 65532:65532
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/usr/local/bin/healthcheck"]
ENTRYPOINT ["/usr/local/bin/healthkit-relay"]

# syntax=docker/dockerfile:1.7
#
# Multi-stage Dockerfile for HUE. Final image is distroless/static:nonroot
# (~25 MB), runs as UID 65532, no shell, no package manager.

ARG GO_VERSION=1.26
FROM golang:${GO_VERSION}-alpine AS deps
WORKDIR /src
RUN apk add --no-cache git ca-certificates tzdata
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

FROM deps AS build
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -buildvcs=false \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${BUILD_DATE}" \
      -o /out/hue ./cmd/hue

FROM gcr.io/distroless/static-debian12:nonroot AS runtime
COPY --from=build /out/hue /usr/local/bin/hue
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
EXPOSE 8443
USER nonroot:nonroot

# Self-healthcheck via the binary's "healthcheck" subcommand — no curl needed.
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/hue", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/hue"]

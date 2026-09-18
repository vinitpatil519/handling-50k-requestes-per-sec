# syntax=docker/dockerfile:1.7
#
# One Dockerfile for every Go binary in the repo:
#   docker build -f build/go.Dockerfile --build-arg CMD=api       -t titanedge/api .
#   docker build -f build/go.Dockerfile --build-arg CMD=worker    -t titanedge/worker .
#   docker build -f build/go.Dockerfile --build-arg CMD=titanload -t titanedge/titanload .

ARG GO_VERSION=1.27

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
RUN apk add --no-cache git ca-certificates tzdata

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY cmd ./cmd
COPY internal ./internal

ARG CMD=api
ARG VERSION=dev
ARG COMMIT=none
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags "-s -w \
        -X github.com/titanedge/titanedge/internal/buildinfo.Version=${VERSION} \
        -X github.com/titanedge/titanedge/internal/buildinfo.Commit=${COMMIT} \
        -X github.com/titanedge/titanedge/internal/buildinfo.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      -o /out/app ./cmd/${CMD}

# Distroless static: no shell, no package manager, runs as uid 65532.
FROM gcr.io/distroless/static-debian13:nonroot
ARG CMD=api
LABEL org.opencontainers.image.source="https://github.com/titanedge/titanedge" \
      org.opencontainers.image.title="titanedge-${CMD}"
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /out/app /app
USER 65532:65532
EXPOSE 8080 9090 9102
ENTRYPOINT ["/app"]

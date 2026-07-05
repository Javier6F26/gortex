# syntax=docker/dockerfile:1
# Multi-stage build for gortex — code intelligence engine.
#
# Build with Docker Buildx for multi-arch:
#
#   docker buildx build \
#     --platform linux/amd64,linux/arm64 \
#     -t ghcr.io/javier6f26/gortex:latest \
#     -t ghcr.io/javier6f26/gortex:<version> \
#     --push .
#
# Or just for local testing on Mac:
#
#   docker build -t gortex:dev .

# ---- Build stage ----
FROM golang:1.26-bookworm AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    libtree-sitter-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build

# Cache go modules. Copy the thirdparty dir too because go.mod has
# replace directives pointing at ./internal/thirdparty/*. Without
# these directories go mod download fails with "no such file".
COPY go.mod go.sum internal/thirdparty/ ./internal/thirdparty/
RUN go mod download

COPY . .

ARG TARGETOS TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown

RUN CGO_ENABLED=1 \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w \
      -X main.version=${VERSION} \
      -X main.commit=${COMMIT} \
      -X main.date=${DATE}" \
    -o /gortex ./cmd/gortex/

# ---- Runtime stage ----
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    libtree-sitter0 \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /gortex /usr/local/bin/gortex

ENTRYPOINT ["gortex"]
CMD ["--help"]

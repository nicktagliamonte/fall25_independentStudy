# syntax=docker/dockerfile:1
# Purpose: Docker image for running distributed node instances (BuildKit: cache mounts, GOPROXY).

FROM golang:1.26-alpine AS builder

ARG GOPROXY=https://proxy.golang.org,direct
ARG GOSUMDB=sum.golang.org
ENV GOPROXY=${GOPROXY} GOSUMDB=${GOSUMDB}

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    sh -c 'set -e; for _ in 1 2 3 4 5; do go mod download && exit 0; sleep 20; done; exit 1'

# Copy source code
COPY . .

# Build the binary
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -o bin/node ./cmd/node

# Runtime stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates curl jq

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/bin/node /app/node

# Create directories for node data
RUN mkdir -p /app/data /app/keys /app/logs

# Expose default ports (will be overridden in docker-compose)
EXPOSE 2893 2894

ENTRYPOINT ["/app/node"]

# syntax=docker/dockerfile:1.6
# Purpose: Docker image for running distributed node instances (BuildKit: cache mounts, GOPROXY).
#
# Order matters: the runtime stage must COPY from builder before any RUN apk. Otherwise BuildKit
# runs the apk layer in parallel with the Go build; apk then uses the build network resolver and
# often fails with "DNS: transient error" to dl-cdn while the builder stage is still compiling.

FROM golang:1.26-alpine AS builder

ARG GOPROXY=https://proxy.golang.org,direct
ARG GOSUMDB=sum.golang.org
ENV GOPROXY=${GOPROXY} GOSUMDB=${GOSUMDB}

WORKDIR /build

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    sh -c 'set -e; for _ in 1 2 3 4 5; do go mod download && exit 0; sleep 20; done; exit 1'

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -o bin/node ./cmd/node

FROM alpine:3.21

WORKDIR /app

COPY --from=builder /build/bin/node /app/node

RUN --network=host apk add --no-cache ca-certificates curl jq

RUN mkdir -p /app/data /app/keys /app/logs

EXPOSE 2893 2894

ENTRYPOINT ["/app/node"]

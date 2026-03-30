# syntax=docker/dockerfile:1
# Purpose: Docker image for Ethereum Swarm node (v0.5.8). Builder stays on golang:1.14-alpine
# because Swarm v0.5.8 does not build under Go 1.16+ module semantics. Runtime uses a current
# Alpine (3.21) with retried apk to mitigate transient dl-cdn DNS failures.

FROM golang:1.14-alpine AS builder

WORKDIR /build

RUN set -e; \
  for _ in 1 2 3 4 5 6; do \
    apk add --no-cache git make gcc musl-dev linux-headers && break; \
    sleep 15; \
  done

RUN set -e; \
  for _ in 1 2 3 4 5; do \
    git clone --depth 1 --branch v0.5.8 https://github.com/ethersphere/swarm.git && break; \
    sleep 15; \
  done

WORKDIR /build/swarm

RUN make swarm

FROM alpine:3.21

RUN set -e; \
  for _ in 1 2 3 4 5 6; do \
    apk add --no-cache ca-certificates curl jq && break; \
    sleep 15; \
  done

WORKDIR /app

COPY --from=builder /build/swarm/build/bin/swarm /app/swarm
RUN chmod +x /app/swarm

RUN mkdir -p /app/data /app/logs

EXPOSE 8500 30399

COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh

ENTRYPOINT ["/app/entrypoint.sh"]

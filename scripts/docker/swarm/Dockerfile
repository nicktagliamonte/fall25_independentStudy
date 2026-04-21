# syntax=docker/dockerfile:1
# Purpose: Docker image for Ethereum Swarm node (v0.5.8). Builder stays on golang:1.14-alpine
# because Swarm v0.5.8 does not build under Go 1.16+ module semantics. Runtime uses a current
# Alpine (3.21) with retried apk to mitigate transient dl-cdn DNS failures.

FROM golang:1.14-alpine AS builder

WORKDIR /build

RUN sed -i 's/dl-cdn.alpinelinux.org/dl-5.alpinelinux.org/g' /etc/apk/repositories

RUN set -e; \
  for i in 1 2 3 4 5 6; do \
    if apk add --no-cache git make gcc musl-dev linux-headers; then break; fi; \
    if [ $i -eq 6 ]; then exit 1; fi; \
    sleep 15; \
  done

RUN set -e; \
  for i in 1 2 3 4 5 6; do \
    if git clone --depth 1 --branch v0.5.8 https://github.com/ethersphere/swarm.git; then break; fi; \
    if [ $i -eq 6 ]; then exit 1; fi; \
    sleep 15; \
  done

WORKDIR /build/swarm

RUN make swarm

FROM alpine:3.21

# Workaround for transient dl-cdn DNS issues
RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.edge.kernel.org/g' /etc/apk/repositories

RUN set -e; \
  for i in 1 2 3 4 5 6; do \
    if apk add --no-cache ca-certificates curl jq; then break; fi; \
    if [ $i -eq 6 ]; then exit 1; fi; \
    sleep 15; \
  done

WORKDIR /app

COPY --from=builder /build/swarm/build/bin/swarm /app/swarm
COPY entrypoint.sh /app/entrypoint.sh

RUN chmod +x /app/swarm /app/entrypoint.sh && \
    mkdir -p /app/data /app/logs

EXPOSE 8500 30399

ENTRYPOINT ["/app/entrypoint.sh"]

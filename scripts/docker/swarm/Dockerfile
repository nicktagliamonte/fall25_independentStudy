# Purpose: Docker image for Ethereum Swarm node (v0.5.8)

FROM golang:1.14-alpine AS builder

WORKDIR /build

# Install build dependencies
RUN apk add --no-cache git make gcc musl-dev linux-headers

# Clone Swarm v0.5.8 repository
RUN git clone --depth 1 --branch v0.5.8 https://github.com/ethersphere/swarm.git

WORKDIR /build/swarm

# Build Swarm binary
RUN make swarm

# Runtime stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates curl jq

WORKDIR /app

# Copy Swarm binary from builder
COPY --from=builder /build/swarm/build/bin/swarm /app/swarm
RUN chmod +x /app/swarm

# Create directories for Swarm data
RUN mkdir -p /app/data /app/logs

# Expose Swarm API port (default 8500) and P2P port (default 30399)
EXPOSE 8500 30399

# Copy entrypoint script
COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh

ENTRYPOINT ["/app/entrypoint.sh"]

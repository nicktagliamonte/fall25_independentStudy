# Purpose: Docker image for running distributed node instances

FROM golang:1.26-alpine AS builder

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN go build -o bin/node ./cmd/node

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

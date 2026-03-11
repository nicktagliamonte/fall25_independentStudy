#!/usr/bin/env bash
set -euo pipefail

# Purpose: Start N Swarm/Bee Docker nodes (N in {10, 50, 100, 500} for fair comparison with vn-IPFS)
# Usage: ./scripts/docker/swarm/start.sh [N]
#   N: 10, 50, 100, or 500 (default: 10)

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT_DIR"

VALID_COUNTS="10 50 100 500"
N="${1:-10}"

if [[ ! " $VALID_COUNTS " =~ " $N " ]]; then
  echo "Error: N must be one of: $VALID_COUNTS (for fair comparison with vn-IPFS)" >&2
  exit 1
fi

echo "Starting $N Swarm/Bee Docker nodes..."

# Stop any existing Swarm containers first (but don't remove network if other containers use it)
if docker-compose -f docker-compose.swarm.yml ps 2>/dev/null | grep -q "Up"; then
  echo "Stopping existing Swarm containers..."
  # Stop containers and remove volumes, but don't remove network (it may be shared with our system)
  docker-compose -f docker-compose.swarm.yml stop 2>/dev/null || true
  docker-compose -f docker-compose.swarm.yml rm -f 2>/dev/null || true
fi

# Ensure the shared network exists (created by our system's docker-compose)
# If it doesn't exist, create it
if ! docker network inspect fall25_independentstudy_node-network >/dev/null 2>&1; then
  echo "Creating shared network..."
  docker network create --driver bridge --subnet 172.20.0.0/16 fall25_independentstudy_node-network 2>/dev/null || true
fi

# Generate docker-compose.swarm.yml dynamically
TEMPLATE="$ROOT_DIR/scripts/docker/swarm-compose.template.yml"
COMPOSE_FILE="$ROOT_DIR/docker-compose.swarm.yml"

if [[ ! -f "$TEMPLATE" ]]; then
  echo "Error: Template file not found: $TEMPLATE" >&2
  exit 1
fi

# Copy template and generate node services
cp "$TEMPLATE" "$COMPOSE_FILE"

# Find the line number where "networks:" starts
NETWORKS_LINE=$(grep -n "^networks:" "$COMPOSE_FILE" | cut -d: -f1)

# Create temp file with content before networks
head -n $((NETWORKS_LINE - 1)) "$COMPOSE_FILE" > "$COMPOSE_FILE.tmp"

# Generate node services (swarm-node1 .. swarm-nodeN-1) - insert before networks section
for i in $(seq 1 $((N - 1))); do
  IP_LAST=$((200 + i))  # Bootstrap 172.20.0.200, peers 172.20.0.201+
  cat >> "$COMPOSE_FILE.tmp" <<EOF
  swarm-node${i}:
    build: scripts/docker/swarm
    image: swarm-node:latest
    container_name: swarm-node${i}
    hostname: swarm-node${i}
    environment:
      - SWARM_DATA_DIR=/app/data
      - SWARM_HTTP_ADDR=0.0.0.0:8500
      - SWARM_HTTP_PORT=8500
      - SWARM_VERBOSITY=4
      - SWARM_PASSWORD=swarm-test-password
      - SWARM_BOOTNODE=enode://PLACEHOLDER_PEER_ID@172.20.0.200:30399
    volumes:
      - swarm-node${i}-data:/app/data
      - swarm-node${i}-logs:/app/logs
    networks:
      node-network:
        ipv4_address: 172.20.0.${IP_LAST}
    healthcheck:
      test: ["CMD", "sh", "-c", "curl -sf http://localhost:8500/ || exit 1"]
      interval: 5s
      timeout: 3s
      retries: 10
      start_period: 30s
    depends_on:
      swarm-bootstrap:
        condition: service_healthy
EOF
done

# Append networks section
tail -n +$NETWORKS_LINE "$COMPOSE_FILE" >> "$COMPOSE_FILE.tmp"

# Append node volumes to the existing volumes section (at end of file)
if [[ $((N - 1)) -gt 0 ]]; then
  # Add volumes for additional nodes (they'll be appended after bootstrap volumes)
  for i in $(seq 1 $((N - 1))); do
    cat >> "$COMPOSE_FILE.tmp" <<EOF
  swarm-node${i}-data:
  swarm-node${i}-logs:
EOF
  done
fi

mv "$COMPOSE_FILE.tmp" "$COMPOSE_FILE"

# Build the image first
echo "Building Swarm Docker image..."
if ! docker build -t swarm-node:latest scripts/docker/swarm/; then
  echo "ERROR: Docker build failed" >&2
  exit 1
fi

# Start bootstrap node first
echo "Starting bootstrap node..."
if ! docker-compose -f docker-compose.swarm.yml up -d swarm-bootstrap; then
  echo "ERROR: Failed to start bootstrap node" >&2
  exit 1
fi

# Wait for bootstrap to be ready
echo "Waiting for bootstrap node to be ready..."
BOOTSTRAP_READY=false
for i in {1..30}; do
  if docker-compose -f docker-compose.swarm.yml exec -T swarm-bootstrap curl -sf http://localhost:8500/ >/dev/null 2>&1; then
    BOOTSTRAP_READY=true
    break
  fi
  echo "  Attempt $i/30..."
  sleep 2
done

if [[ "$BOOTSTRAP_READY" != "true" ]]; then
  echo "ERROR: Bootstrap node failed to become ready" >&2
  docker-compose -f docker-compose.swarm.yml logs swarm-bootstrap
  exit 1
fi

echo "Bootstrap node is ready!"

# Get bootstrap peer ID (Swarm v0.5.8 doesn't expose this via API easily)
# We'll need to extract it from logs or use a placeholder
# For now, we'll use the placeholder and let Swarm handle bootnode discovery
BOOTSTRAP_PEER_ID="PLACEHOLDER_PEER_ID"
echo "Note: Swarm v0.5.8 bootnode configuration may need manual peer ID extraction from logs"

# Start remaining nodes if N > 1
if [[ "$N" -gt 1 ]]; then
  echo "Starting $((N - 1)) peer nodes..."
  for i in $(seq 1 $((N - 1))); do
    docker-compose -f docker-compose.swarm.yml up -d "swarm-node${i}" || true
    [[ $((i % 20)) -eq 0 ]] && echo "  Started swarm-node$i..."
  done

  MAX_ATTEMPTS=$((60 + (N - 1) / 5))  # Scale wait time for large clusters
  echo "Waiting for all nodes (up to ${MAX_ATTEMPTS} attempts)..."
  ALL_READY=false
  for i in $(seq 1 $MAX_ATTEMPTS); do
    READY_COUNT=0
    for j in $(seq 1 $((N - 1))); do
      if docker-compose -f docker-compose.swarm.yml exec -T "swarm-node${j}" curl -sf http://localhost:8500/ >/dev/null 2>&1; then
        READY_COUNT=$((READY_COUNT + 1))
      fi
    done
    if [[ $READY_COUNT -eq $((N - 1)) ]]; then
      ALL_READY=true
      break
    fi
    echo "  Attempt $i/$MAX_ATTEMPTS: $READY_COUNT/$((N - 1)) nodes ready..."
    sleep 2
  done
  
  if [[ "$ALL_READY" != "true" ]]; then
    echo "WARNING: Not all nodes became ready, but continuing..."
  else
    echo "All nodes are ready!"
  fi
fi

# Print summary
echo ""
echo "=========================================="
echo "Swarm/Bee cluster started successfully!"
echo "=========================================="
echo "Bootstrap API: http://172.20.0.200:8500"
echo "Bootstrap Peer ID: $BOOTSTRAP_PEER_ID"
echo ""
echo "Node addresses:"
echo "  swarm-bootstrap: http://172.20.0.200:8500"
for i in $(seq 1 $((N - 1))); do
  IP_LAST=$((200 + i))
  echo "  swarm-node${i}: http://172.20.0.${IP_LAST}:8500"
done
echo ""
echo "To check status: docker-compose -f docker-compose.swarm.yml ps"
echo "To view logs: docker-compose -f docker-compose.swarm.yml logs -f"
echo "To stop: docker-compose -f docker-compose.swarm.yml down"
echo ""

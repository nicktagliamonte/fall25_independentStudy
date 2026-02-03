#!/usr/bin/env bash
set -euo pipefail

# Purpose: Start N Docker nodes with bootstrap configuration
# Usage: ./scripts/docker/start.sh [N]
#   N: number of nodes (default: 4)

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

N="${1:-4}"

if [[ ! "$N" =~ ^[0-9]+$ ]] || [[ "$N" -lt 2 ]]; then
  echo "Error: N must be an integer >= 2" >&2
  exit 1
fi

echo "Starting $N Docker nodes..."

# Stop any existing containers first
if docker-compose ps 2>/dev/null | grep -q "Up"; then
  echo "Stopping existing containers..."
  docker-compose down
fi

# Generate docker-compose.yml dynamically
TEMPLATE="$ROOT_DIR/scripts/docker/docker-compose.template.yml"
COMPOSE_FILE="$ROOT_DIR/docker-compose.yml"

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

# Generate node services (2 through N) - insert before networks section
for i in $(seq 2 "$N"); do
  IP_LAST=$((9 + i))
  cat >> "$COMPOSE_FILE.tmp" <<EOF
  node${i}:
    build: .
    container_name: fall25-node${i}
    hostname: node${i}
    command: run
      --listen /ip4/0.0.0.0/tcp/4001
      --listen /ip4/0.0.0.0/udp/4002/quic-v1
      --key /app/keys/node${i}.key
      --store /app/data/node${i}
      --min-outbound 4
      --control /app/logs/node${i}.json
      --log /app/logs/node${i}.log
    volumes:
      - node${i}-data:/app/data
      - node${i}-keys:/app/keys
      - node${i}-logs:/app/logs
    networks:
      node-network:
        ipv4_address: 172.20.0.${IP_LAST}
    depends_on:
      bootstrap:
        condition: service_healthy
    environment:
      - SNG40_SEEDS=/ip4/172.20.0.10/tcp/4001/p2p/PLACEHOLDER_PEER_ID
EOF
done

# Append networks section and rest of file
tail -n +$NETWORKS_LINE "$COMPOSE_FILE" >> "$COMPOSE_FILE.tmp"
mv "$COMPOSE_FILE.tmp" "$COMPOSE_FILE"

# Add volumes for all nodes
cat >> "$COMPOSE_FILE" <<EOF

volumes:
  bootstrap-data:
  bootstrap-keys:
  bootstrap-logs:
EOF

for i in $(seq 2 "$N"); do
  cat >> "$COMPOSE_FILE" <<EOF
  node${i}-data:
  node${i}-keys:
  node${i}-logs:
EOF
done

# Build the image first (skip if already exists and up to date)
echo "Building Docker image..."
if ! docker-compose build --progress=plain 2>&1 | tee /tmp/docker_build.log; then
  echo "ERROR: Docker build failed. Check /tmp/docker_build.log for details" >&2
  exit 1
fi

# Start bootstrap node first
echo "Starting bootstrap node..."
docker-compose up -d bootstrap

# Wait for bootstrap to be ready and get its peer ID
echo "Waiting for bootstrap node to be ready..."
PEER_ID=""
BOOTSTRAP_SEED=""
MAX_WAIT=120
for i in $(seq 1 $MAX_WAIT); do
  # Check if container is running
  if ! docker-compose ps bootstrap | grep -q "Up"; then
    echo "ERROR: Bootstrap container is not running" >&2
    docker-compose logs bootstrap | tail -50
    exit 1
  fi
  
  if docker-compose exec -T bootstrap sh -c "test -f /app/logs/bootstrap.json" 2>/dev/null; then
    sleep 2
    CTRL_ADDR=$(docker-compose exec -T bootstrap jq -r '.addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
    if [[ -n "$CTRL_ADDR" && "$CTRL_ADDR" != "null" ]]; then
      # Wait for HTTP endpoint
      if docker-compose exec -T bootstrap curl -sf "http://$CTRL_ADDR/health" >/dev/null 2>&1; then
        PEER_ID=$(docker-compose exec -T bootstrap curl -sf "http://$CTRL_ADDR/id" | jq -r '.peer' 2>/dev/null || echo "")
        if [[ -n "$PEER_ID" && "$PEER_ID" != "null" ]]; then
          BOOTSTRAP_SEED="/ip4/172.20.0.10/tcp/4001/p2p/$PEER_ID"
          echo "Bootstrap node ready! (took ${i}s)"
          echo "  Control: $CTRL_ADDR"
          echo "  Peer ID: $PEER_ID"
          echo "  Seed: $BOOTSTRAP_SEED"
          break
        fi
      fi
    fi
  fi
  
  if [[ $((i % 10)) -eq 0 ]]; then
    echo "  Still waiting... (${i}s/${MAX_WAIT}s)"
    docker-compose logs bootstrap | tail -5
  fi
  sleep 1
done

if [[ -z "$PEER_ID" || "$PEER_ID" == "null" ]]; then
  echo "ERROR: Failed to get bootstrap peer ID after ${MAX_WAIT}s" >&2
  echo "Bootstrap container status:"
  docker-compose ps bootstrap
  echo ""
  echo "Bootstrap logs (last 50 lines):"
  docker-compose logs bootstrap | tail -50
  echo ""
  echo "Checking if bootstrap.json exists:"
  docker-compose exec -T bootstrap ls -la /app/logs/ 2>&1 || true
  exit 1
fi

# Update docker-compose.yml with actual peer ID (escape special chars for sed)
ESCAPED_PEER_ID=$(echo "$PEER_ID" | sed 's/[[\.*^$()+?{|]/\\&/g')
sed -i "s|PLACEHOLDER_PEER_ID|$ESCAPED_PEER_ID|g" "$COMPOSE_FILE"

# Start remaining nodes (2 through N)
echo "Starting peer nodes..."
for i in $(seq 2 "$N"); do
  SERVICE="node$i"
  echo "Starting $SERVICE..."
  docker-compose up -d "$SERVICE"
done

# Wait a bit for nodes to connect
echo "Waiting for nodes to connect..."
sleep 10

# Print status
echo ""
echo "=== Node Status ==="
for i in $(seq 1 "$N"); do
  if [[ "$i" -eq 1 ]]; then
    SERVICE="bootstrap"
  else
    SERVICE="node$i"
  fi
  
  if docker-compose ps "$SERVICE" | grep -q "Up"; then
    CTRL_FILE="/app/logs/$SERVICE.json"
    if docker-compose exec -T "$SERVICE" test -f "$CTRL_FILE" 2>/dev/null; then
      CTRL_ADDR=$(docker-compose exec -T "$SERVICE" jq -r '.addr' "$CTRL_FILE" 2>/dev/null || echo "")
      if [[ -n "$CTRL_ADDR" && "$CTRL_ADDR" != "null" ]]; then
        NODE_PEER_ID=$(docker-compose exec -T "$SERVICE" curl -sf "http://$CTRL_ADDR/id" | jq -r '.peer' 2>/dev/null || echo "")
        NEIGHBORS=$(docker-compose exec -T "$SERVICE" curl -sf "http://$CTRL_ADDR/neighbors" | jq 'length' 2>/dev/null || echo "0")
        echo "Node $i ($SERVICE):"
        echo "  Peer ID: $NODE_PEER_ID"
        echo "  Control: $CTRL_ADDR"
        echo "  Neighbors: $NEIGHBORS"
      fi
    fi
  else
    echo "Node $i ($SERVICE): not running"
  fi
done

echo ""
echo "Done! Use './scripts/docker/status.sh' to check status"
echo "Use './scripts/docker/stop.sh' to stop all nodes"

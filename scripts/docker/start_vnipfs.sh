#!/usr/bin/env bash
set -euo pipefail

# Purpose: Start N vn-IPFS Docker nodes (N in {10, 50, 100, 500})
# Usage: ./scripts/docker/start_vnipfs.sh [N]
#   N: 10, 50, 100, or 500 (default: 10)

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

VALID_COUNTS="10 50 100 500"
N="${1:-10}"

if [[ ! " $VALID_COUNTS " =~ " $N " ]]; then
  echo "Error: N must be one of: $VALID_COUNTS" >&2
  exit 1
fi

echo "Starting $N vn-IPFS Docker nodes..."

COMPOSE_FILE="$ROOT_DIR/docker-compose.vnipfs.yml"
TEMPLATE="$ROOT_DIR/scripts/docker/vnipfs-compose.template.yml"

if [[ ! -f "$TEMPLATE" ]]; then
  echo "Error: Template not found: $TEMPLATE" >&2
  exit 1
fi

# Stop existing vnipfs containers
if docker-compose -f "$COMPOSE_FILE" ps 2>/dev/null | grep -q "Up"; then
  echo "Stopping existing vn-IPFS containers..."
  docker-compose -f "$COMPOSE_FILE" stop >/dev/null 2>&1 || true
  docker-compose -f "$COMPOSE_FILE" rm -f >/dev/null 2>&1 || true
fi

# Generate docker-compose.vnipfs.yml
cp "$TEMPLATE" "$COMPOSE_FILE"
NETWORKS_LINE=$(grep -n "^networks:" "$COMPOSE_FILE" | cut -d: -f1)
head -n $((NETWORKS_LINE - 1)) "$COMPOSE_FILE" > "$COMPOSE_FILE.tmp"

for i in $(seq 2 "$N"); do
  IP_LAST=$((9 + i))
  cat >> "$COMPOSE_FILE.tmp" <<EOF
  node${i}:
    build: .
    container_name: fall25-node${i}
    hostname: node${i}
    command: run
      --listen /ip4/172.20.0.${IP_LAST}/tcp/4001
      --listen /ip4/172.20.0.${IP_LAST}/udp/4002/quic-v1
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

tail -n +$NETWORKS_LINE "$COMPOSE_FILE" >> "$COMPOSE_FILE.tmp"
mv "$COMPOSE_FILE.tmp" "$COMPOSE_FILE"

echo "volumes:" >> "$COMPOSE_FILE"
echo "  bootstrap-data:" >> "$COMPOSE_FILE"
echo "  bootstrap-keys:" >> "$COMPOSE_FILE"
echo "  bootstrap-logs:" >> "$COMPOSE_FILE"
for i in $(seq 2 "$N"); do
  echo "  node${i}-data:" >> "$COMPOSE_FILE"
  echo "  node${i}-keys:" >> "$COMPOSE_FILE"
  echo "  node${i}-logs:" >> "$COMPOSE_FILE"
done

echo "Building Docker image..."
if ! docker-compose -f "$COMPOSE_FILE" build --progress=plain 2>&1 | tee /tmp/docker_build_vnipfs.log; then
  echo "ERROR: Docker build failed. Check /tmp/docker_build_vnipfs.log" >&2
  exit 1
fi

echo "Starting bootstrap node..."
docker-compose -f "$COMPOSE_FILE" up -d bootstrap

echo "Waiting for bootstrap (health check)..."
PEER_ID=""
MAX_WAIT=120
for i in $(seq 1 $MAX_WAIT); do
  if ! docker-compose -f "$COMPOSE_FILE" ps bootstrap | grep -q "Up"; then
    echo "ERROR: Bootstrap container not running" >&2
    docker-compose -f "$COMPOSE_FILE" logs bootstrap | tail -50
    exit 1
  fi

  if docker-compose -f "$COMPOSE_FILE" exec -T bootstrap sh -c "test -f /app/logs/bootstrap.json" 2>/dev/null; then
    sleep 2
    CTRL_ADDR=$(docker-compose -f "$COMPOSE_FILE" exec -T bootstrap jq -r '.addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
    if [[ -n "$CTRL_ADDR" && "$CTRL_ADDR" != "null" ]]; then
      if docker-compose -f "$COMPOSE_FILE" exec -T bootstrap curl -sf "http://$CTRL_ADDR/health" >/dev/null 2>&1; then
        PEER_ID=$(docker-compose -f "$COMPOSE_FILE" exec -T bootstrap curl -sf "http://$CTRL_ADDR/id" | jq -r '.peer' 2>/dev/null || echo "")
        if [[ -n "$PEER_ID" && "$PEER_ID" != "null" ]]; then
          echo "Bootstrap ready (${i}s)"
          break
        fi
      fi
    fi
  fi

  [[ $((i % 10)) -eq 0 ]] && echo "  Waiting... (${i}s/${MAX_WAIT}s)"
  sleep 1
done

if [[ -z "$PEER_ID" || "$PEER_ID" == "null" ]]; then
  echo "ERROR: Bootstrap not ready after ${MAX_WAIT}s" >&2
  docker-compose -f "$COMPOSE_FILE" logs bootstrap | tail -50
  exit 1
fi

ESCAPED_PEER_ID=$(echo "$PEER_ID" | sed 's/[[\.*^$()+?{|]/\\&/g')
if [[ "$OSTYPE" == "darwin"* ]]; then
  sed -i '' "s|PLACEHOLDER_PEER_ID|$ESCAPED_PEER_ID|g" "$COMPOSE_FILE"
else
  sed -i "s|PLACEHOLDER_PEER_ID|$ESCAPED_PEER_ID|g" "$COMPOSE_FILE"
fi

echo "Starting peer nodes 2..$N..."
for i in $(seq 2 "$N"); do
  docker-compose -f "$COMPOSE_FILE" up -d "node$i" || true
  [[ $((i % 20)) -eq 0 ]] && echo "  Started node$i..."
done

echo "Waiting for nodes to connect..."
sleep 15

echo ""
echo "vn-IPFS: $N nodes running (docker-compose.vnipfs.yml)"
echo "  Stop: docker-compose -f docker-compose.vnipfs.yml down"
echo "  Logs: docker-compose -f docker-compose.vnipfs.yml logs -f"

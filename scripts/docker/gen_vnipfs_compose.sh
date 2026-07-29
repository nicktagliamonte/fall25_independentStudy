#!/usr/bin/env bash
set -euo pipefail

# Purpose: Emit docker-compose.vnipfs.yml from vnipfs-compose.template.yml without Docker build/up.
# Usage: ./scripts/docker/gen_vnipfs_compose.sh [N]
#   N: 10, 50, 100, or 500 (default: 10)

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VALID_COUNTS="10 50 100 500"
N="${1:-10}"

if [[ ! " $VALID_COUNTS " =~ " $N " ]]; then
  echo "Error: N must be one of: $VALID_COUNTS" >&2
  exit 1
fi

COMPOSE_FILE="${COMPOSE_FILE:-$ROOT_DIR/docker-compose.vnipfs.yml}"
TEMPLATE="$ROOT_DIR/scripts/docker/vnipfs-compose.template.yml"

if [[ ! -f "$TEMPLATE" ]]; then
  echo "Error: Template not found: $TEMPLATE" >&2
  exit 1
fi

cp "$TEMPLATE" "$COMPOSE_FILE"
NETWORKS_LINE=$(grep -n "^networks:" "$COMPOSE_FILE" | cut -d: -f1)
head -n $((NETWORKS_LINE - 1)) "$COMPOSE_FILE" > "$COMPOSE_FILE.tmp"

for i in $(seq 2 "$N"); do
  IP_LAST=$((9 + i))
  cat >> "$COMPOSE_FILE.tmp" <<EOF
  node${i}:
    image: fall25_independentstudy-bootstrap:latest
    container_name: fall25-node${i}
    hostname: node${i}
    command: run
      --listen /ip4/172.20.0.${IP_LAST}/tcp/4001
      --key /app/keys/node${i}.key
      --store /app/data/node${i}
      --min-outbound \${TARSUS_MIN_OUTBOUND:-4}
      --cluster-nodes $N
      --no-default-bootstrap
      --index-shards \${TARSUS_INDEX_SHARDS:-16}
      --disable-bloom-pruning=\${TARSUS_DISABLE_BLOOM_PRUNING:-false}
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

echo "Wrote $COMPOSE_FILE (nodes 2..$N)"

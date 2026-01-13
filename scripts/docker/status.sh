#!/usr/bin/env bash
set -euo pipefail

# Purpose: Check status of Docker nodes
# Usage: ./scripts/docker/status.sh

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

echo "=== Docker Node Status ==="
echo ""

# Get all running node containers
NODES=$(docker-compose ps --services | grep -E '^(bootstrap|node)' || true)

if [[ -z "$NODES" ]]; then
  echo "No nodes are running"
  exit 0
fi

for SERVICE in $NODES; do
  if docker-compose ps "$SERVICE" | grep -q "Up"; then
    CTRL_FILE="/app/logs/${SERVICE}.json"
    if docker-compose exec -T "$SERVICE" test -f "$CTRL_FILE" 2>/dev/null; then
      CTRL_ADDR=$(docker-compose exec -T "$SERVICE" jq -r '.addr' "$CTRL_FILE" 2>/dev/null || echo "")
      if [[ -n "$CTRL_ADDR" && "$CTRL_ADDR" != "null" ]]; then
        ID_JSON=$(docker-compose exec -T "$SERVICE" curl -sf "http://$CTRL_ADDR/id" 2>/dev/null || echo "{}")
        PEER_ID=$(echo "$ID_JSON" | jq -r '.peer' 2>/dev/null || echo "")
        NEIGHBORS_JSON=$(docker-compose exec -T "$SERVICE" curl -sf "http://$CTRL_ADDR/neighbors" 2>/dev/null || echo "[]")
        NEIGHBORS=$(echo "$NEIGHBORS_JSON" | jq 'length' 2>/dev/null || echo "0")
        HEALTH=$(docker-compose exec -T "$SERVICE" curl -sf "http://$CTRL_ADDR/health" >/dev/null 2>&1 && echo "healthy" || echo "unhealthy")
        
        echo "Service: $SERVICE"
        echo "  Status: running ($HEALTH)"
        echo "  Peer ID: $PEER_ID"
        echo "  Control: $CTRL_ADDR"
        echo "  Neighbors: $NEIGHBORS"
        
        if [[ "$NEIGHBORS" -gt 0 ]]; then
          echo "  Connected peers:"
          echo "$NEIGHBORS_JSON" | jq -r '.[] | "    - \(.peer)"' 2>/dev/null || true
        fi
        echo ""
      else
        echo "Service: $SERVICE"
        echo "  Status: running (control file not ready)"
        echo ""
      fi
    else
      echo "Service: $SERVICE"
      echo "  Status: running (control file missing)"
      echo ""
    fi
  else
    echo "Service: $SERVICE"
    echo "  Status: stopped"
    echo ""
  fi
done

#!/usr/bin/env bash
set -euo pipefail

# Purpose: C.2 verification - 2-node cluster, PUT on node A, verify /replication/status returns replica_count>=2 within 10s.
# replica_count>=1 is trivial (putter is always a replica); >=2 proves block was sent to another node.
# Usage: ./verify_replication_integration.sh [--compose <path>] [--our-api <addr>]
# Exit: 0 if replica_count>=2 within 10s; 1 otherwise.
# Uses docker-compose exec (like wait_for_stabilization) for consistency with run_comparison.

echo "C.2 verify: starting..." >&2

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

CURL_OPTS="-sS --connect-timeout 5 --max-time 15"

# Parse args first
COMPOSE=""
OUR_API_ARG=""
while [[ $# -gt 0 ]]; do
  case $1 in
    --compose) COMPOSE="$2"; shift 2 ;;
    --our-api) OUR_API_ARG="$2"; shift 2 ;;
    *) echo "C.2 verify: unknown arg: $1" >&2; exit 1 ;;
  esac
done

# Resolve compose file if not passed (match run_comparison)
if [[ -z "$COMPOSE" ]]; then
  [[ -f "$ROOT_DIR/docker-compose.vnipfs.yml" ]] && COMPOSE="$ROOT_DIR/docker-compose.vnipfs.yml"
  [[ -z "$COMPOSE" && -f "$ROOT_DIR/docker-compose.yml" ]] && COMPOSE="$ROOT_DIR/docker-compose.yml"
fi

# Bootstrap detection: try docker exec first (fall25-bootstrap), then docker-compose
OUR_CONTAINER=""
OUR_API_ADDR=""
USE_COMPOSE=false

if [[ -n "$OUR_API_ARG" ]]; then
  OUR_API_ADDR="$OUR_API_ARG"
  echo "C.2 verify: using --our-api addr=$OUR_API_ADDR" >&2
  docker ps --format '{{.Names}}' 2>/dev/null | grep -q "^fall25-bootstrap$" && OUR_CONTAINER="fall25-bootstrap"
else
  # Try direct docker (container fall25-bootstrap)
  if docker ps --format '{{.Names}}' 2>/dev/null | grep -q "^fall25-bootstrap$"; then
    OUR_CONTAINER="fall25-bootstrap"
    OUR_API_ADDR=$(docker exec "$OUR_CONTAINER" jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
    if [[ -n "$OUR_API_ADDR" && "$OUR_API_ADDR" != "null" ]]; then
      echo "C.2 verify: using container=$OUR_CONTAINER addr=$OUR_API_ADDR" >&2
    fi
  fi

  # Fallback: docker-compose exec
  if [[ -z "$OUR_API_ADDR" || "$OUR_API_ADDR" == "null" ]] && [[ -n "$COMPOSE" && -f "$COMPOSE" ]]; then
    if docker-compose -f "$COMPOSE" ps bootstrap 2>/dev/null | grep -q "Up"; then
      USE_COMPOSE=true
      OUR_API_ADDR=$(docker-compose -f "$COMPOSE" exec -T bootstrap jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
      if [[ -n "$OUR_API_ADDR" && "$OUR_API_ADDR" != "null" ]]; then
        echo "C.2 verify: using docker-compose bootstrap addr=$OUR_API_ADDR" >&2
      fi
    fi
  fi

  if [[ -z "$OUR_API_ADDR" || "$OUR_API_ADDR" == "null" ]]; then
    echo "C.2 verify: no containers or API addr (compose=$COMPOSE)" >&2
    exit 1
  fi
fi

OUR_API="http://$OUR_API_ADDR"

# Helper: run curl inside bootstrap (compose or docker exec)
run_curl() {
  local method="$1"
  local path="$2"
  local data_arg="${3:-}"
  if [[ "$USE_COMPOSE" == "true" ]]; then
    if [[ -n "${data_arg:-}" ]]; then
      docker-compose -f "$COMPOSE" exec -T bootstrap curl $CURL_OPTS -X "$method" -H "Content-Type: application/json" -d "@$data_arg" "http://$OUR_API_ADDR$path" 2>/dev/null || true
    else
      docker-compose -f "$COMPOSE" exec -T bootstrap curl $CURL_OPTS "http://$OUR_API_ADDR$path" 2>/dev/null || true
    fi
  else
    if [[ -n "${data_arg:-}" ]]; then
      docker exec "$OUR_CONTAINER" curl $CURL_OPTS -X "$method" -H "Content-Type: application/json" -d "@$data_arg" "http://$OUR_API_ADDR$path" 2>/dev/null || true
    else
      docker exec "$OUR_CONTAINER" curl $CURL_OPTS "http://$OUR_API_ADDR$path" 2>/dev/null || true
    fi
  fi
}

TEMP_DIR=$(mktemp -d)
trap "rm -rf '$TEMP_DIR'" EXIT

# PUT on node A
dd if=/dev/urandom of="$TEMP_DIR/payload.bin" bs=1 count=1024 2>/dev/null
data_b64=$(base64 -w 0 < "$TEMP_DIR/payload.bin" 2>/dev/null || base64 < "$TEMP_DIR/payload.bin" | tr -d '\n')
echo "{\"data\":\"$data_b64\"}" > "$TEMP_DIR/put.json"

if [[ "$USE_COMPOSE" == "true" ]]; then
  docker cp "$TEMP_DIR/put.json" "$(docker-compose -f "$COMPOSE" ps -q bootstrap):/tmp/verify_put.json" 2>/dev/null || true
else
  docker cp "$TEMP_DIR/put.json" "${OUR_CONTAINER}:/tmp/verify_put.json" 2>/dev/null || true
fi

echo "C.2 verify: PUT request..." >&2
resp=$(run_curl POST /put /tmp/verify_put.json)

if [[ "$USE_COMPOSE" == "true" ]]; then
  docker-compose -f "$COMPOSE" exec -T bootstrap rm -f /tmp/verify_put.json 2>/dev/null || true
else
  docker exec "$OUR_CONTAINER" rm -f /tmp/verify_put.json 2>/dev/null || true
fi

KEY=$(echo "$resp" | jq -r '.multihash_hex // .key // empty' 2>/dev/null || echo "")
if [[ -z "$KEY" || "$KEY" == "null" || ${#KEY} -ne 64 ]]; then
  echo "C.2 verify: PUT failed or no key" >&2
  echo "C.2 verify: response: ${resp:0:300}" >&2
  exit 1
fi

# Poll /replication/status for 10s (1s x 10)
echo "C.2 verify: polling /replication/status..." >&2
for i in $(seq 1 10); do
  count=$(run_curl GET "/replication/status?key=$KEY" | jq -r '.replica_count // 0' || echo "0")
  [[ -z "$count" || "$count" == "null" ]] && count=0
  if [[ "$count" -ge 2 ]]; then
    echo "C.2 verify: replica_count=$count (within ${i}s)"
    exit 0
  fi
  sleep 1
done

echo "C.2 verify: replica_count=$count after 10s (expected >=2)" >&2
exit 1

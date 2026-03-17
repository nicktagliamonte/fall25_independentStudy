#!/usr/bin/env bash
set -euo pipefail

# Purpose: Compare replication distribution N/M/F (Near/Midrange/FarFlung) vs Swarm's approach.
# vn-IPFS uses N/M/F; Swarm uses chunk-based replication (N/A for this metric).
# Output: system,node_count,near,midrange,farflung
# Usage: ./scripts/tests/swarm_comparison/replication_distribution_test.sh [options]

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

source "$ROOT_DIR/scripts/utils/error_handler.sh"
source "$SCRIPT_DIR/api.sh"

RUN_ID="${RUN_ID:-$(date +%s)}"
ERROR_LOG_DIR="artifacts/swarm_tests/$RUN_ID"
export RUN_ID ERROR_LOG_DIR
mkdir -p "$ERROR_LOG_DIR"

OUR_API=""
PAYLOAD_SIZE=65536
REPLICAS_TARGET=2
TIMEOUT_S=120
POLL_INTERVAL_S=2
OUTPUT_FILE="replication_distribution.csv"
NODE_COUNT=""
APPEND=false

while [[ $# -gt 0 ]]; do
  case $1 in
    --our-api)         OUR_API="$2"; shift 2 ;;
    --payload-size)    PAYLOAD_SIZE="$2"; shift 2 ;;
    --replicas-target) REPLICAS_TARGET="$2"; shift 2 ;;
    --timeout)         TIMEOUT_S="$2"; shift 2 ;;
    --poll-interval)   POLL_INTERVAL_S="$2"; shift 2 ;;
    --node-count)      NODE_COUNT="$2"; shift 2 ;;
    --output)          OUTPUT_FILE="$2"; shift 2 ;;
    --append)          APPEND=true; shift ;;
    --help)
      echo "Usage: $0 [options]"
      echo "  --our-api <addr>         Our system (default: auto-detect)"
      echo "  --payload-size <n>       Payload bytes (default: 65536)"
      echo "  --replicas-target <R>    Replicas to reach before sampling (default: 2)"
      echo "  --timeout <s>            Max wait seconds (default: 120)"
      echo "  --poll-interval <s>      Poll interval seconds (default: 2)"
      echo "  --node-count <n>         Node count for output"
      echo "  --output <file>          Output CSV"
      echo "  --append                 Append rows (skip header)"
      exit 0
      ;;
    *) echo "Unknown option: $1" >&2; exit 1 ;;
  esac
done

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

OUR_CONTAINER=""
OUR_API_ADDR=""

if [[ -z "$OUR_API" ]]; then
  if docker ps --format '{{.Names}}' | grep -q "^fall25-bootstrap$"; then
    OUR_CONTAINER="fall25-bootstrap"
    OUR_API_ADDR=$(docker exec "$OUR_CONTAINER" jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
  fi
  if [[ -z "$OUR_API_ADDR" || "$OUR_API_ADDR" == "null" ]]; then
    for compose in "$ROOT_DIR/docker-compose.vnipfs.yml" "$ROOT_DIR/docker-compose.yml"; do
      [[ ! -f "$compose" ]] || ! command -v docker-compose >/dev/null 2>&1 && continue
      if docker-compose -f "$compose" ps bootstrap 2>/dev/null | grep -q "Up"; then
        OUR_CONTAINER="bootstrap"
        OUR_API_ADDR=$(docker-compose -f "$compose" exec -T bootstrap jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
        [[ -n "$OUR_API_ADDR" && "$OUR_API_ADDR" != "null" ]] && break
      fi
    done
  fi
  if [[ -z "$OUR_API_ADDR" || "$OUR_API_ADDR" == "null" ]]; then
    echo -e "${RED}Error: Could not detect our system API.${NC}" >&2
    exit 1
  fi
  OUR_API="http://$OUR_API_ADDR"
fi

if [[ "$OUR_API" =~ ^[a-zA-Z0-9_-]+$ ]]; then
  OUR_CONTAINER="$OUR_API"
  OUR_API_ADDR=$(docker exec "$OUR_CONTAINER" jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
  [[ -n "$OUR_API_ADDR" && "$OUR_API_ADDR" != "null" ]] && OUR_API="http://$OUR_API_ADDR"
fi

if [[ -z "$OUR_CONTAINER" ]]; then
  resolved=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -E '^fall25-bootstrap$|^bootstrap$' | head -1)
  [[ -n "$resolved" ]] && OUR_CONTAINER="$resolved"
fi

if [[ -z "$NODE_COUNT" ]]; then
  vn_count=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -c -E '^fall25-(bootstrap|node)' || echo "0")
  swarm_count=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -c -E '^swarm-(bootstrap|node)' || echo "0")
  NODE_COUNT=$((vn_count > swarm_count ? vn_count : swarm_count))
  [[ "$NODE_COUNT" -lt 1 ]] && NODE_COUNT=1
fi

TEMP_DIR=$(mktemp -d)
trap "rm -rf '$TEMP_DIR'" EXIT

if [[ "$APPEND" != "true" ]]; then
  echo "system,node_count,near,midrange,farflung" > "$OUTPUT_FILE"
fi

echo -e "${BLUE}Replication Distribution Test (N/M/F vs Swarm)${NC}"
echo "  Payload size: $PAYLOAD_SIZE, replicas target: $REPLICAS_TARGET"
echo ""

dd if=/dev/urandom of="$TEMP_DIR/payload.bin" bs=1 count="$PAYLOAD_SIZE" 2>/dev/null
data_b64=$(base64 -w 0 < "$TEMP_DIR/payload.bin" 2>/dev/null || base64 < "$TEMP_DIR/payload.bin" | tr -d '\n')
echo "{\"data\":\"$data_b64\"}" > "$TEMP_DIR/put_req.json"
docker cp "$TEMP_DIR/put_req.json" "${OUR_CONTAINER}:/tmp/put_req_dist_$$.json" >/dev/null 2>&1
resp=$(docker exec "$OUR_CONTAINER" curl -sSf -X POST -H "Content-Type: application/json" \
  -d @/tmp/put_req_dist_$$.json "http://$OUR_API_ADDR/put" 2>/dev/null || echo "{}")
docker exec "$OUR_CONTAINER" rm -f /tmp/put_req_dist_$$.json >/dev/null 2>&1 || true

KEY=$(echo "$resp" | jq -r '.multihash_hex // .cid // empty')
if [[ -z "$KEY" || "$KEY" == "null" ]]; then
  cid_val=$(echo "$resp" | jq -r '.cid // empty')
  [[ -n "$cid_val" ]] && KEY=$(echo "$cid_val" | grep -oE '[a-fA-F0-9]{64}' || echo "$cid_val" | sed 's/.*Qm//' | head -c 64)
fi

if [[ -z "$KEY" || ${#KEY} -lt 32 ]]; then
  echo -e "${RED}Upload failed, no key. Skipping distribution.${NC}" >&2
  exit 1
fi

echo -e "${GREEN}Our system: put done, waiting for R >= $REPLICAS_TARGET...${NC}"
start=$(date +%s)
while true; do
  now=$(date +%s)
  elapsed=$((now - start))
  [[ $elapsed -ge $TIMEOUT_S ]] && break
  count=$(docker exec "$OUR_CONTAINER" curl -sSf "http://$OUR_API_ADDR/replication/status?key=$KEY&simulate_distances=1" 2>/dev/null | jq -r '.replica_count // 0' || echo "0")
  [[ -z "$count" || "$count" == "null" ]] && count=0
  [[ "$count" -ge "$REPLICAS_TARGET" ]] && break
  sleep "$POLL_INTERVAL_S"
done

status=$(docker exec "$OUR_CONTAINER" curl -sSf "http://$OUR_API_ADDR/replication/status?key=$KEY&simulate_distances=1" 2>/dev/null || echo "{}")
near=$(echo "$status" | jq -r '.near_count // 0')
midrange=$(echo "$status" | jq -r '.midrange_count // 0')
farflung=$(echo "$status" | jq -r '.farflung_count // 0')
echo "  N/M/F: near=$near, midrange=$midrange, farflung=$farflung"
echo "our_system,$NODE_COUNT,$near,$midrange,$farflung" >> "$OUTPUT_FILE"

echo -e "\n${GREEN}Swarm: chunk-based replication (no N/M/F)${NC}"
echo "  Swarm uses chunk-based push sync; N/M/F not applicable."
echo "swarm,$NODE_COUNT,N/A,N/A,N/A" >> "$OUTPUT_FILE"

echo ""
echo "Results: $OUTPUT_FILE"
cat "$OUTPUT_FILE"

#!/usr/bin/env bash
set -euo pipefail

# Purpose: Measure isolated lookup latency (token routing vs provider discovery).
# vn-IPFS: /lookup endpoint does GetToken only (no fetch). Swarm: TTFB as lookup proxy (discovery before first byte).
# Output: system,iteration,lookup_latency_ms,network_hops,lookup_type
# Usage: ./scripts/tests/swarm_comparison/lookup_latency_test.sh [options]

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"
source "$SCRIPT_DIR/api.sh"

OUR_API=""
SWARM_API="${SWARM_API:-http://127.0.0.1:8500}"
ITERATIONS=10
PAYLOAD_SIZE=10240
OUTPUT_FILE="lookup_latency_results.csv"

while [[ $# -gt 0 ]]; do
  case $1 in
    --our-api)   OUR_API="$2"; shift 2 ;;
    --swarm-api) SWARM_API="$2"; shift 2 ;;
    --iterations) ITERATIONS="$2"; shift 2 ;;
    --output)    OUTPUT_FILE="$2"; shift 2 ;;
    --help)
      echo "Usage: $0 [options]"
      echo "  Isolated lookup latency: token routing (vn-IPFS) vs provider discovery (Swarm TTFB proxy)."
      echo "  --our-api, --swarm-api, --iterations, --output"
      exit 0
      ;;
    *) echo "Unknown option: $1" >&2; exit 1 ;;
  esac
done

source "$SCRIPT_DIR/comparison_system_env.sh"
cmp_resolve_system_flags

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

OUR_CONTAINER=""
OUR_API_ADDR=""
if [[ "${CMP_INCLUDE_OUR:-1}" == "1" ]] && [[ -z "$OUR_API" ]]; then
  if docker ps --format '{{.Names}}' | grep -q "^fall25-bootstrap$"; then
    OUR_CONTAINER="fall25-bootstrap"
    OUR_API_ADDR=$(docker exec "$OUR_CONTAINER" jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
  fi
  [[ -z "$OUR_API_ADDR" || "$OUR_API_ADDR" == "null" ]] && for compose in "$ROOT_DIR/docker-compose.vnipfs.yml" "$ROOT_DIR/docker-compose.yml"; do
    [[ ! -f "$compose" ]] && continue
    if docker-compose -f "$compose" ps bootstrap 2>/dev/null | grep -q "Up"; then
      OUR_CONTAINER="bootstrap"
      OUR_API_ADDR=$(docker-compose -f "$compose" exec -T bootstrap jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
      [[ -n "$OUR_API_ADDR" && "$OUR_API_ADDR" != "null" ]] && break
    fi
  done
  if [[ -z "$OUR_API_ADDR" || "$OUR_API_ADDR" == "null" ]]; then
    echo -e "${RED}Error: Could not detect our system API. Specify --our-api.${NC}" >&2
    exit 1
  fi
  OUR_API="http://$OUR_API_ADDR"
elif [[ "${CMP_INCLUDE_OUR:-1}" == "1" ]]; then
  [[ "$OUR_API" =~ ^[a-zA-Z0-9_-]+$ ]] && OUR_CONTAINER="$OUR_API" && OUR_API_ADDR=$(docker exec "$OUR_CONTAINER" jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
  [[ -n "$OUR_API_ADDR" && "$OUR_API_ADDR" != "null" ]] && OUR_API="http://$OUR_API_ADDR"
else
  OUR_API=""
  OUR_CONTAINER=""
  OUR_API_ADDR=""
fi

echo "Lookup latency test (isolated: token routing vs provider discovery)"
echo "  vn-IPFS: /lookup (GetToken only). Swarm: TTFB as lookup proxy."
echo "  our=$OUR_API swarm=$SWARM_API iterations=$ITERATIONS"
echo "  Output: $OUTPUT_FILE"
echo ""

TEMP_DIR=$(mktemp -d)
trap "rm -rf $TEMP_DIR" EXIT

echo "system,iteration,lookup_latency_ms,network_hops,lookup_type" > "$OUTPUT_FILE"

# --- vn-IPFS: isolated token lookup via /lookup ---
if [[ "${CMP_INCLUDE_OUR:-1}" == "1" ]]; then
echo -e "${GREEN}Our system (token routing, isolated lookup)...${NC}"
dd if=/dev/urandom of="$TEMP_DIR/p.bin" bs=1 count="$PAYLOAD_SIZE" 2>/dev/null
data_b64=$(base64 -w 0 < "$TEMP_DIR/p.bin" 2>/dev/null || base64 < "$TEMP_DIR/p.bin" | tr -d '\n')
put_json="$TEMP_DIR/put.json"
echo "{\"data\":\"$data_b64\"}" > "$put_json"
docker cp "$put_json" "${OUR_CONTAINER}:/tmp/put.json" 2>/dev/null || true
put_resp=$(docker exec "$OUR_CONTAINER" curl -sSf -X POST -H "Content-Type: application/json" -d @/tmp/put.json "http://$OUR_API_ADDR/put" 2>/dev/null || echo "{}")
docker exec "$OUR_CONTAINER" rm -f /tmp/put.json 2>/dev/null || true
key=$(echo "$put_resp" | jq -r '.multihash_hex // .cid // empty' 2>/dev/null || echo "")

if [[ -z "$key" || "$key" == "null" ]]; then
  echo -e "${RED}Our put failed, skipping lookup test${NC}"
  for i in $(seq 1 "$ITERATIONS"); do echo "our_system,$i,FAILED,,key" >> "$OUTPUT_FILE"; done
else
  for i in $(seq 1 "$ITERATIONS"); do
    lookup_resp=$(docker exec "$OUR_CONTAINER" curl -sSf "http://$OUR_API_ADDR/lookup?key=$key" 2>/dev/null || echo "{}")
    lat=$(echo "$lookup_resp" | jq -r '.lookup_latency_ms // empty' 2>/dev/null || echo "")
    hops=$(echo "$lookup_resp" | jq -r '.network_hops // empty' 2>/dev/null || echo "")
    if [[ -n "$lat" && "$lat" != "null" ]]; then
      echo "our_system,$i,$lat,${hops:-},key" >> "$OUTPUT_FILE"
      echo "    $i: ${lat}ms, hops=${hops:-}"
    else
      echo "our_system,$i,FAILED,,key" >> "$OUTPUT_FILE"
    fi
  done
fi
else
  echo -e "${YELLOW}Skipping vn-IPFS lookup (Swarm-only run)${NC}"
fi

# --- Swarm: TTFB as lookup proxy (discovery happens before first byte) ---
if [[ "${CMP_INCLUDE_SWARM:-1}" == "1" ]]; then
echo -e "\n${GREEN}Swarm (provider discovery, TTFB as lookup proxy)...${NC}"
[[ -f "$TEMP_DIR/p.bin" ]] || dd if=/dev/urandom of="$TEMP_DIR/p.bin" bs=1 count="$PAYLOAD_SIZE" 2>/dev/null
SWARM_HASH=$(upload_file "$SWARM_API" "$TEMP_DIR/p.bin" 2>/dev/null || echo "")
if [[ -z "$SWARM_HASH" || ${#SWARM_HASH} -lt 64 ]]; then
  echo -e "${RED}Swarm upload failed${NC}"
  for i in $(seq 1 "$ITERATIONS"); do echo "swarm,$i,FAILED,N/A,cid" >> "$OUTPUT_FILE"; done
else
  for i in $(seq 1 "$ITERATIONS"); do
    ttfb=$(curl -sSfL -o /dev/null -w "%{time_starttransfer}" "$SWARM_API/bzz:/$SWARM_HASH/" 2>/dev/null || echo "")
    if [[ -n "$ttfb" && "$ttfb" =~ ^[0-9.]+$ ]]; then
      ttfb_ms=$(echo "scale=2; $ttfb * 1000" | bc -l 2>/dev/null || echo "")
      echo "swarm,$i,$ttfb_ms,N/A,cid" >> "$OUTPUT_FILE"
      echo "    $i: ${ttfb_ms}ms (TTFB proxy)"
    else
      echo "swarm,$i,FAILED,N/A,cid" >> "$OUTPUT_FILE"
    fi
  done
fi
else
  echo -e "${YELLOW}Skipping Swarm lookup (vn-IPFS-only run)${NC}"
fi

echo ""
echo "Results: $OUTPUT_FILE"
cat "$OUTPUT_FILE"

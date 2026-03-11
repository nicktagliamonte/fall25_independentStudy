#!/usr/bin/env bash
set -euo pipefail

# Purpose: O(log N) lookup complexity verification - record DHT lookup hops at N=10,50,100,500.
# Output: system,node_count,operation,hops (CSV)
# Usage: ./scripts/tests/swarm_comparison/lookup_complexity_test.sh [options]
#   --our-api <container>   Our system container (default: auto-detect bootstrap)
#   --node-count <n>        Node count for CSV label (default: from running containers)
#   --iterations <n>        Iterations (default: 10)
#   --output <file>         Output CSV (default: lookup_complexity_results.csv)
#   --append                Append to output; do not overwrite header (for multi-N runs)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

OUR_API=""
NODE_COUNT=""
ITERATIONS=10
OUTPUT_FILE="lookup_complexity_results.csv"
APPEND=false
PAYLOAD_SIZE=4096

while [[ $# -gt 0 ]]; do
  case $1 in
    --our-api)      OUR_API="$2"; shift 2 ;;
    --node-count)   NODE_COUNT="$2"; shift 2 ;;
    --iterations)   ITERATIONS="$2"; shift 2 ;;
    --output)       OUTPUT_FILE="$2"; shift 2 ;;
    --append)       APPEND=true; shift ;;
    --help)
      echo "Usage: $0 [--our-api <container>] [--node-count <n>] [--iterations <n>] [--output <file>] [--append]"
      echo "  Run lookups, record DHT hop count. vn-IPFS reports hops; Swarm does not."
      echo "  For N=10,50,100,500: invoke once per N with --node-count N --append (caller starts N nodes)."
      exit 0
      ;;
    *) echo "Unknown option: $1" >&2; exit 1 ;;
  esac
done

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
    echo "Error: Could not detect our system API. Specify --our-api <container>" >&2
    exit 1
  fi
  OUR_API="http://$OUR_API_ADDR"
else
  if [[ "$OUR_API" =~ ^[a-zA-Z0-9_-]+$ ]]; then
    OUR_CONTAINER="$OUR_API"
    OUR_API_ADDR=$(docker exec "$OUR_CONTAINER" jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
    OUR_API="http://$OUR_API_ADDR"
  fi
fi

if [[ -z "$OUR_CONTAINER" ]]; then
  resolved=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -E '^fall25-bootstrap$|^bootstrap' | head -1)
  [[ -n "$resolved" ]] && OUR_CONTAINER="$resolved"
fi

detect_node_count() {
  docker ps --format '{{.Names}}' 2>/dev/null | grep -c -E '^fall25-(bootstrap|node)' || echo "0"
}

if [[ -z "$NODE_COUNT" ]]; then
  NODE_COUNT=$(detect_node_count)
  [[ -z "$NODE_COUNT" || "$NODE_COUNT" -lt 1 ]] && NODE_COUNT=1
fi

TEMP_DIR=$(mktemp -d)
trap "rm -rf $TEMP_DIR" EXIT

write_header="true"
[[ "$APPEND" == "true" ]] && [[ -f "$OUTPUT_FILE" ]] && [[ -s "$OUTPUT_FILE" ]] && write_header=""

put_req() {
  local data_b64="$1"
  local json="$TEMP_DIR/put_$$.json"
  echo "{\"data\":\"$data_b64\"}" > "$json"
  docker cp "$json" "${OUR_CONTAINER}:/tmp/put_$$.json" 2>/dev/null || return 1
  docker exec "$OUR_CONTAINER" curl -sSf -X POST -H "Content-Type: application/json" \
    -d @/tmp/put_$$.json "http://$OUR_API_ADDR/put" 2>/dev/null || echo "{}"
  docker exec "$OUR_CONTAINER" rm -f /tmp/put_$$.json 2>/dev/null || true
}

get_req() {
  local key="$1"
  local json="$TEMP_DIR/get_$$.json"
  echo "{\"key\":\"$key\",\"timeout\":\"30s\"}" > "$json"
  docker cp "$json" "${OUR_CONTAINER}:/tmp/get_$$.json" 2>/dev/null || return 1
  docker exec "$OUR_CONTAINER" curl -sSf -X POST -H "Content-Type: application/json" \
    -d @/tmp/get_$$.json "http://$OUR_API_ADDR/get" 2>/dev/null || echo "{}"
  docker exec "$OUR_CONTAINER" rm -f /tmp/get_$$.json 2>/dev/null || true
}

[[ -n "$write_header" ]] && echo "system,node_count,operation,hops,lookup_type" > "$OUTPUT_FILE"

echo "=========================================="
echo "Lookup Complexity Test (O(log N) verification)"
echo "=========================================="
echo "API: $OUR_API | Node count: $NODE_COUNT | Iterations: $ITERATIONS"
echo "Output: $OUTPUT_FILE"
echo ""

for i in $(seq 1 "$ITERATIONS"); do
  test_file="$TEMP_DIR/test_$$.bin"
  dd if=/dev/urandom of="$test_file" bs=1 count="$PAYLOAD_SIZE" 2>/dev/null
  data_b64=$(base64 -w 0 < "$test_file" 2>/dev/null || base64 < "$test_file" | tr -d '\n')
  put_resp=$(put_req "$data_b64" 2>/dev/null || echo "{}")
  key=$(echo "$put_resp" | jq -r '.multihash_hex // empty' 2>/dev/null || echo "")
  if [[ -z "$key" || "$key" == "null" ]]; then
    echo "our_system,$NODE_COUNT,get,N/A,key" >> "$OUTPUT_FILE"
    continue
  fi
  put_hops=$(echo "$put_resp" | jq -r '.network_hops // empty' 2>/dev/null || echo "")
  [[ -n "$put_hops" && "$put_hops" != "null" ]] && echo "our_system,$NODE_COUNT,put,$put_hops,key" >> "$OUTPUT_FILE"
  get_resp=$(get_req "$key" 2>/dev/null || echo "{}")
  get_hops=$(echo "$get_resp" | jq -r '.network_hops // empty' 2>/dev/null || echo "")
  echo "our_system,$NODE_COUNT,get,${get_hops:-},key" >> "$OUTPUT_FILE"
done

echo ""
echo "Results: $OUTPUT_FILE"
echo "  Format: system,node_count,operation,hops,lookup_type"

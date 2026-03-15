#!/usr/bin/env bash
set -euo pipefail

# Purpose: Key-based lookup vs CID-based retrieval - measure latency and hop count for same logical operation (store X, fetch X).
# Semantic equivalence: vn-IPFS key K = SHA256(payload); Swarm BZZ hash = content hash of payload. Both identify content by digest.
# Same logical flow: store P, fetch P by its content-derived identifier. Output: system,operation,latency_ms,hops,lookup_type
# Usage: ./scripts/tests/swarm_comparison/key_lookup_vs_cid_test.sh [options]
#   --our-api <addr>     Our system API (default: auto-detect)
#   --swarm-api <addr>   Swarm API (default: http://172.20.0.200:8500)
#   --iterations <n>     Iterations (default: 5)
#   --payload-size <n>   Payload bytes (default: 10240)
#   --output <file>      Output CSV (default: key_lookup_vs_cid_results.csv)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

source "$SCRIPT_DIR/api.sh"

OUR_API=""
SWARM_API="http://172.20.0.200:8500"
ITERATIONS=5
PAYLOAD_SIZE=10240
OUTPUT_FILE="key_lookup_vs_cid_results.csv"

while [[ $# -gt 0 ]]; do
  case $1 in
    --our-api)       OUR_API="$2"; shift 2 ;;
    --swarm-api)     SWARM_API="$2"; shift 2 ;;
    --iterations)    ITERATIONS="$2"; shift 2 ;;
    --payload-size)  PAYLOAD_SIZE="$2"; shift 2 ;;
    --output)        OUTPUT_FILE="$2"; shift 2 ;;
    --help)
      echo "Usage: $0 [options]"
      echo "  Measure latency and hop count for store X, fetch X (key vs CID)."
      echo "  --our-api, --swarm-api, --iterations, --payload-size, --output"
      exit 0
      ;;
    *) echo "Unknown option: $1" >&2; exit 1 ;;
  esac
done

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
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
      [[ ! -f "$compose" ]] && continue
      if docker-compose -f "$compose" ps bootstrap 2>/dev/null | grep -q "Up"; then
        OUR_CONTAINER="bootstrap"
        OUR_API_ADDR=$(docker-compose -f "$compose" exec -T bootstrap jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
        [[ -n "$OUR_API_ADDR" && "$OUR_API_ADDR" != "null" ]] && break
      fi
    done
  fi
  if [[ -z "$OUR_API_ADDR" || "$OUR_API_ADDR" == "null" ]]; then
    echo -e "${RED}Error: Could not detect our system API. Specify --our-api.${NC}" >&2
    exit 1
  fi
  OUR_API="http://$OUR_API_ADDR"
else
  if [[ "$OUR_API" =~ ^[a-zA-Z0-9_-]+$ ]]; then
    OUR_CONTAINER="$OUR_API"
    OUR_API_ADDR=$(docker exec "$OUR_CONTAINER" jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
    [[ -n "$OUR_API_ADDR" && "$OUR_API_ADDR" != "null" ]] && OUR_API="http://$OUR_API_ADDR"
  fi
fi

echo "Key lookup vs CID retrieval test"
echo "  store X, fetch X — same payload, same logical operation"
echo "  Semantic equivalence: key K = content hash of payload (SHA256); Swarm CID/BZZ = content hash"
echo "  our=$OUR_API swarm=$SWARM_API iterations=$ITERATIONS payload=$PAYLOAD_SIZE"
echo "  Output: $OUTPUT_FILE"
echo ""

TEMP_DIR=$(mktemp -d)
trap "rm -rf $TEMP_DIR" EXIT

echo "system,operation,latency_ms,hops,lookup_type" > "$OUTPUT_FILE"
# B.1: Put and get on same node yields hops=0 (local). B.2: Put on bootstrap, get from worker (cold) for non-zero hops.

# --- vn-IPFS (key-based) ---
# Put on bootstrap: GET from worker (cold) for non-zero hops; local GET on same node yields hops=0
PUT_CONTAINER="$OUR_CONTAINER"
PUT_API_ADDR="$OUR_API_ADDR"
WORKER_CONTAINER=""
WORKER_API_ADDR=""
for c in $(docker ps --format '{{.Names}}' 2>/dev/null | grep -E '^fall25-node[0-9]+$' || true); do
  addr=$(docker exec "$c" jq -r '.addr // .Addr' /app/logs/"${c#fall25-}".json 2>/dev/null || echo "")
  if [[ -n "$addr" && "$addr" != "null" ]]; then
    WORKER_CONTAINER="$c"
    WORKER_API_ADDR="$addr"
    break
  fi
done
if [[ -z "$WORKER_CONTAINER" ]]; then
  WORKER_CONTAINER="$PUT_CONTAINER"
  WORKER_API_ADDR="$PUT_API_ADDR"
  echo -e "${YELLOW}No worker node; put and get on same node (hops may be 0)${NC}"
fi

echo -e "${GREEN}Our system (key-based lookup)...${NC}"
for i in $(seq 1 "$ITERATIONS"); do
  test_file="$TEMP_DIR/x_$$.bin"
  dd if=/dev/urandom of="$test_file" bs=1 count="$PAYLOAD_SIZE" 2>/dev/null
  data_b64=$(base64 -w 0 < "$test_file" 2>/dev/null || base64 < "$test_file" | tr -d '\n')

  json="$TEMP_DIR/put_$$.json"
  echo "{\"data\":\"$data_b64\"}" > "$json"
  docker cp "$json" "${PUT_CONTAINER}:/tmp/put_$$.json" 2>/dev/null || true
  start=$(date +%s.%N)
  put_resp=$(docker exec "$PUT_CONTAINER" curl -sSf -X POST -H "Content-Type: application/json" -d @/tmp/put_$$.json "http://$PUT_API_ADDR/put" 2>/dev/null || echo "{}")
  end=$(date +%s.%N)
  docker exec "$PUT_CONTAINER" rm -f /tmp/put_$$.json 2>/dev/null || true
  put_latency=$(echo "scale=2; ($end - $start) * 1000" | bc -l 2>/dev/null || echo "0")
  put_hops=$(echo "$put_resp" | jq -r '.network_hops // empty' 2>/dev/null || echo "")
  key=$(echo "$put_resp" | jq -r '.multihash_hex // .key // .cid // empty' 2>/dev/null || echo "")

  if [[ -z "$key" || "$key" == "null" ]]; then
    echo "our_system,put,FAILED,,key" >> "$OUTPUT_FILE"
    echo "our_system,get,FAILED,,key" >> "$OUTPUT_FILE"
    continue
  fi
  echo "our_system,put,$put_latency,${put_hops:-},key" >> "$OUTPUT_FILE"

  # B.4: /lookup returns lookup_latency_ms and network_hops for isolated GetToken; call from worker (cold) for DHT hops
  LOOKUP_CONTAINER="$WORKER_CONTAINER"
  LOOKUP_API_ADDR="$WORKER_API_ADDR"
  lookup_resp=$(docker exec "$LOOKUP_CONTAINER" curl -sSf "http://$LOOKUP_API_ADDR/lookup?key=$key" 2>/dev/null || echo "{}")
  lookup_latency=$(echo "$lookup_resp" | jq -r '.lookup_latency_ms // empty' 2>/dev/null || echo "")
  lookup_hops=$(echo "$lookup_resp" | jq -r '.network_hops // empty' 2>/dev/null || echo "")
  echo "our_system,lookup,${lookup_latency:-},${lookup_hops:-},key" >> "$OUTPUT_FILE"

  get_json="$TEMP_DIR/get_$$.json"
  echo "{\"key\":\"$key\",\"timeout\":\"30s\"}" > "$get_json"
  docker cp "$get_json" "${WORKER_CONTAINER}:/tmp/get_$$.json" 2>/dev/null || true
  start=$(date +%s.%N)
  get_resp=$(docker exec "$WORKER_CONTAINER" curl -sSf -X POST -H "Content-Type: application/json" -d @/tmp/get_$$.json "http://$WORKER_API_ADDR/get" 2>/dev/null || echo "{}")
  end=$(date +%s.%N)
  docker exec "$WORKER_CONTAINER" rm -f /tmp/get_$$.json 2>/dev/null || true
  get_latency=$(echo "scale=2; ($end - $start) * 1000" | bc -l 2>/dev/null || echo "0")
  get_hops=$(echo "$get_resp" | jq -r '.network_hops // empty' 2>/dev/null || echo "")
  echo "our_system,get,$get_latency,${get_hops:-},key" >> "$OUTPUT_FILE"
  # C.4 verification: when put on bootstrap and get from worker, pass when hops > 0 OR replica_count >= 2.
  # With aggressive replication (R=7), worker receives block during PUT replication before GET runs,
  # so content is local on worker and hops=0 is correct. Allow hops=0 when replica_count >= 2.
  if [[ "$WORKER_CONTAINER" != "$PUT_CONTAINER" ]]; then
    get_succeeded=$(echo "$get_resp" | jq -r 'if .data_b64 != null and .data_b64 != "" then "1" else "0" end' 2>/dev/null || echo "0")
    if [[ "$get_succeeded" == "1" ]]; then
      get_hops_num=$(echo "$get_hops" | grep -E '^[0-9]+$' || echo "0")
      lookup_hops_num=$(echo "$lookup_hops" | grep -E '^[0-9]+$' || echo "0")
      if [[ "${get_hops_num:-0}" -eq 0 && "${lookup_hops_num:-0}" -eq 0 ]]; then
        replica_count=$(docker exec "$PUT_CONTAINER" curl -sSf "http://$PUT_API_ADDR/replication/status?key=$key" 2>/dev/null | jq -r '.replica_count // 0' || echo "0")
        if [[ "${replica_count:-0}" -lt 2 ]]; then
          echo -e "${RED}C.4 verification failed: put on bootstrap, get from worker succeeded, but hops=0 and replica_count=$replica_count (expected hops>0 when content not replicated)${NC}" >&2
          exit 1
        fi
      fi
    fi
  fi
done

# --- Swarm (CID/content-hash based) ---
echo -e "\n${GREEN}Swarm (CID-based retrieval)...${NC}"
for i in $(seq 1 "$ITERATIONS"); do
  test_file="$TEMP_DIR/x_$$.bin"
  dd if=/dev/urandom of="$test_file" bs=1 count="$PAYLOAD_SIZE" 2>/dev/null

  start=$(date +%s.%N)
  hash=$(upload_file "$SWARM_API" "$test_file" 2>/dev/null || echo "")
  end=$(date +%s.%N)
  put_latency=$(echo "scale=2; ($end - $start) * 1000" | bc -l 2>/dev/null || echo "0")

  if [[ -z "$hash" || ${#hash} -lt 64 ]]; then
    echo "swarm,put,FAILED,N/A,cid" >> "$OUTPUT_FILE"
    echo "swarm,get,FAILED,N/A,cid" >> "$OUTPUT_FILE"
    continue
  fi
  echo "swarm,put,$put_latency,N/A,cid" >> "$OUTPUT_FILE"

  out="$TEMP_DIR/swarm_get_$$.bin"
  start=$(date +%s.%N)
  if download_file "$SWARM_API" "$hash" "$out" 2>/dev/null && [[ -f "$out" && -s "$out" ]] && ! grep -q "<a href=" "$out" 2>/dev/null; then
    end=$(date +%s.%N)
    get_latency=$(echo "scale=2; ($end - $start) * 1000" | bc -l 2>/dev/null || echo "0")
  else
    get_latency="FAILED"
  fi
  echo "swarm,get,$get_latency,N/A,cid" >> "$OUTPUT_FILE"
done

echo ""
echo "Results: $OUTPUT_FILE"
cat "$OUTPUT_FILE"

#!/usr/bin/env bash
set -euo pipefail

# Purpose: Record network_hops per put/get operation for vn-IPFS.
# Output: system,operation,payload_size,iteration,hops (CSV)
# Usage: ./scripts/tests/swarm_comparison/network_hops_test.sh [options]
#   --our-api <container>  Our system container (default: auto-detect bootstrap)
#   --iterations <n>       Iterations per size (default: 10)
#   --output <file>        Output CSV (default: network_hops_results.csv)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

OUR_API=""
ITERATIONS=10
OUTPUT_FILE="network_hops_results.csv"
PAYLOAD_SIZES=(1024 10240 102400)

while [[ $# -gt 0 ]]; do
  case $1 in
    --our-api)   OUR_API="$2"; shift 2 ;;
    --iterations) ITERATIONS="$2"; shift 2 ;;
    --output)    OUTPUT_FILE="$2"; shift 2 ;;
    --help)
      echo "Usage: $0 [--our-api <container>] [--iterations <n>] [--output <file>]"
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
      if [[ -f "$compose" ]] && docker-compose -f "$compose" ps bootstrap 2>/dev/null | grep -q "Up"; then
        OUR_CONTAINER="bootstrap"
        OUR_API_ADDR=$(docker-compose -f "$compose" exec -T bootstrap jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
        break
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

PUT_CONTAINER="$OUR_CONTAINER"
PUT_API_ADDR="$OUR_API_ADDR"
if [[ "$PUT_CONTAINER" == "bootstrap" ]]; then
  for compose in "$ROOT_DIR/docker-compose.vnipfs.yml" "$ROOT_DIR/docker-compose.yml"; do
    if [[ -f "$compose" ]] && docker-compose -f "$compose" ps bootstrap 2>/dev/null | grep -q "Up"; then
      PUT_CONTAINER=$(docker-compose -f "$compose" ps -q bootstrap 2>/dev/null | xargs -r docker inspect -f '{{.Name}}' 2>/dev/null | sed 's|^/||' || echo "fall25-bootstrap")
      [[ -z "$PUT_CONTAINER" ]] && PUT_CONTAINER="fall25-bootstrap"
      break
    fi
  done
fi

BOOTSTRAP_MA=""
peer_id=$(docker exec "$PUT_CONTAINER" curl -sSf "http://$PUT_API_ADDR/id" 2>/dev/null | jq -r '.peer // empty' 2>/dev/null || echo "")
if [[ -n "$peer_id" && "$peer_id" != "null" ]]; then
  BOOTSTRAP_MA="/dns4/${PUT_CONTAINER}/tcp/4001/p2p/${peer_id}"
fi

cold_lookup_req() {
  local key="$1"
  local bootstrap_ma="$2"
  [[ -z "$bootstrap_ma" ]] && echo "{}" && return
  local net
  net=$(docker inspect "$PUT_CONTAINER" --format '{{range $k, $v := .NetworkSettings.Networks}}{{$k}}{{end}}' 2>/dev/null | head -1)
  [[ -z "$net" ]] && net="fall25_independentstudy_node-network"
  local img
  img=$(docker inspect "$PUT_CONTAINER" --format '{{.Config.Image}}' 2>/dev/null || echo "")
  [[ -z "$img" ]] && img=$(docker inspect "$PUT_CONTAINER" --format '{{.Image}}' 2>/dev/null || echo "")
  [[ -z "$img" ]] && echo "{}" && return
  docker run --rm --network "$net" "$img" lookup-key --bootstrap "$bootstrap_ma" --key "$key" --timeout 180s 2>/dev/null || echo "{}"
}

echo "=========================================="
echo "Network Hops Test (vn-IPFS)"
echo "=========================================="
echo "API: $OUR_API (PUT on $PUT_CONTAINER, GET hops via cold lookup)"
echo "Iterations: $ITERATIONS"
echo "Output: $OUTPUT_FILE"
echo ""

TEMP_DIR=$(mktemp -d)
trap "rm -rf $TEMP_DIR" EXIT

echo "system,operation,payload_size,iteration,hops" > "$OUTPUT_FILE"

put_req() {
  local data_b64="$1"
  local json="$TEMP_DIR/put_$$.json"
  echo "{\"data\":\"$data_b64\"}" > "$json"
  docker cp "$json" "${PUT_CONTAINER}:/tmp/put_$$.json" >/dev/null 2>&1
  local resp=$(docker exec "$PUT_CONTAINER" curl -sSf -X POST -H "Content-Type: application/json" \
    -d @/tmp/put_$$.json "http://$PUT_API_ADDR/put" 2>&1)
  docker exec "$PUT_CONTAINER" rm -f /tmp/put_$$.json >/dev/null 2>&1 || true
  echo "$resp"
}

[[ -n "$BOOTSTRAP_MA" ]] && echo "  Waiting 15s for DHT before first cold lookup..." && sleep 15

for size in "${PAYLOAD_SIZES[@]}"; do
  echo "Payload size: $size bytes"
  test_file="$TEMP_DIR/test_${size}.bin"
  dd if=/dev/urandom of="$test_file" bs=1 count="$size" 2>/dev/null
  data_b64=$(base64 -w 0 < "$test_file" 2>/dev/null || base64 < "$test_file" | tr -d '\n')

  for i in $(seq 1 $ITERATIONS); do
    put_resp=$(put_req "$data_b64" 2>/dev/null || echo "{}")
    put_hops=$(echo "$put_resp" | jq -r '.network_hops // empty' 2>/dev/null || echo "")
    key=$(echo "$put_resp" | jq -r '.multihash_hex // empty' 2>/dev/null || echo "")
    if [[ -z "$key" || "$key" == "null" ]]; then
      echo "vn-ipfs,put,$size,$i," >> "$OUTPUT_FILE"
      echo "vn-ipfs,get,$size,$i," >> "$OUTPUT_FILE"
      continue
    fi

    echo "vn-ipfs,put,$size,$i,${put_hops:-}" >> "$OUTPUT_FILE"

    get_hops=""
    if [[ -n "$BOOTSTRAP_MA" ]]; then
      lookup_resp=$(cold_lookup_req "$key" "$BOOTSTRAP_MA" 2>/dev/null || echo "{}")
      get_hops=$(echo "$lookup_resp" | jq -r '.network_hops // empty' 2>/dev/null || echo "")
      for retry in 1 2; do
        [[ -n "$get_hops" && "$get_hops" != "null" ]] && break
        sleep $((3 * retry))
        lookup_resp=$(cold_lookup_req "$key" "$BOOTSTRAP_MA" 2>/dev/null || echo "{}")
        get_hops=$(echo "$lookup_resp" | jq -r '.network_hops // empty' 2>/dev/null || echo "")
      done
    fi
    echo "vn-ipfs,get,$size,$i,${get_hops:-}" >> "$OUTPUT_FILE"
  done
done

echo ""
echo "Results: $OUTPUT_FILE"
echo "  Format: system,operation,payload_size,iteration,hops"

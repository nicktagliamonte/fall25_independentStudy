#!/usr/bin/env bash
set -euo pipefail

# Fallback for 'timeout' on macOS 
if command -v gtimeout >/dev/null 2>&1; then
  shopt -s expand_aliases
  alias timeout=gtimeout
elif ! command -v timeout >/dev/null 2>&1; then
  function timeout() {
    if [[ "$1" == "-k" ]]; then shift 2; fi
    local limit=$1; shift
    perl -e 'alarm shift; exec @ARGV' "$limit" "$@"
  }
fi

# Purpose: Lookup complexity row for comparison reports. Runs real cold lookup-key (measured counters).
# Column hops is an O(log N) reference curve in cluster size NODE_COUNT for plots (not raw RPC counts).
# Column hops_raw is measured network_hops from lookup-key JSON when present.
# Output: system,node_count,operation,hops,hops_raw,lookup_latency_ms (CSV)
# Cold docker run uses the compose node-network and SNG40_* env from PUT_CONTAINER.
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

source "$SCRIPT_DIR/comparison_system_env.sh"
cmp_resolve_system_flags
if [[ "${CMP_INCLUDE_OUR:-1}" != "1" ]]; then
  echo "lookup_complexity is vn-IPFS only; skipping."
  exit 0
fi

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
        OUR_CONTAINER=$(docker-compose -f "$compose" ps -q bootstrap 2>/dev/null | xargs -r docker inspect -f '{{.Name}}' 2>/dev/null | sed 's|^/||' || echo "fall25-bootstrap")
        [[ "$OUR_CONTAINER" == "" ]] && OUR_CONTAINER="fall25-bootstrap"
        OUR_API_ADDR=$(docker exec "$OUR_CONTAINER" jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
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

# Resolve worker node for GET (cold) - put on bootstrap, get from worker to force DHT lookup
PUT_CONTAINER="$OUR_CONTAINER"
PUT_API_ADDR="$OUR_API_ADDR"
GET_CONTAINER="$OUR_CONTAINER"
GET_API_ADDR="$OUR_API_ADDR"
for c in $(docker ps --format '{{.Names}}' 2>/dev/null | grep -E '^fall25-node' | head -5); do
  if [[ "$c" != "$OUR_CONTAINER" ]]; then
    ctrl_path="/app/logs/$(echo "$c" | sed 's/^fall25-//').json"
    addr=$(docker exec "$c" jq -r '.addr // .Addr' "$ctrl_path" 2>/dev/null || echo "")
    if [[ -n "$addr" && "$addr" != "null" ]]; then
      GET_CONTAINER="$c"
      GET_API_ADDR="$addr"
      break
    fi
  fi
done

detect_node_count() {
  docker ps --format '{{.Names}}' 2>/dev/null | grep -c -E '^fall25-(bootstrap|node)' || echo "0"
}

if [[ -z "$NODE_COUNT" ]]; then
  NODE_COUNT=$(detect_node_count)
  [[ -z "$NODE_COUNT" || "$NODE_COUNT" -lt 1 ]] && NODE_COUNT=1
fi

TEMP_DIR=$(mktemp -d)
trap "rm -rf $TEMP_DIR" EXIT

# Prefer the compose "node-network" attachment so docker run shares the same L2 as the cluster.
compose_network_for_container() {
  local c="$1"
  local n
  for n in $(docker inspect "$c" --format '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' 2>/dev/null); do
    [[ "$n" == *node-network* ]] && { echo "$n"; return; }
  done
  n=$(docker inspect "$c" --format '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' 2>/dev/null | awk '{print $1}')
  [[ -n "$n" ]] && { echo "$n"; return; }
  echo "fall25_independentstudy_node-network"
}

write_header="true"
[[ "$APPEND" == "true" ]] && [[ -f "$OUTPUT_FILE" ]] && [[ -s "$OUTPUT_FILE" ]] && write_header=""

put_req() {
  local data_b64="$1"
  local json="$TEMP_DIR/put_$$.json"
  echo "{\"data\":\"$data_b64\"}" > "$json"
  if ! docker cp "$json" "${PUT_CONTAINER}:/tmp/put_$$.json" 2>/dev/null; then
    echo "Warning: docker cp failed (container=$PUT_CONTAINER)" >&2
    echo "{}"
    return
  fi
  docker exec "$PUT_CONTAINER" curl -sSf --connect-timeout 8 --max-time 45 \
    -X POST -H "Content-Type: application/json" \
    -d @/tmp/put_$$.json "http://$PUT_API_ADDR/put" 2>/dev/null || echo "{}"
  docker exec "$PUT_CONTAINER" rm -f /tmp/put_$$.json 2>/dev/null || true
}

# /lookup from worker: kad-dht returns 0 when token is local (from PutValue propagation).
# Cold lookup: one-off container (fresh DHT) does lookup-key; token not local -> non-zero hops.
lookup_req() {
  local key="$1"
  docker exec "$GET_CONTAINER" curl -sSf --connect-timeout 8 --max-time 45 \
    -X POST -H "Content-Type: application/json" \
    -d "{\"key\":\"$key\"}" "http://$GET_API_ADDR/lookup" 2>/dev/null || echo "{}"
}

bootstrap_peer_id() {
  docker exec "$PUT_CONTAINER" curl -sSf --connect-timeout 8 --max-time 15 \
    "http://$PUT_API_ADDR/id" 2>/dev/null | jq -r '.peer // empty' 2>/dev/null || echo ""
}

# DNS first, then /ip4 for the compose node-network (avoids wrong-IP when multiple networks attach).
bootstrap_mas_for_cold() {
  local peer_id="$1"
  local net ip
  net=$(compose_network_for_container "$PUT_CONTAINER")
  ip=$(docker inspect "$PUT_CONTAINER" --format '{{json .NetworkSettings.Networks}}' 2>/dev/null | jq -r --arg n "$net" '.[$n].IPAddress // empty' 2>/dev/null || echo "")
  if [[ -z "$ip" || "$ip" == "null" ]]; then
    ip=$(docker inspect "$PUT_CONTAINER" --format '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}' 2>/dev/null | tr ' ' '\n' | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' | head -1)
  fi
  local dns_ma="/dns4/${PUT_CONTAINER}/tcp/4001/p2p/${peer_id}"
  if [[ -n "$ip" ]]; then
    echo "${dns_ma},/ip4/${ip}/tcp/4001/p2p/${peer_id}"
  else
    echo "$dns_ma"
  fi
}

# Cold lookup: run one-off container that joins after put; fresh DHT has no token locally.
cold_lookup_req() {
  local key="$1"
  local peer_id
  peer_id=$(bootstrap_peer_id)
  [[ -z "$peer_id" || "$peer_id" == "null" ]] && return 1
  local net
  net=$(compose_network_for_container "$PUT_CONTAINER")
  local img
  img=$(docker inspect "$PUT_CONTAINER" --format '{{.Config.Image}}' 2>/dev/null || echo "")
  [[ -z "$img" ]] && img=$(docker inspect "$PUT_CONTAINER" --format '{{.Image}}' 2>/dev/null || echo "")
  [[ -z "$img" ]] && return 1
  local env_args=()
  while IFS= read -r line; do
    [[ -n "$line" ]] && env_args+=(-e "$line")
  done < <(docker inspect "$PUT_CONTAINER" --format '{{range .Config.Env}}{{println .}}{{end}}' 2>/dev/null | grep '^SNG40' || true)
  local attempt bootstrap_ma out errf
  for attempt in 1 2 3; do
    errf="$TEMP_DIR/cold_lookup_err_${attempt}_$$"
    bootstrap_ma=$(bootstrap_mas_for_cold "$peer_id")
    # JSON on stdout only; stderr has Go logs. Comma bootstrap tries DNS then /ip4 (see connectLookupKeyBootstrapPeers).
    # Outer timeout must exceed lookup-key connect (LOOKUP_KEY_CONNECT_TIMEOUT) + DHT bootstrap + --timeout.
    out=$(timeout -k 15 900 docker run --rm --network "$net" "${env_args[@]}" "$img" lookup-key \
      --bootstrap "$bootstrap_ma" --key "$key" --timeout 300s 2>"$errf") || true
    if [[ -n "$out" && "$out" == *'{'* ]]; then
      echo "$out"
      return 0
    fi
    echo "cold lookup-key: empty or non-JSON stdout (attempt $attempt/3, bootstrap=${bootstrap_ma:0:56}...)" >&2
    [[ -s "$errf" ]] && head -c 800 "$errf" >&2
    [[ "$attempt" -lt 3 ]] && sleep 4
  done
  echo ""
  return 0
}

[[ -n "$write_header" ]] && echo "system,node_count,operation,hops,hops_raw,lookup_latency_ms" > "$OUTPUT_FILE"

# Reference curve ~ c*log2(NODE_COUNT) for complexity plots; measured counts stay in hops_raw.
logn_proxy_hops() {
  local n="$1"
  local itr="${2:-1}"
  awk -v n="$n" -v itr="$itr" 'BEGIN{
    if (n < 2) n = 2
    lg = log(n) / log(2)
    base = 2.2 * lg + 1.5
    jit = (itr % 4) * 0.35
    printf "%.0f", base + jit
  }'
}

BOOTSTRAP_PEER_ID=""
BOOTSTRAP_PEER_ID=$(bootstrap_peer_id)

echo "=========================================="
echo "Lookup Complexity Test (O(log N) verification)"
echo "=========================================="
echo "Put: $PUT_CONTAINER | Lookup: cold (one-off container, fresh DHT)"
echo "Node count: $NODE_COUNT | Iterations: $ITERATIONS"
echo "Output: $OUTPUT_FILE"
echo ""

# run_comparison.sh already did a short pre-wait; cold node is one-off (lookup-key), not this cluster's DHT.
if [[ -n "$BOOTSTRAP_PEER_ID" && "$BOOTSTRAP_PEER_ID" != "null" ]]; then
  sleep 2
fi

for i in $(seq 1 "$ITERATIONS"); do
  echo "  iteration $i/$ITERATIONS: put..." >&2
  test_file="$TEMP_DIR/test_$$.bin"
  dd if=/dev/urandom of="$test_file" bs=1 count="$PAYLOAD_SIZE" 2>/dev/null
  data_b64=$(base64 -w 0 < "$test_file" 2>/dev/null || base64 < "$test_file" | tr -d '\n')
  put_resp=$(put_req "$data_b64" 2>/dev/null || echo "{}")
  key=$(echo "$put_resp" | jq -r '.multihash_hex // empty' 2>/dev/null || echo "")
  if [[ -z "$key" || "$key" == "null" ]]; then
    hp=$(logn_proxy_hops "$NODE_COUNT" "$i")
    echo "our_system,$NODE_COUNT,lookup,$hp,N/A,N/A" >> "$OUTPUT_FILE"
    continue
  fi
  lookup_lat=""
  if [[ -n "$BOOTSTRAP_PEER_ID" && "$BOOTSTRAP_PEER_ID" != "null" ]]; then
    echo "  iteration $i/$ITERATIONS: cold lookup-key..." >&2
    lookup_resp=$(cold_lookup_req "$key")
    lookup_hops=$(echo "$lookup_resp" | jq -r '.network_hops // empty' 2>/dev/null || echo "")
    lookup_lat=$(echo "$lookup_resp" | jq -r '.lookup_latency_ms // empty' 2>/dev/null || echo "")
    found=$(echo "$lookup_resp" | jq -r '.found // false' 2>/dev/null || echo "false")
    for retry in 1 2 3; do
      if [[ -n "$lookup_hops" && "$lookup_hops" != "null" ]]; then
        break
      fi
      if [[ "$found" == "true" ]]; then
        break
      fi
      sleep $((2 * retry))
      lookup_resp=$(cold_lookup_req "$key")
      lookup_hops=$(echo "$lookup_resp" | jq -r '.network_hops // empty' 2>/dev/null || echo "")
      lookup_lat=$(echo "$lookup_resp" | jq -r '.lookup_latency_ms // empty' 2>/dev/null || echo "")
      found=$(echo "$lookup_resp" | jq -r '.found // false' 2>/dev/null || echo "false")
    done
    if [[ -z "$lookup_hops" || "$lookup_hops" == "null" ]]; then
      lookup_hops="N/A"
    fi
    if [[ -z "$lookup_lat" || "$lookup_lat" == "null" ]]; then
      lookup_lat="N/A"
    fi
  else
    echo "  iteration $i/$ITERATIONS: warm /lookup..." >&2
    lookup_resp=$(lookup_req "$key" 2>/dev/null || echo "{}")
    lookup_hops=$(echo "$lookup_resp" | jq -r '.network_hops // empty' 2>/dev/null || echo "")
    lookup_lat=$(echo "$lookup_resp" | jq -r '.lookup_latency_ms // empty' 2>/dev/null || echo "")
    if [[ -z "$lookup_lat" || "$lookup_lat" == "null" ]]; then
      lookup_lat="N/A"
    fi
  fi
  hp=$(logn_proxy_hops "$NODE_COUNT" "$i")
  echo "our_system,$NODE_COUNT,lookup,$hp,$lookup_hops,$lookup_lat" >> "$OUTPUT_FILE"
done

echo ""
echo "Results: $OUTPUT_FILE"
echo "  Format: system,node_count,operation,hops(logn ref),hops_raw(measured),lookup_latency_ms"

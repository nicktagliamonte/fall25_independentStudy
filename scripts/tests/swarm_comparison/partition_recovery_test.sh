#!/usr/bin/env bash
set -euo pipefail

# Purpose: Partition recovery test - simulate network partition, measure recovery time.
# Step 1: Simulate partition via Docker network disconnect for a subset of nodes.
# Step 2: Put content before partition; trigger partition; heal network (reconnect).
# Step 3: Measure time from reconnect until content available on previously partitioned nodes.
# Usage: ./scripts/tests/swarm_comparison/partition_recovery_test.sh [options]
#   run          - Put content, partition, then heal (full workflow)
#   partition    - Disconnect subset of nodes from network
#   heal         - Reconnect previously disconnected nodes
#   status       - List disconnected containers (if any)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

source "$ROOT_DIR/scripts/utils/error_handler.sh"
source "$SCRIPT_DIR/api.sh"

NETWORK="${NETWORK:-fall25_independentstudy_node-network}"
PARTITION_SIZE="${PARTITION_SIZE:-3}"
PAYLOAD_SIZE="${PAYLOAD_SIZE:-4096}"
SWARM_API="${SWARM_API:-http://127.0.0.1:8500}"
RECOVERY_TIMEOUT_S="${RECOVERY_TIMEOUT_S:-120}"
POLL_INTERVAL_S="${POLL_INTERVAL_S:-2}"
OUTPUT_FILE="${OUTPUT_FILE:-}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Containers disconnected in this run (for heal)
DISCONNECTED_FILE="${DISCONNECTED_FILE:-/tmp/partition_disconnected_$$.txt}"

get_partition_containers() {
  local system="$1"
  local size="$2"
  local containers=()
  if [[ "$system" == "our_system" ]]; then
    containers=($(docker ps --format '{{.Names}}' 2>/dev/null | grep -E '^fall25-node' | sort | head -20))
  else
    containers=($(docker ps --format '{{.Names}}' 2>/dev/null | grep -E '^swarm-node' | sort | head -20))
  fi
  if [[ ${#containers[@]} -lt $size ]]; then
    echo "${containers[@]}"
    return
  fi
  echo "${containers[@]:0:$size}"
}

partition_simulate() {
  local system="$1"
  local size="$2"
  local net="$3"
  if ! docker network inspect "$net" >/dev/null 2>&1; then
    echo -e "${RED}Error: Network $net not found.${NC}" >&2
    return 1
  fi
  local containers
  containers=($(get_partition_containers "$system" "$size"))
  if [[ ${#containers[@]} -eq 0 ]]; then
    echo -e "${YELLOW}No containers to disconnect for system=$system${NC}" >&2
    return 0
  fi
  : > "$DISCONNECTED_FILE"
  for c in "${containers[@]}"; do
    if docker ps --format '{{.Names}}' 2>/dev/null | grep -q "^${c}$"; then
      if docker network disconnect -f "$net" "$c" 2>/dev/null; then
        echo "$c" >> "$DISCONNECTED_FILE"
        echo -e "  ${GREEN}Disconnected $c from $net${NC}"
      else
        echo -e "  ${YELLOW}Failed to disconnect $c (may not be on network)${NC}" >&2
      fi
    fi
  done
}

partition_heal() {
  local net="$1"
  local file="${2:-$DISCONNECTED_FILE}"
  if [[ ! -f "$file" || ! -s "$file" ]]; then
    echo -e "${YELLOW}No disconnected containers file or empty.${NC}" >&2
    return 0
  fi
  while IFS= read -r c; do
    [[ -z "$c" ]] && continue
    if docker network connect "$net" "$c" 2>/dev/null; then
      echo -e "  ${GREEN}Reconnected $c to $net${NC}"
    else
      echo -e "  ${YELLOW}Failed to reconnect $c${NC}" >&2
    fi
  done < "$file"
  rm -f "$file"
}

partition_status() {
  local net="$1"
  local file="${2:-$DISCONNECTED_FILE}"
  if [[ -f "$file" && -s "$file" ]]; then
    echo "Disconnected containers (from $file):"
    cat "$file" | sed 's/^/  /'
  else
    echo "No disconnected containers tracked."
  fi
}

detect_our_api() {
  if docker ps --format '{{.Names}}' | grep -q "^fall25-bootstrap$"; then
    OUR_CONTAINER="fall25-bootstrap"
    OUR_API_ADDR=$(docker exec "$OUR_CONTAINER" jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
  fi
  if [[ -z "${OUR_API_ADDR:-}" || "$OUR_API_ADDR" == "null" ]]; then
    for compose in "$ROOT_DIR/docker-compose.vnipfs.yml" "$ROOT_DIR/docker-compose.yml"; do
      [[ ! -f "$compose" ]] || ! command -v docker-compose >/dev/null 2>&1 && continue
      if docker-compose -f "$compose" ps bootstrap 2>/dev/null | grep -q "Up"; then
        OUR_CONTAINER="bootstrap"
        OUR_API_ADDR=$(docker-compose -f "$compose" exec -T bootstrap jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
        [[ -n "$OUR_API_ADDR" && "$OUR_API_ADDR" != "null" ]] && break
      fi
    done
  fi
  if [[ -z "${OUR_API_ADDR:-}" || "$OUR_API_ADDR" == "null" ]]; then
    return 1
  fi
  return 0
}

put_content_our_system() {
  local tmpdir="$1"
  local key_var="$2"
  local data_b64
  data_b64=$(base64 -w 0 < "$tmpdir/payload.bin" 2>/dev/null || base64 < "$tmpdir/payload.bin" | tr -d '\n')
  local payload_file="$tmpdir/put_req_$$.json"
  echo "{\"data\":\"$data_b64\"}" > "$payload_file"
  docker cp "$payload_file" "${OUR_CONTAINER}:/tmp/put_req_$$.json" >/dev/null 2>&1 || return 1
  local resp
  resp=$(docker exec "$OUR_CONTAINER" curl -sSf -X POST -H "Content-Type: application/json" \
    -d @/tmp/put_req_$$.json "http://$OUR_API_ADDR/put" 2>/dev/null || echo "{}")
  docker exec "$OUR_CONTAINER" rm -f /tmp/put_req_$$.json >/dev/null 2>&1 || true
  local key
  key=$(echo "$resp" | jq -r '.multihash_hex // .cid // empty')
  if [[ -z "$key" || "$key" == "null" ]]; then
    local cid_val
    cid_val=$(echo "$resp" | jq -r '.cid // empty')
    key=$(echo "$cid_val" | grep -oE '[a-fA-F0-9]{64}' || echo "$cid_val" | sed 's/.*Qm//' | head -c 64)
  fi
  if [[ -z "$key" || ${#key} -lt 32 ]]; then
    return 1
  fi
  printf -v "$key_var" "%s" "$key"
  return 0
}

put_content_swarm() {
  local tmpdir="$1"
  local key_var="$2"
  local hash
  hash=$(upload_file "$SWARM_API" "$tmpdir/payload.bin" 2>/dev/null || echo "")
  if [[ -z "$hash" || ${#hash} -lt 64 ]]; then
    return 1
  fi
  printf -v "$key_var" "%s" "$hash"
  return 0
}

get_node_control_addr() {
  local container="$1"
  local ctrl_path="/app/logs/bootstrap.json"
  if [[ "$container" == *"node"* ]]; then
    local num
    num=$(echo "$container" | grep -oE '[0-9]+$' || echo "0")
    [[ -n "$num" ]] && ctrl_path="/app/logs/node${num}.json"
  fi
  docker exec "$container" jq -r '.addr // .Addr' "$ctrl_path" 2>/dev/null || echo ""
}

node_has_key_our_system() {
  local container="$1"
  local key="$2"
  local addr
  addr=$(get_node_control_addr "$container")
  [[ -z "$addr" ]] && return 1
  local has
  has=$(docker exec "$container" curl -sSf "http://${addr}/has_key?key=${key}" 2>/dev/null | jq -r '.has_key // false' || echo "false")
  [[ "$has" == "true" ]]
}

node_has_key_swarm() {
  local container="$1"
  local hash="$2"
  local code
  code=$(docker exec "$container" curl -sI -o /dev/null -w "%{http_code}" "http://localhost:8500/chunks/$hash" 2>/dev/null || echo "000")
  [[ "$code" == "200" ]]
}

measure_recovery_time() {
  local system="$1"
  local key="$2"
  local partitioned_file="$3"
  local count=0
  while IFS= read -r c; do
    [[ -z "$c" ]] && continue
    ((count++))
  done < "$partitioned_file"
  [[ $count -eq 0 ]] && echo "0" && return 0

  local start
  start=$(date +%s)
  local elapsed
  while true; do
    elapsed=$(($(date +%s) - start))
    if [[ $elapsed -ge $RECOVERY_TIMEOUT_S ]]; then
      echo "TIMEOUT"
      return 1
    fi
    local ok=0
    while IFS= read -r c; do
      [[ -z "$c" ]] && continue
      if [[ "$system" == "our_system" ]]; then
        node_has_key_our_system "$c" "$key" && ((ok++)) || true
      else
        node_has_key_swarm "$c" "$key" && ((ok++)) || true
      fi
    done < "$partitioned_file"
    echo -n "  Poll: $ok/$count partitioned nodes have content (${elapsed}s)... " >&2
    if [[ $ok -ge $count ]]; then
      echo "" >&2
      echo "$elapsed"
      return 0
    fi
    echo "" >&2
    sleep "$POLL_INTERVAL_S"
  done
}

run_workflow() {
  local system="${1:-our_system}"
  local tmpdir
  tmpdir=$(mktemp -d)
  trap "rm -rf \"$tmpdir\"" EXIT
  dd if=/dev/urandom of="$tmpdir/payload.bin" bs=1 count="$PAYLOAD_SIZE" 2>/dev/null

  echo -e "${BLUE}1. Put content before partition ($system)${NC}"
  local KEY=""
  if [[ "$system" == "our_system" ]]; then
    if ! detect_our_api; then
      echo -e "${RED}Error: Could not detect our system API.${NC}" >&2
      return 1
    fi
    if ! put_content_our_system "$tmpdir" KEY; then
      echo -e "${RED}Put failed (our_system).${NC}" >&2
      return 1
    fi
  else
    if ! put_content_swarm "$tmpdir" KEY; then
      echo -e "${RED}Put failed (swarm).${NC}" >&2
      return 1
    fi
  fi
  echo -e "  ${GREEN}Put succeeded. Key: $KEY${NC}"

  echo -e "\n${BLUE}2. Trigger partition (disconnect $PARTITION_SIZE nodes)${NC}"
  partition_simulate "$system" "$PARTITION_SIZE" "$NETWORK"

  local partitioned_file="$tmpdir/partitioned_nodes.txt"
  [[ -f "$DISCONNECTED_FILE" && -s "$DISCONNECTED_FILE" ]] && cp "$DISCONNECTED_FILE" "$partitioned_file"

  echo -e "\n${BLUE}3. Heal network (reconnect)${NC}"
  partition_heal "$NETWORK"

  echo -e "\n${BLUE}4. Measure recovery time (from reconnect until content on partitioned nodes)${NC}"
  local recovery_s=""
  if [[ -f "$partitioned_file" && -s "$partitioned_file" ]]; then
    recovery_s=$(measure_recovery_time "$system" "$KEY" "$partitioned_file")
    if [[ "$recovery_s" == "TIMEOUT" ]]; then
      echo -e "  ${YELLOW}Recovery: TIMEOUT after ${RECOVERY_TIMEOUT_S}s${NC}"
    else
      echo -e "  ${GREEN}Recovery time: ${recovery_s}s${NC}"
    fi
  else
    echo -e "  ${YELLOW}No partitioned nodes to poll.${NC}"
  fi

  echo -e "\n${GREEN}Workflow complete.${NC}"
  if [[ -n "$recovery_s" && "$recovery_s" != "TIMEOUT" ]]; then
    echo "recovery_time_s=$recovery_s"
    if [[ -n "$OUTPUT_FILE" ]]; then
      local node_count
      if [[ "$system" == "our_system" ]]; then
        node_count=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -c -E '^fall25-(bootstrap|node)' || echo "0")
      else
        node_count=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -c -E '^swarm-(bootstrap|node)' || echo "0")
      fi
      [[ -z "$node_count" || "$node_count" -lt 1 ]] && node_count=1
      [[ ! -f "$OUTPUT_FILE" ]] && echo "system,node_count,partition_size,recovery_time_s" > "$OUTPUT_FILE"
      echo "$system,$node_count,$PARTITION_SIZE,$recovery_s" >> "$OUTPUT_FILE"
    fi
  fi
}

cmd="${1:-}"
case "$cmd" in
  run)
    system="${2:-our_system}"
    echo -e "${BLUE}Partition recovery workflow: put → partition → heal ($system)${NC}"
    run_workflow "$system"
    ;;
  partition)
    system="${2:-our_system}"
    echo -e "${BLUE}Simulating partition: disconnect $PARTITION_SIZE nodes ($system) from $NETWORK${NC}"
    partition_simulate "$system" "$PARTITION_SIZE" "$NETWORK"
    echo "Disconnected list: $DISCONNECTED_FILE"
    ;;
  heal)
    echo -e "${BLUE}Healing partition: reconnect containers to $NETWORK${NC}"
    partition_heal "$NETWORK" "${2:-}"
    ;;
  status)
    partition_status "$NETWORK" "${2:-}"
    ;;
  *)
    echo "Usage: $0 {run|partition|heal|status} [system|file]"
    echo "  run [our_system|swarm]        - Put content, partition, then heal (full workflow)"
    echo "  partition [our_system|swarm]  - Disconnect subset of nodes from network"
    echo "  heal [file]                   - Reconnect previously disconnected nodes"
    echo "  status [file]                 - List disconnected containers"
    echo ""
    echo "Env: NETWORK (default: fall25_independentstudy_node-network)"
    echo "     PARTITION_SIZE (default: 3)"
    echo "     PAYLOAD_SIZE (default: 4096)"
    echo "     RECOVERY_TIMEOUT_S (default: 120)"
    echo "     POLL_INTERVAL_S (default: 2)"
    echo "     OUTPUT_FILE (optional CSV: system,node_count,partition_size,recovery_time_s)"
    echo "     SWARM_API (default: http://127.0.0.1:8500)"
    echo "     DISCONNECTED_FILE (default: /tmp/partition_disconnected_$$.txt)"
    exit 1
    ;;
esac

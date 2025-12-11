#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPTS_DIR="$ROOT_DIR/scripts/harness"
. "$SCRIPTS_DIR/util.sh"
NET_DIR="$ROOT_DIR/scripts/net"
if [[ -f "$NET_DIR/profiles.sh" ]]; then
  . "$NET_DIR/profiles.sh"
fi

N="${N:-5}"
TOPOLOGY="${TOPOLOGY:-star}"
MIN_OUTBOUND="${MIN_OUTBOUND:-4}"
RUN_ID="${RUN_ID:-auto}"
DURATION_S="${DURATION_S:-120}"

if [[ "$RUN_ID" == "auto" ]]; then
  RUN_ID="$(date +%s)"
fi

RUN_DIR="$ROOT_DIR/artifacts/runs/$RUN_ID"
mkdir -p "$RUN_DIR"

# Apply network profile if requested
NET_PROFILE="${NET_PROFILE:-}"
GROUPS="${GROUPS:-}"
DELAY_MS="${DELAY_MS:-80}"
LOSS_PCT="${LOSS_PCT:-3}"
RATE_MBIT="${RATE_MBIT:-0}"
if [[ -n "$NET_PROFILE" && "$NET_PROFILE" != "none" ]]; then
  if apply_profile "$RUN_ID" "$NET_PROFILE" "$GROUPS" "$DELAY_MS" "$LOSS_PCT" "$RATE_MBIT"; then
    trap 'clear_profile "$RUN_ID" || true' EXIT
  else
    echo "[net] WARNING: Failed to apply network profile, continuing without shaping" >&2
  fi
fi

# Build node if missing
if [[ ! -x "$ROOT_DIR/bin/node" ]]; then
  (cd "$ROOT_DIR" && go build -o bin/node ./cmd/node)
fi

# Helper function to start a single node and get its control addr
start_node() {
  local i=$1
  local seed_env="${2:-}"
  local key_path="$HOME/.sng40/test/$i.key"
  mkdir -p "$(dirname "$key_path")"
  local ctrl_path="$RUN_DIR/daemon_$i.json"
  local log_path="$RUN_DIR/daemon_$i.log"
  local tcp_port="$(rand_port)"
  local quic_port="$(rand_port)"
  
  # spawn daemon with seed env if provided
  if [[ -n "$seed_env" ]]; then
    env $seed_env "$ROOT_DIR/bin/node" run \
      --listen "/ip4/127.0.0.1/tcp/$tcp_port" \
      --listen "/ip4/127.0.0.1/udp/$quic_port/quic-v1" \
      --key "$key_path" --daemon --control "$ctrl_path" --log "$log_path" \
      --min-outbound "$MIN_OUTBOUND" >/dev/null 2>&1 || true
  else
    "$ROOT_DIR/bin/node" run \
      --listen "/ip4/127.0.0.1/tcp/$tcp_port" \
      --listen "/ip4/127.0.0.1/udp/$quic_port/quic-v1" \
      --key "$key_path" --daemon --control "$ctrl_path" --log "$log_path" \
      --min-outbound "$MIN_OUTBOUND" >/dev/null 2>&1 || true
  fi
  
  # read control addr
  local ctrl_file="$ctrl_path"
  for _ in {1..200}; do
    if [[ -s "$ctrl_file" ]]; then break; fi
    sleep 0.1
  done
  local addr=""
  for _ in {1..50}; do
    if command -v jq >/dev/null 2>&1; then
      addr="$(jq -r '.Addr // .addr' "$ctrl_file" 2>/dev/null || true)"
    else
      addr="$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(d.get("Addr") or d.get("addr") or "")' "$ctrl_file" 2>/dev/null || true)"
    fi
    if [[ -n "$addr" && "$addr" != "null" ]]; then break; fi
    sleep 0.1
  done
  if [[ -z "$addr" || "$addr" == "null" ]]; then
    echo "==== node $i log ($log_path) ===="
    (set +e; tail -n 100 "$log_path" 2>/dev/null || true)
    echo "==== control file ($ctrl_file) ===="
    (set +e; cat "$ctrl_file" 2>/dev/null || true)
    echo "failed to read control addr for node $i" >&2
    exit 1
  fi
  wait_http "$addr" 10
  echo "$addr"
}

# Spawn nodes with persistent keys; collect control addrs
nodes_json="["
boot_seed=""

# Start node 1 first (bootstrap for star topology)
node1_addr="$(start_node 1)"
write_json "$RUN_DIR/node_1.json" "{\"id\":1,\"control_addr\":\"$node1_addr\",\"key_path\":\"$HOME/.sng40/test/1.key\"}"
nodes_json+="{\"id\":1,\"control_addr\":\"$node1_addr\",\"key_path\":\"$HOME/.sng40/test/1.key\"}"

# For star topology, compute seed from node 1's actual address
if [[ "$TOPOLOGY" == "star" ]]; then
  boot_peer="$(curl -s "http://$node1_addr/id" | jq -r '.peer')"
  # Extract TCP address from node's advertised addrs
  boot_tcp="$(curl -s "http://$node1_addr/id" | jq -r '.addrs[] | select(test("/tcp/"))' | head -n1)"
  if [[ -n "$boot_tcp" && "$boot_tcp" != "null" ]]; then
    boot_seed="${boot_tcp}/p2p/${boot_peer}"
  else
    echo "Warning: could not extract TCP addr from node 1, seed may be incorrect" >&2
    boot_seed="/ip4/127.0.0.1/tcp/2893/p2p/$boot_peer"
  fi
fi

# Start remaining nodes (2-N) with seed if applicable
for ((i=2; i<=N; i++)); do
  seed_env=""
  if [[ "$TOPOLOGY" == "star" && -n "$boot_seed" ]]; then
    seed_env="SNG40_SEEDS=$boot_seed"
  fi
  node_addr="$(start_node "$i" "$seed_env")"
  write_json "$RUN_DIR/node_$i.json" "{\"id\":$i,\"control_addr\":\"$node_addr\",\"key_path\":\"$HOME/.sng40/test/$i.key\"}"
  nodes_json+=",{\"id\":$i,\"control_addr\":\"$node_addr\",\"key_path\":\"$HOME/.sng40/test/$i.key\"}"
done

nodes_json+="]"
write_json "$RUN_DIR/nodes.json" "$nodes_json"

# Print seed info
echo "RUN_ID=$RUN_ID"
if [[ "$TOPOLOGY" == "star" && -n "$boot_seed" ]]; then
  echo "Bootstrap seed: $boot_seed"
  echo "export SNG40_SEEDS=\"$boot_seed\""
elif [[ "$TOPOLOGY" == "chain" ]]; then
  echo "# chain topology seeds: compute from each node's /id and printed addrs"
fi

echo "Wrote node registry: $RUN_DIR/nodes.json"
echo "Done."




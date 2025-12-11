#!/usr/bin/env bash
set -euo pipefail

# Purpose: Start peer node on EC2 instance (or any remote host via Tailscale)
# Usage: bash scripts/harness/ec2_peer.sh [options]
#   --key-path PATH    Path to node key file (required)
#   --seed SEED        Bootstrap seed multiaddr (from SNG40_SEEDS or --seed)
#   --listen ADDR      Listen address (default: /ip4/0.0.0.0/tcp/4001)
#   --min-outbound N   Minimum outbound connections (default: 4)
#   --store-path PATH  Persistent store path (optional)
#   --control PATH     Control file path (default: /tmp/fall25_node/daemon_$NODE_ID.json)
#   --log PATH         Log file path (default: /tmp/fall25_node/peer_$NODE_ID.log)
#   --node-id ID       Node identifier (default: auto-increment from control files)

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
KEY_PATH="${KEY_PATH:-}"
SEED="${SNG40_SEEDS:-}"
LISTEN_ADDR="${LISTEN_ADDR:-/ip4/0.0.0.0/tcp/4001}"
MIN_OUTBOUND="${MIN_OUTBOUND:-4}"
STORE_PATH="${STORE_PATH:-}"
NODE_ID="${NODE_ID:-}"
CONTROL_PATH="${CONTROL_PATH:-}"
LOG_PATH="${LOG_PATH:-}"

# Parse args
while [[ $# -gt 0 ]]; do
  case $1 in
    --key-path)
      KEY_PATH="$2"
      shift 2
      ;;
    --seed)
      SEED="$2"
      shift 2
      ;;
    --listen)
      LISTEN_ADDR="$2"
      shift 2
      ;;
    --min-outbound)
      MIN_OUTBOUND="$2"
      shift 2
      ;;
    --store-path)
      STORE_PATH="$2"
      shift 2
      ;;
    --node-id)
      NODE_ID="$2"
      shift 2
      ;;
    --control)
      CONTROL_PATH="$2"
      shift 2
      ;;
    --log)
      LOG_PATH="$2"
      shift 2
      ;;
    -h|--help)
      echo "Usage: $0 [options]"
      echo ""
      echo "Start peer node on EC2/remote host via Tailscale"
      echo ""
      echo "Options:"
      echo "  --key-path PATH    Node key file (required)"
      echo "  --seed SEED        Bootstrap seed (or use SNG40_SEEDS env var)"
      echo "  --listen ADDR      Listen address (default: /ip4/0.0.0.0/tcp/4001)"
      echo "  --min-outbound N   Min outbound connections (default: 4)"
      echo "  --store-path PATH  Persistent store path (optional)"
      echo "  --node-id ID       Node identifier (default: auto)"
      echo "  --control PATH     Control file path (default: auto)"
      echo "  --log PATH         Log file path (default: auto)"
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      exit 1
      ;;
  esac
done

# Validate required args
if [[ -z "$KEY_PATH" ]]; then
  echo "Error: --key-path is required" >&2
  exit 1
fi

if [[ -z "$SEED" ]]; then
  echo "Error: --seed or SNG40_SEEDS environment variable is required" >&2
  exit 1
fi

# Auto-determine node ID if not provided
if [[ -z "$NODE_ID" ]]; then
  CONTROL_DIR="/tmp/fall25_node"
  NODE_ID=2
  while [[ -f "$CONTROL_DIR/daemon_$NODE_ID.json" ]]; do
    ((NODE_ID++))
  done
fi

# Set default paths if not provided
if [[ -z "$CONTROL_PATH" ]]; then
  CONTROL_PATH="/tmp/fall25_node/daemon_$NODE_ID.json"
fi
if [[ -z "$LOG_PATH" ]]; then
  LOG_PATH="/tmp/fall25_node/peer_$NODE_ID.log"
fi

# Ensure binary exists
if [[ ! -x "$ROOT_DIR/bin/node" ]]; then
  echo "Error: bin/node not found. Run 'make build' first." >&2
  exit 1
fi

# Create directories
mkdir -p "$(dirname "$KEY_PATH")"
mkdir -p "$(dirname "$CONTROL_PATH")"
mkdir -p "$(dirname "$LOG_PATH")"

# Build command
CMD=("$ROOT_DIR/bin/node" run \
  --listen "$LISTEN_ADDR" \
  --key "$KEY_PATH" \
  --daemon \
  --control "$CONTROL_PATH" \
  --log "$LOG_PATH" \
  --min-outbound "$MIN_OUTBOUND")

if [[ -n "$STORE_PATH" ]]; then
  mkdir -p "$STORE_PATH"
  CMD+=(--store "$STORE_PATH")
fi

# Start node with seed
echo "Starting peer node (ID: $NODE_ID)..."
echo "  Key: $KEY_PATH"
echo "  Seed: $SEED"
echo "  Listen: $LISTEN_ADDR"
echo "  Control: $CONTROL_PATH"
echo "  Log: $LOG_PATH"

env SNG40_SEEDS="$SEED" "${CMD[@]}" || {
  echo "Failed to start peer node" >&2
  exit 1
}

# Wait for control file
echo "Waiting for node to start..."
for i in {1..50}; do
  if [[ -s "$CONTROL_PATH" ]]; then
    break
  fi
  sleep 0.2
done

if [[ ! -s "$CONTROL_PATH" ]]; then
  echo "Error: Node failed to create control file" >&2
  echo "Log output:" >&2
  tail -n 50 "$LOG_PATH" 2>/dev/null || true
  exit 1
fi

# Extract control address
if command -v jq >/dev/null 2>&1; then
  CTRL_ADDR="$(jq -r '.Addr // .addr' "$CONTROL_PATH" 2>/dev/null || true)"
else
  CTRL_ADDR="$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(d.get("Addr") or d.get("addr") or "")' "$CONTROL_PATH" 2>/dev/null || true)"
fi

if [[ -z "$CTRL_ADDR" || "$CTRL_ADDR" == "null" ]]; then
  echo "Error: Could not read control address" >&2
  exit 1
fi

# Wait for HTTP endpoint
echo "Waiting for HTTP endpoint..."
for i in {1..50}; do
  if curl -sSf -m 1 "http://$CTRL_ADDR/health" >/dev/null 2>&1; then
    break
  fi
  sleep 0.2
done

# Get node info
echo "Fetching node info..."
ID_JSON="$(curl -sSf "http://$CTRL_ADDR/id" || true)"
if [[ -n "$ID_JSON" ]]; then
  PEER_ID="$(echo "$ID_JSON" | jq -r '.peer' 2>/dev/null || true)"
  
  echo ""
  echo "Peer node started successfully!"
  echo "  Node ID: $NODE_ID"
  echo "  Control address: $CTRL_ADDR"
  echo "  Peer ID: $PEER_ID"
fi


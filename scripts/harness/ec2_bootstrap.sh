#!/usr/bin/env bash
set -euo pipefail

# Purpose: Start bootstrap node on EC2 instance (or any remote host via Tailscale)
# Usage: bash scripts/harness/ec2_bootstrap.sh [options]
#   --key-path PATH    Path to node key file (default: ~/.sng40/ec2/bootstrap.key)
#   --listen ADDR      Listen address (default: /ip4/0.0.0.0/tcp/4001)
#   --min-outbound N   Minimum outbound connections (default: 4)
#   --store-path PATH  Persistent store path (optional)
#   --control PATH     Control file path (default: /tmp/fall25_node/daemon.json)
#   --log PATH         Log file path (default: /tmp/fall25_node/bootstrap.log)

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
KEY_PATH="${KEY_PATH:-$HOME/.sng40/ec2/bootstrap.key}"
LISTEN_ADDR="${LISTEN_ADDR:-/ip4/0.0.0.0/tcp/4001}"
MIN_OUTBOUND="${MIN_OUTBOUND:-4}"
STORE_PATH="${STORE_PATH:-}"
CONTROL_PATH="${CONTROL_PATH:-/tmp/fall25_node/daemon.json}"
LOG_PATH="${LOG_PATH:-/tmp/fall25_node/bootstrap.log}"

# Parse args
while [[ $# -gt 0 ]]; do
  case $1 in
    --key-path)
      KEY_PATH="$2"
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
      echo "Start bootstrap node on EC2/remote host via Tailscale"
      echo ""
      echo "Options:"
      echo "  --key-path PATH    Node key file (default: ~/.sng40/ec2/bootstrap.key)"
      echo "  --listen ADDR      Listen address (default: /ip4/0.0.0.0/tcp/4001)"
      echo "  --min-outbound N   Min outbound connections (default: 4)"
      echo "  --store-path PATH  Persistent store path (optional)"
      echo "  --control PATH     Control file path (default: /tmp/fall25_node/daemon.json)"
      echo "  --log PATH         Log file path (default: /tmp/fall25_node/bootstrap.log)"
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      exit 1
      ;;
  esac
done

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

# Start node
echo "Starting bootstrap node..."
echo "  Key: $KEY_PATH"
echo "  Listen: $LISTEN_ADDR"
echo "  Control: $CONTROL_PATH"
echo "  Log: $LOG_PATH"
"${CMD[@]}" || {
  echo "Failed to start bootstrap node" >&2
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
if [[ -z "$ID_JSON" ]]; then
  echo "Warning: Could not fetch node ID" >&2
else
  PEER_ID="$(echo "$ID_JSON" | jq -r '.peer' 2>/dev/null || true)"
  TAILSCALE_IP="$(ip addr show tailscale0 2>/dev/null | grep -oP 'inet \K[0-9.]+' | head -n1 || true)"
  
  echo ""
  echo "Bootstrap node started successfully!"
  echo "  Control address: $CTRL_ADDR"
  echo "  Peer ID: $PEER_ID"
  if [[ -n "$TAILSCALE_IP" ]]; then
    # Extract port from LISTEN_ADDR or use default
    PORT="4001"
    if [[ "$LISTEN_ADDR" =~ /tcp/([0-9]+) ]]; then
      PORT="${BASH_REMATCH[1]}"
    fi
    SEED="/ip4/$TAILSCALE_IP/tcp/$PORT/p2p/$PEER_ID"
    echo "  Tailscale IP: $TAILSCALE_IP"
    echo "  Seed: $SEED"
    echo ""
    echo "Export this for peer nodes:"
    echo "  export SNG40_SEEDS=\"$SEED\""
  else
    echo "  (Tailscale IP not detected - check manually)"
  fi
fi


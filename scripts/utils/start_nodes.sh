#!/usr/bin/env bash
set -e

NODE_COUNT=${1:-10}
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "=========================================="
echo "Starting $NODE_COUNT vn-IPFS nodes..."
echo "=========================================="
if [[ -f "$SCRIPT_DIR/../docker/start_vnipfs.sh" ]]; then
    "$SCRIPT_DIR/../docker/start_vnipfs.sh" "$NODE_COUNT"
else
    "$SCRIPT_DIR/../docker/start.sh" "$NODE_COUNT"
fi

echo ""
echo "=========================================="
echo "Starting $NODE_COUNT Swarm nodes..."
echo "=========================================="
"$SCRIPT_DIR/../docker/swarm/start.sh" "$NODE_COUNT"

echo ""
echo "=========================================="
echo "Finished starting $NODE_COUNT vn-IPFS and Swarm nodes."
echo "=========================================="

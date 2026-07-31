#!/usr/bin/env bash
set -e

NODE_COUNT=${1:-10}
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "=========================================="
echo "Starting $NODE_COUNT Tarsus nodes..."
echo "=========================================="
"$SCRIPT_DIR/../docker/start.sh" "$NODE_COUNT"

echo ""
echo "=========================================="
echo "Finished starting $NODE_COUNT Tarsus nodes."
echo "=========================================="

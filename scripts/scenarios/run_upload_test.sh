#!/usr/bin/env bash
set -euo pipefail

# Purpose: Start both systems and run upload latency test
# Usage: ./scripts/scenarios/run_upload_test.sh [options]
#   --our-nodes <n>      Number of our system nodes (default: 2)
#   --swarm-nodes <n>    Number of Swarm nodes (default: 1)
#   --iterations <n>     Test iterations per size (default: 10)
#   --skip-start         Skip starting Docker containers (assume already running)
#   --skip-swarm         Skip starting Swarm (assume already running)
#   --skip-our           Skip starting our system (assume already running)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

OUR_NODES=2
SWARM_NODES=1
ITERATIONS=10
SKIP_START=false
SKIP_SWARM=false
SKIP_OUR=false

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --our-nodes)
      OUR_NODES="$2"
      shift 2
      ;;
    --swarm-nodes)
      SWARM_NODES="$2"
      shift 2
      ;;
    --iterations)
      ITERATIONS="$2"
      shift 2
      ;;
    --skip-start)
      SKIP_START=true
      shift
      ;;
    --skip-swarm)
      SKIP_SWARM=true
      shift
      ;;
    --skip-our)
      SKIP_OUR=true
      shift
      ;;
    --help)
      echo "Usage: $0 [options]"
      echo "Options:"
      echo "  --our-nodes <n>      Number of our system nodes (default: 2)"
      echo "  --swarm-nodes <n>    Number of Swarm nodes (default: 1)"
      echo "  --iterations <n>     Test iterations per size (default: 10)"
      echo "  --skip-start         Skip starting Docker containers"
      echo "  --skip-swarm         Skip starting Swarm"
      echo "  --skip-our           Skip starting our system"
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      exit 1
      ;;
  esac
done

cd "$ROOT_DIR"

echo "=========================================="
echo "Upload Latency Test - Full Setup"
echo "=========================================="
echo "Our system nodes: $OUR_NODES"
echo "Swarm nodes: $SWARM_NODES"
echo "Iterations per size: $ITERATIONS"
echo ""

# Start our system Docker containers
if [[ "$SKIP_START" != "true" && "$SKIP_OUR" != "true" ]]; then
  echo "Step 1: Starting our system Docker containers..."
  if ! "$ROOT_DIR/scripts/docker/start.sh" "$OUR_NODES"; then
    echo "ERROR: Failed to start our system containers" >&2
    exit 1
  fi
  echo ""
else
  echo "Skipping our system startup (--skip-our or --skip-start)"
fi

# Start Swarm Docker containers
if [[ "$SKIP_START" != "true" && "$SKIP_SWARM" != "true" ]]; then
  echo "Step 2: Starting Swarm Docker containers..."
  if ! "$ROOT_DIR/scripts/docker/swarm/start.sh" "$SWARM_NODES"; then
    echo "ERROR: Failed to start Swarm containers" >&2
    exit 1
  fi
  echo ""
else
  echo "Skipping Swarm startup (--skip-swarm or --skip-start)"
fi

# Wait a bit for everything to stabilize
echo "Step 3: Waiting for systems to stabilize..."
sleep 5
echo ""

# Run the upload test
echo "Step 4: Running upload latency test..."
echo ""
"$ROOT_DIR/scripts/scenarios/swarm_upload_test.sh" \
  --iterations "$ITERATIONS"

echo ""
echo "=========================================="
echo "Test Complete!"
echo "=========================================="

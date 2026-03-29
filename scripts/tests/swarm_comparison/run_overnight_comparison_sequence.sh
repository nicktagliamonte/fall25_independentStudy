#!/usr/bin/env bash
set -euo pipefail

# Purpose: Run the swarm comparison matrix split by system — for each node count, run each test
# against vn-IPFS only, then Swarm only (half the containers vs starting both stacks at once).
# Prunes Docker build cache and unused data between (node_count, system) blocks to limit disk/RAM.

# Run from repo root:
#   ./scripts/tests/swarm_comparison/run_overnight_comparison_sequence.sh 2>&1 | tee overnight_matrix.log
# Stops on first non-zero exit (set -e). To continue after failures, run with: bash -c 'set +e; source ./scripts/...'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"
cd "$ROOT_DIR"

stamp() { echo "=== $(date -Iseconds) === $*"; }

ITERATIONS="${ITERATIONS:-10}"
NODE_COUNTS=(10 50 100)
SYSTEMS=(vnipfs swarm)

# Order: lighter / structural tests first; upload last per cell (heaviest).
TESTS=(
  download_cold
  download_warm
  lookup_latency
  lookup_complexity
  replication
  replication_distribution
  repair_time
  network_hops
  routing_overhead
  storage_efficiency
  concurrent
  upload
)

# vn-IPFS-only tests (no Swarm analogue in this harness): skip when --system swarm.
skip_for_swarm() {
  local t="$1"
  case "$t" in
    lookup_complexity|repair_time|replication|replication_distribution|network_hops) return 0 ;;
    *) return 1 ;;
  esac
}

docker_prune_block() {
  stamp "docker prune (builder / buildx / system)"
  docker builder prune -a -f 2>/dev/null || true
  docker buildx prune -a -f 2>/dev/null || true
  docker system prune -f 2>/dev/null || true
}

for N in "${NODE_COUNTS[@]}"; do
  for SYSTEM in "${SYSTEMS[@]}"; do
    stamp "node_count=$N system=$SYSTEM"
    for T in "${TESTS[@]}"; do
      if [[ "$SYSTEM" == "swarm" ]] && skip_for_swarm "$T"; then
        stamp "skip test=$T system=swarm (vn-IPFS-only)"
        continue
      fi
      stamp "run_single_comparison.sh --test $T --nodes $N --iterations $ITERATIONS --system $SYSTEM"
      "$SCRIPT_DIR/run_single_comparison.sh" --test "$T" --nodes "$N" --iterations "$ITERATIONS" --system "$SYSTEM"
    done
    docker_prune_block
  done
done

stamp "sequence complete"

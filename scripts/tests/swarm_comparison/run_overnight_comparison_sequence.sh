#!/usr/bin/env bash
set -euo pipefail

# Purpose: Run the swarm comparison matrix split by system — for each node count, run each test
# against vn-IPFS only, then Swarm only (half the containers vs starting both stacks at once).
# Prunes Docker build cache and unused data between (node_count, system) blocks to limit disk/RAM.
# vn-IPFS image is built once per start (see start_vnipfs.sh: build bootstrap only); full rebuilds after prune must succeed.

# Run from repo root:
#   ./scripts/tests/swarm_comparison/run_overnight_comparison_sequence.sh 2>&1 | tee overnight_matrix.log
# Stops on first failing run_comparison (set -e). To keep going after a bad cell:
#   CONTINUE_ON_ERROR=1 ./scripts/tests/swarm_comparison/run_overnight_comparison_sequence.sh 2>&1 | tee overnight_matrix.log

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"
cd "$ROOT_DIR"

stamp() { echo "=== $(date -Iseconds) === $*"; }

ITERATIONS="${ITERATIONS:-50}"
NODE_COUNTS=(10 50 100)
SYSTEMS=(vnipfs swarm)

# Order: lighter / structural tests first; upload last per cell (heaviest).
# download_warm_raw: same-node GET latency with raw stream mode for vn-IPFS (graphable).
# lookup_latency omitted (often uninformative on LAN; use --tests lookup_latency manually if needed).
TESTS=(
  download_warm_raw
  lookup_complexity
  replication
  replication_distribution
  repair_time
  routing_overhead
  storage_efficiency
  concurrent
  upload
)

# vn-IPFS-only tests (no Swarm analogue in this harness): skip when --system swarm.
skip_for_swarm() {
  local t="$1"
  case "$t" in
    lookup_complexity|repair_time|replication|replication_distribution) return 0 ;;
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
      set +e
      "$SCRIPT_DIR/run_single_comparison.sh" --test "$T" --nodes "$N" --iterations "$ITERATIONS" --system "$SYSTEM"
      rc=$?
      set -e
      if [[ "$rc" -ne 0 ]]; then
        stamp "run_single_comparison failed (exit $rc) test=$T N=$N system=$SYSTEM"
        [[ "${CONTINUE_ON_ERROR:-0}" == "1" ]] || exit "$rc"
      fi
    done
    docker_prune_block
  done
done

stamp "sequence complete"

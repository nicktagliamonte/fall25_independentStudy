#!/usr/bin/env bash
set -euo pipefail

# Purpose: One command: tear down Swarm, rebuild cluster with pinning + zero in-memory chunk cache,
# run catalog growth with first-key fetch + host-wall GET timing + eviction, write CSV + latency PNG.
# Host-wall timing measures docker exec + curl on the host (ms-scale); in-container curl time_total is
# often sub-ms noise on LAN. Requires Linux date +%s%N on the host.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT_DIR"

N="${SWARM_CATALOG_N:-50}"
MAX_FILES="${SWARM_CATALOG_FILES:-512}"
PAYLOAD="${SWARM_CATALOG_PAYLOAD_BYTES:-262144}"
TRIALS="${CATALOG_GROWTH_TRIALS:-3}"
RESULTS_DIR="${SWARM_CATALOG_OUT_DIR:-$ROOT_DIR/test_results/catalog_swarm_512}"
CSV_NAME="catalog_growth_n${N}.csv"
MERGE_SH="$ROOT_DIR/scripts/tests/swarm_comparison/catalog_growth_merge.sh"

export SWARM_ENABLE_PINNING=true
export SWARM_STORE_CACHE_CAPACITY=0
export CATALOG_GROWTH_SWARM_FETCH=first
export CATALOG_GROWTH_HOST_WALL_GET="${CATALOG_GROWTH_HOST_WALL_GET:-1}"
export CATALOG_GROWTH_SWARM_HOST_WALL_GET=1
export CATALOG_GROWTH_MAX_OBJECTS="$MAX_FILES"
export CATALOG_GROWTH_PAYLOAD_BYTES="$PAYLOAD"
export CMP_INCLUDE_SWARM="${CMP_INCLUDE_SWARM:-1}"

echo "==> Swarm catalog benchmark: N=$N files=$MAX_FILES payload=$PAYLOAD trials=$TRIALS (fresh volumes per trial)"
echo "    OUT: $RESULTS_DIR/$CSV_NAME"

mkdir -p "$RESULTS_DIR"
OUT="$RESULTS_DIR/$CSV_NAME"
PASS_FILES=()
for ((t = 1; t <= TRIALS; t++)); do
  echo "==> Trial $t/$TRIALS: docker-compose down -v (swarm)"
  docker-compose -f "$ROOT_DIR/docker-compose.swarm.yml" down -v 2>/dev/null || true
  echo "==> start.sh (build + up)"
  "$ROOT_DIR/scripts/docker/swarm/start.sh" "$N"
  pf="$RESULTS_DIR/.catalog_swarm_pass_${t}.csv"
  CATALOG_GROWTH_TRIALS=1 "$ROOT_DIR/scripts/tests/swarm_comparison/catalog_growth_swarm_test.sh" \
    --node-count "$N" \
    --max-files "$MAX_FILES" \
    --payload-size "$PAYLOAD" \
    --output "$pf"
  PASS_FILES+=("$pf")
done

if [[ "$TRIALS" -eq 1 ]]; then
  cp -f "${PASS_FILES[0]}" "$OUT"
else
  [[ -x "$MERGE_SH" ]] || MERGE_SH="bash $MERGE_SH"
  "$MERGE_SH" "$OUT" "${PASS_FILES[@]}"
fi
rm -f "${PASS_FILES[@]}"

python3 "$ROOT_DIR/scripts/analysis/catalog_growth_plot.py" "$OUT" \
  -o "$RESULTS_DIR/catalog_growth_n${N}_latency.png"

echo "==> Done: $OUT"
echo "    Plot: $RESULTS_DIR/catalog_growth_n${N}_latency.png"

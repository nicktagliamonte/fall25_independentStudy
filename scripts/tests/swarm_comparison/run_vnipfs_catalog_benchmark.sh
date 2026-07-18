#!/usr/bin/env bash
set -euo pipefail

# Purpose: One shot: tear down vn-IPFS (containers + named volumes), start N nodes, catalog growth CSV, latency PNG.
# Usage: ./run_vnipfs_catalog_benchmark.sh
# Env: VN_CATALOG_N (default 50), VN_CATALOG_FILES (512), VN_CATALOG_PAYLOAD_BYTES (default 262144),
#      CATALOG_GROWTH_TRIALS (default 3; fresh volumes per trial), VN_CATALOG_OUT_DIR

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT_DIR"

N="${VN_CATALOG_N:-50}"
MAX_FILES="${VN_CATALOG_FILES:-512}"
PAYLOAD="${VN_CATALOG_PAYLOAD_BYTES:-262144}"
TRIALS="${CATALOG_GROWTH_TRIALS:-3}"
RESULTS_DIR="${VN_CATALOG_OUT_DIR:-$ROOT_DIR/test_results/catalog_growth_512}"
COMPOSE_VN="$ROOT_DIR/docker-compose.vnipfs.yml"
MERGE_SH="$ROOT_DIR/scripts/tests/swarm_comparison/catalog_growth_merge.sh"

export CATALOG_GROWTH_HOST_WALL_GET="${CATALOG_GROWTH_HOST_WALL_GET:-1}"
export CATALOG_GROWTH_MAX_OBJECTS="$MAX_FILES"
export CATALOG_GROWTH_PAYLOAD_BYTES="$PAYLOAD"
export CMP_INCLUDE_OUR="${CMP_INCLUDE_OUR:-1}"

echo "==> vn-IPFS catalog benchmark: N=$N files=$MAX_FILES payload=$PAYLOAD trials=$TRIALS (fresh volumes per trial)"
echo "    OUT: $RESULTS_DIR/catalog_growth_n${N}.csv"

mkdir -p "$RESULTS_DIR"
OUT="$RESULTS_DIR/catalog_growth_n${N}.csv"
PASS_FILES=()

for ((t = 1; t <= TRIALS; t++)); do
  if [[ -f "$COMPOSE_VN" ]] && command -v docker-compose >/dev/null 2>&1; then
    echo "==> Trial $t/$TRIALS: docker-compose down -v (vn-IPFS)"
    docker-compose -f "$COMPOSE_VN" down -v 2>/dev/null || true
  fi
  echo "==> start_vnipfs.sh"
  "$ROOT_DIR/scripts/docker/start_vnipfs.sh" "$N"
  pf="$RESULTS_DIR/.catalog_vn_pass_${t}.csv"
  CATALOG_GROWTH_TRIALS=1 "$ROOT_DIR/scripts/tests/swarm_comparison/catalog_growth_test.sh" \
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

#!/usr/bin/env bash
set -euo pipefail

# Purpose: One shot: tear down vn-IPFS (containers + named volumes), start N nodes, catalog growth CSV, latency PNG.
# Usage: ./run_vnipfs_catalog_benchmark.sh
# Env: VN_CATALOG_N (default 50), VN_CATALOG_FILES (512), VN_CATALOG_PAYLOAD_BYTES (8192),
#      VN_CATALOG_OUT_DIR (default ROOT/test_results/catalog_growth_512)

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT_DIR"

N="${VN_CATALOG_N:-50}"
MAX_FILES="${VN_CATALOG_FILES:-512}"
PAYLOAD="${VN_CATALOG_PAYLOAD_BYTES:-8192}"
RESULTS_DIR="${VN_CATALOG_OUT_DIR:-$ROOT_DIR/test_results/catalog_growth_512}"
COMPOSE_VN="$ROOT_DIR/docker-compose.vnipfs.yml"

export CATALOG_GROWTH_HOST_WALL_GET="${CATALOG_GROWTH_HOST_WALL_GET:-1}"
export CATALOG_GROWTH_MAX_OBJECTS="$MAX_FILES"
export CATALOG_GROWTH_PAYLOAD_BYTES="$PAYLOAD"
export CMP_INCLUDE_OUR="${CMP_INCLUDE_OUR:-1}"

echo "==> vn-IPFS catalog benchmark: N=$N files=$MAX_FILES payload=$PAYLOAD"
echo "    OUT: $RESULTS_DIR/catalog_growth_n${N}.csv"

if [[ -f "$COMPOSE_VN" ]] && command -v docker-compose >/dev/null 2>&1; then
  if docker-compose -f "$COMPOSE_VN" ps 2>/dev/null | grep -q "Up"; then
    echo "==> docker-compose down -v (vn-IPFS)"
    docker-compose -f "$COMPOSE_VN" down -v 2>/dev/null || true
  fi
fi

echo "==> start_vnipfs.sh"
"$ROOT_DIR/scripts/docker/start_vnipfs.sh" "$N"

mkdir -p "$RESULTS_DIR"
OUT="$RESULTS_DIR/catalog_growth_n${N}.csv"

"$ROOT_DIR/scripts/tests/swarm_comparison/catalog_growth_test.sh" \
  --node-count "$N" \
  --max-files "$MAX_FILES" \
  --payload-size "$PAYLOAD" \
  --output "$OUT"

python3 "$ROOT_DIR/scripts/analysis/catalog_growth_plot.py" "$OUT" \
  -o "$RESULTS_DIR/catalog_growth_n${N}_latency.png"

echo "==> Done: $OUT"
echo "    Plot: $RESULTS_DIR/catalog_growth_n${N}_latency.png"

#!/usr/bin/env bash
set -euo pipefail

# Purpose: Run one swarm comparison matrix cell — one test name, one node count, fixed iterations — under test_results/matrix/<test>_n<N>_i<I>/ by default. Wraps run_comparison.sh.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

TEST=""
NODES="10"
ITERATIONS="10"
MATRIX_ROOT="$ROOT_DIR/test_results/matrix"
SKIP_START=false
SYSTEM_MODE="both"
EXTRA=()

usage() {
  cat <<EOF
Usage: $0 --test <name> [options] [--] [extra args for run_comparison.sh]

Options:
  --test <name>       Single test (required). Same names as run_comparison.sh --tests list.
  --nodes <n>         One node count: 10, 50, 100, or 500 (default: 10)
  --iterations <i>    Iterations per test (default: 10)
  --system <mode>     both | vnipfs | swarm — start only that stack (default: both). Aliases: ours→vnipfs.
  --matrix-root <dir> Parent directory for per-cell dirs (default: <repo>/test_results/matrix)
  --skip-start        Passed through: clusters already running at N
  --help              This help

After --, remaining arguments are appended to run_comparison.sh (e.g. -- --validate --batch-sizes 1).

Example:
  $0 --test upload --nodes 10 --iterations 10
  $0 --test upload --nodes 100 --iterations 10 --system vnipfs
  $0 --test download_warm_raw --nodes 50 --iterations 10 --system swarm
  $0 --test lookup_complexity --nodes 50 --iterations 10 --skip-start
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --test)
      TEST="$2"
      shift 2
      ;;
    --nodes)
      NODES="$2"
      shift 2
      ;;
    --iterations)
      ITERATIONS="$2"
      shift 2
      ;;
    --matrix-root)
      MATRIX_ROOT="$2"
      shift 2
      ;;
    --skip-start)
      SKIP_START=true
      shift
      ;;
    --system)
      SYSTEM_MODE="$2"
      shift 2
      ;;
    --help)
      usage
      exit 0
      ;;
    --)
      shift
      EXTRA=("$@")
      break
      ;;
    *)
      echo "Unknown option: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

[[ -n "$TEST" ]] || { echo "Error: --test <name> is required" >&2; exit 1; }

NODES="${NODES// /}"
IFS=',' read -ra NC_ARR <<< "$NODES"
if [[ "${#NC_ARR[@]}" -ne 1 ]]; then
  echo "Error: exactly one node count required for matrix layout (got: $NODES)" >&2
  exit 1
fi
N="${NC_ARR[0]}"

case ",$TEST," in
  *,upload,*|*,download_warm_raw,*|*,lookup_latency,*|*,lookup_complexity,*|*,catalog_growth,*|*,replication,*|*,replication_distribution,*|*,repair_time,*|*,routing_overhead,*|*,storage_efficiency,*|*,concurrent,*) ;;
  *)
    echo "Error: unknown test '$TEST'. Run: run_comparison.sh --tests list" >&2
    exit 1
    ;;
esac

if [[ "$MATRIX_ROOT" != /* ]]; then
  MATRIX_ROOT="${ROOT_DIR}/${MATRIX_ROOT#./}"
fi

case "${SYSTEM_MODE,,}" in
  both)
    OUT_SUFFIX=""
    ;;
  vnipfs|vn-ipfs|ours|our|vn)
    OUT_SUFFIX="_vnipfs"
    ;;
  swarm|bee)
    OUT_SUFFIX="_swarm"
    ;;
  *)
    echo "Error: --system must be both, vnipfs, or swarm (got: $SYSTEM_MODE)" >&2
    exit 1
    ;;
esac

OUT="${MATRIX_ROOT}/${TEST}_n${N}_i${ITERATIONS}${OUT_SUFFIX}"
mkdir -p "$OUT"

RC_ARGS=(
  --nodes "$N"
  --iterations "$ITERATIONS"
  --tests "$TEST"
  --output-dir "$OUT"
  --system "$SYSTEM_MODE"
)
[[ "$SKIP_START" == "true" ]] && RC_ARGS+=(--skip-start)

echo "Matrix cell output: $OUT"
exec "$SCRIPT_DIR/run_comparison.sh" "${RC_ARGS[@]}" "${EXTRA[@]}"

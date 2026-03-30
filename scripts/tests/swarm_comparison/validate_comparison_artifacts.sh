#!/usr/bin/env bash
set -euo pipefail

# Purpose: Post-run check that expected Swarm comparison CSVs exist and have at least one data row.
# Usage: validate_comparison_artifacts.sh --dir <results_dir> --nodes <list> [--tests <list>] [--batch-sizes <list>]
#   --tests empty = assume full suite (all known tests). Omit rows for tests not in --tests.
# Exit 1 if any expected artifact is missing or has no data rows (gap report on stderr).

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

RESULTS_DIR=""
NODES_STR=""
TESTS_FILTER=""
BATCH_SIZES_STR="1,5"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dir) RESULTS_DIR="$2"; shift 2 ;;
    --nodes) NODES_STR="$2"; shift 2 ;;
    --tests) TESTS_FILTER="${2// /}"; shift 2 ;;
    --batch-sizes) BATCH_SIZES_STR="$2"; shift 2 ;;
    --help)
      echo "Usage: $0 --dir <results_dir> --nodes <comma_n> [--tests <names>] [--batch-sizes <list>]"
      exit 0
      ;;
    *) echo "Unknown option: $1" >&2; exit 1 ;;
  esac
done

[[ -n "$RESULTS_DIR" ]] || { echo "Missing --dir" >&2; exit 1; }
[[ -n "$NODES_STR" ]] || { echo "Missing --nodes" >&2; exit 1; }
RESULTS_DIR="${RESULTS_DIR/#\~/$HOME}"
[[ -d "$RESULTS_DIR" ]] || { echo "Not a directory: $RESULTS_DIR" >&2; exit 1; }

IFS=',' read -ra NODE_COUNTS <<< "$NODES_STR"
IFS=',' read -ra BATCH_ARR <<< "$BATCH_SIZES_STR"
last_n="${NODE_COUNTS[-1]}"

wants_test() {
  local name="$1"
  if [[ -z "$TESTS_FILTER" ]]; then
    [[ "$name" == "lookup_latency" ]] && [[ "${INCLUDE_LOOKUP_LATENCY:-0}" != "1" ]] && return 1
    return 0
  fi
  [[ ",${TESTS_FILTER}," == *",${name},"* ]]
}

data_rows() {
  local f="$1"
  [[ -f "$f" ]] || return 1
  local n
  n=$(tail -n +2 "$f" 2>/dev/null | grep -c . || true)
  [[ "$n" -ge 1 ]]
}

rows_for_node_in_csv() {
  local file="$1"
  local n="$2"
  local col="${3:-2}"
  [[ -f "$file" ]] || return 1
  awk -F',' -v want="$n" -v c="$col" 'NR>1 && $c==want { ok=1 } END { exit ok ? 0 : 1 }' "$file"
}

failures=0
note() { echo "[validate] $*" >&2; }

for n in "${NODE_COUNTS[@]}"; do
  if wants_test "upload"; then
    found=false
    for b in "${BATCH_ARR[@]}"; do
      f="$RESULTS_DIR/upload_n${n}_batch${b}.csv"
      if data_rows "$f"; then found=true; break; fi
    done
    if [[ "$found" != "true" ]]; then
      note "FAIL upload N=$n: no upload_n${n}_batch*.csv with data (checked batches ${BATCH_ARR[*]})"
      failures=$((failures + 1))
    fi
  fi
  if wants_test "download_warm"; then
    f="$RESULTS_DIR/download_n${n}_warm.csv"
    if ! data_rows "$f"; then
      note "FAIL download_warm N=$n: missing or empty $f"
      failures=$((failures + 1))
    fi
  fi
  if wants_test "lookup_latency"; then
    f="$RESULTS_DIR/lookup_latency_n${n}.csv"
    if ! data_rows "$f"; then
      note "FAIL lookup_latency N=$n: missing or empty $f"
      failures=$((failures + 1))
    fi
  fi
  if wants_test "lookup_complexity"; then
    f="$RESULTS_DIR/lookup_complexity_results.csv"
    if ! rows_for_node_in_csv "$f" "$n"; then
      note "FAIL lookup_complexity N=$n: no data rows with node_count=$n in $f"
      failures=$((failures + 1))
    fi
  fi
  if wants_test "replication"; then
    f="$RESULTS_DIR/replication_results.csv"
    if ! rows_for_node_in_csv "$f" "$n" 3; then
      note "FAIL replication N=$n: no data rows with nodes=$n in $f"
      failures=$((failures + 1))
    fi
  fi
  if wants_test "replication_distribution"; then
    f="$RESULTS_DIR/replication_distribution.csv"
    if ! rows_for_node_in_csv "$f" "$n"; then
      note "FAIL replication_distribution N=$n: no data rows with node_count=$n in $f"
      failures=$((failures + 1))
    fi
  fi
  if wants_test "repair_time"; then
    f="$RESULTS_DIR/repair_time_results.csv"
    if ! rows_for_node_in_csv "$f" "$n"; then
      note "FAIL repair_time N=$n: no data rows with node_count=$n in $f"
      failures=$((failures + 1))
    fi
  fi
done

if wants_test "routing_overhead"; then
  f="$RESULTS_DIR/routing_overhead_results.csv"
  if ! data_rows "$f"; then
    note "FAIL routing_overhead: missing or empty $f"
    failures=$((failures + 1))
  fi
fi
if wants_test "storage_efficiency"; then
  f="$RESULTS_DIR/storage_efficiency_results.csv"
  if ! data_rows "$f"; then
    note "FAIL storage_efficiency: missing or empty $f"
    failures=$((failures + 1))
  fi
fi
if wants_test "concurrent"; then
  f="$RESULTS_DIR/concurrent_results.csv"
  if ! data_rows "$f"; then
    note "FAIL concurrent: missing or empty $f"
    failures=$((failures + 1))
  fi
fi

f="$RESULTS_DIR/summary_report.txt"
if [[ ! -f "$f" ]]; then
  note "WARN: missing $f (non-fatal)"
fi

if [[ "$failures" -gt 0 ]]; then
  note "Summary: $failures gap(s) under $RESULTS_DIR"
  exit 1
fi
note "OK: expected artifacts present for nodes=${NODE_COUNTS[*]}"
exit 0

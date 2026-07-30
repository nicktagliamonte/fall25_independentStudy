#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/common.sh"

config_file=""
execute=0
resume_dir=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --config) config_file=$2; shift 2 ;;
    --execute) execute=1; shift ;;
    --resume) resume_dir=$2; shift 2 ;;
    --help)
      echo "usage: $0 [--config FILE] [--resume RUN_DIR] [--execute]"
      echo "without --execute, writes and prints the resolved campaign plan only"
      exit 0
      ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [[ -n "$config_file" ]]; then
  # shellcheck disable=SC1090
  source "$config_file"
fi

NODE_COUNTS=${NODE_COUNTS:-"10 50 100"}
CATALOG_SIZES=${CATALOG_SIZES:-"1000 10000"}
SHARD_COUNTS=${SHARD_COUNTS:-"1 4 16 64"}
LARGE_NODE_COUNT=${LARGE_NODE_COUNT:-100}
LARGE_CATALOG_SIZE=${LARGE_CATALOG_SIZE:-10000}
QUERY_REPETITIONS=${QUERY_REPETITIONS:-30}
CLIENT_COUNT=${CLIENT_COUNT:-5}
RESULT_ROOT=${RESULT_ROOT:-test_results/tarsus_campaign}
export POPULATE_CONCURRENCY=${POPULATE_CONCURRENCY:-32}
export POPULATE_BATCH_SIZE=${POPULATE_BATCH_SIZE:-2500}
export RESOURCE_INTERVAL_SECONDS=${RESOURCE_INTERVAL_SECONDS:-5}
export SETTLE_SECONDS=${SETTLE_SECONDS:-30}
export AVAILABILITY_WAIT_SECONDS=${AVAILABILITY_WAIT_SECONDS:-75}
export TARSUS_MIN_OUTBOUND=${TARSUS_MIN_OUTBOUND:-3}
export RUN_RESILIENCE=${RUN_RESILIENCE:-true}
export RESILIENCE_NODE_COUNT=${RESILIENCE_NODE_COUNT:-100}
export RESILIENCE_PAYLOAD_BYTES=${RESILIENCE_PAYLOAD_BYTES:-8388608}
export RESILIENCE_TRIALS=${RESILIENCE_TRIALS:-5}
export RESILIENCE_REPLICA_TARGET=${RESILIENCE_REPLICA_TARGET:-7}

if [[ -n "$resume_dir" ]]; then
  run_dir=$resume_dir
else
  run_id="$(date -u +%Y%m%dT%H%M%SZ)-$(git -C "$REPO_ROOT" rev-parse --short HEAD)"
  run_dir="$REPO_ROOT/$RESULT_ROOT/$run_id"
fi
mkdir -p "$run_dir"

plan="$run_dir/plan.tsv"
if [[ ! -s "$plan" ]]; then
  echo -e "cell_id\tnode_count\tcatalog_size\tindex_shards\tbloom_pruning" >"$plan"
  for nodes in $NODE_COUNTS; do
    for catalog in $CATALOG_SIZES; do
      printf 'scale-n%03d-c%07d-s016-bloom-on\t%s\t%s\t16\ttrue\n' \
        "$nodes" "$catalog" "$nodes" "$catalog" >>"$plan"
    done
  done
  for shards in $SHARD_COUNTS; do
    printf 'shards-n%03d-c%07d-s%03d-bloom-on\t%s\t%s\t%s\ttrue\n' \
      "$LARGE_NODE_COUNT" "$LARGE_CATALOG_SIZE" "$shards" \
      "$LARGE_NODE_COUNT" "$LARGE_CATALOG_SIZE" "$shards" >>"$plan"
  done
  printf 'ablation-n%03d-c%07d-s016-bloom-off\t%s\t%s\t16\tfalse\n' \
    "$LARGE_NODE_COUNT" "$LARGE_CATALOG_SIZE" \
    "$LARGE_NODE_COUNT" "$LARGE_CATALOG_SIZE" >>"$plan"
  awk -F '\t' 'NR == 1 {print; next} {key=$2 FS $3 FS $4 FS $5} !seen[key]++' "$plan" >"$plan.tmp"
  mv "$plan.tmp" "$plan"
fi

campaign_capture_host_manifest "$run_dir/host.json"
{
  echo "QUERY_REPETITIONS=$QUERY_REPETITIONS"
  echo "CLIENT_COUNT=$CLIENT_COUNT"
  echo "POPULATE_CONCURRENCY=$POPULATE_CONCURRENCY"
  echo "POPULATE_BATCH_SIZE=$POPULATE_BATCH_SIZE"
  echo "RESOURCE_INTERVAL_SECONDS=$RESOURCE_INTERVAL_SECONDS"
  echo "SETTLE_SECONDS=$SETTLE_SECONDS"
  echo "AVAILABILITY_WAIT_SECONDS=$AVAILABILITY_WAIT_SECONDS"
  echo "TARSUS_MIN_OUTBOUND=$TARSUS_MIN_OUTBOUND"
  echo "RUN_RESILIENCE=$RUN_RESILIENCE"
  echo "RESILIENCE_NODE_COUNT=$RESILIENCE_NODE_COUNT"
  echo "RESILIENCE_PAYLOAD_BYTES=$RESILIENCE_PAYLOAD_BYTES"
  echo "RESILIENCE_TRIALS=$RESILIENCE_TRIALS"
  echo "RESILIENCE_REPLICA_TARGET=$RESILIENCE_REPLICA_TARGET"
} >"$run_dir/resolved.env"

campaign_log "campaign plan: $plan"
column -t -s $'\t' "$plan" 2>/dev/null || cat "$plan"
if [[ "$execute" -ne 1 ]]; then
  campaign_log "dry plan only; rerun with --resume '$run_dir' --execute"
  exit 0
fi

campaign_require_tools
while IFS=$'\t' read -r cell_id nodes catalog shards bloom; do
  "$SCRIPT_DIR/run_cell.sh" "$run_dir/cells/$cell_id" \
    "$nodes" "$catalog" "$shards" "$bloom" "$QUERY_REPETITIONS" "$CLIENT_COUNT" \
    </dev/null
done < <(tail -n +2 "$plan")
if [[ "$RUN_RESILIENCE" == "true" ]]; then
  resilience_id=$(printf 'resilience-n%03d-b%09d-r%02d' \
    "$RESILIENCE_NODE_COUNT" "$RESILIENCE_PAYLOAD_BYTES" \
    "$RESILIENCE_REPLICA_TARGET")
  "$SCRIPT_DIR/run_resilience_cell.sh" \
    "$run_dir/resilience/$resilience_id" \
    "$RESILIENCE_NODE_COUNT" "$RESILIENCE_PAYLOAD_BYTES" \
    "$RESILIENCE_TRIALS" "$RESILIENCE_REPLICA_TARGET" </dev/null
fi
"$SCRIPT_DIR/validate_campaign.sh" "$run_dir"
campaign_log "campaign complete: $run_dir"

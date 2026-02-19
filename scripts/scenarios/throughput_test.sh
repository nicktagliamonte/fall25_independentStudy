#!/usr/bin/env bash
set -euo pipefail

# Purpose: Measure restore throughput for random data of various sizes.
# Usage: bash scripts/scenarios/throughput_test.sh [N] [MIN_OUTBOUND] [SIZES]
#   N: number of nodes (default: 2)
#   MIN_OUTBOUND: min outbound peers (default: 1)
#   SIZES: comma-separated sizes (default: 1K,512K,1M,1536K,1G)

N="${1:-2}"
MIN_OUTBOUND="${2:-1}"
SIZES="${3:-1K,500K,1M,1500K}"
TOPOLOGY="${TOPOLOGY:-star}"
DURATION_S="${DURATION_S:-900}"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

size_to_bytes() {
  local s="$1"
  local num suffix
  if [[ "$s" =~ ^([0-9]+)([KMG]?)$ ]]; then
    num="${BASH_REMATCH[1]}"
    suffix="${BASH_REMATCH[2]}"
    case "$suffix" in
      K) echo $((num * 1024)) ;;
      M) echo $((num * 1024 * 1024)) ;;
      G) echo $((num * 1024 * 1024 * 1024)) ;;
      *) echo "$num" ;;
    esac
    return 0
  fi
  echo "Invalid size: $s" >&2
  return 1
}

IFS=',' read -ra SIZE_LIST <<< "$SIZES"

RUN_ID="$(date +%s)"
RESULTS_DIR="${ROOT_DIR}/artifacts/throughput_tests/${RUN_ID}"
mkdir -p "$RESULTS_DIR"

OUTPUT_CSV="${RESULTS_DIR}/throughput.csv"
echo "size_label,size_bytes,put_duration_s,restore_duration_s,restore_bytes,throughput_mb_s" > "$OUTPUT_CSV"

# Build and start nodes
make -C "$ROOT_DIR" build >/dev/null 2>&1

if ! N="$N" TOPOLOGY="$TOPOLOGY" MIN_OUTBOUND="$MIN_OUTBOUND" RUN_ID="$RUN_ID" DURATION_S="$DURATION_S" make -C "$ROOT_DIR" local >/dev/null 2>&1; then
  echo "ERROR: Failed to start nodes" >&2
  exit 1
fi

echo "RUN_ID=$RUN_ID"
echo "Results dir: $RESULTS_DIR"
echo "Waiting 30s for nodes to bootstrap..."
sleep 30

NODES_JSON="${ROOT_DIR}/artifacts/runs/${RUN_ID}/nodes.json"
BOOT_ADDR=$(jq -r '.[0].control_addr' "$NODES_JSON")
LEAF_ADDR=$(jq -r '.[1].control_addr' "$NODES_JSON")

if [[ -z "$BOOT_ADDR" || "$BOOT_ADDR" == "null" || -z "$LEAF_ADDR" || "$LEAF_ADDR" == "null" ]]; then
  echo "ERROR: Could not determine bootstrap/leaf addresses" >&2
  exit 1
fi

for SIZE_LABEL in "${SIZE_LIST[@]}"; do
  SIZE_BYTES=$(size_to_bytes "$SIZE_LABEL")
  echo "" 
  echo "=== Testing size: $SIZE_LABEL ($SIZE_BYTES bytes) ==="

  JSON_FILE="${RESULTS_DIR}/payload_${SIZE_LABEL}.json"

  echo "Generating payload..."
  python3 "${ROOT_DIR}/scripts/util/generate_put_json.py" \
    --size-bytes "$SIZE_BYTES" \
    --output "$JSON_FILE" \
    --chunk-bytes $((1024 * 1024))

  echo "PUT to bootstrap..."
  PUT_START_NS=$(date +%s%N)
  PUT_RESP=$(curl -s -X POST -H "Content-Type: application/json" --data-binary "@${JSON_FILE}" "http://${BOOT_ADDR}/put")
  PUT_END_NS=$(date +%s%N)
  CID=$(echo "$PUT_RESP" | jq -r '.cid // empty')

  if [[ -z "$CID" ]]; then
    echo "ERROR: PUT failed for size $SIZE_LABEL" >&2
    continue
  fi

  PUT_DURATION_S=$(python3 - <<PY
start=$PUT_START_NS
end=$PUT_END_NS
print((end-start)/1e9)
PY
)

  echo "Restore from leaf..."
  RESTORE_START_NS=$(date +%s%N)
  RESTORE_RESP=$(curl -s -X POST -H "Content-Type: application/json" -d "{\"cids\":[\"$CID\"],\"concurrency\":1,\"timeout\":\"15m\"}" "http://${LEAF_ADDR}/restore")
  JOB_ID=$(echo "$RESTORE_RESP" | jq -r '.job // empty')

  if [[ -z "$JOB_ID" ]]; then
    echo "ERROR: Restore submit failed for size $SIZE_LABEL" >&2
    continue
  fi

  RESTORE_BYTES=0
  while true; do
    STATUS_JSON=$(curl -s "http://${LEAF_ADDR}/restore/status?id=${JOB_ID}" 2>/dev/null || echo "{}")
    DONE=$(echo "$STATUS_JSON" | jq -r '.done // false')
    RESTORE_BYTES=$(echo "$STATUS_JSON" | jq -r '.bytes // 0')
    if [[ "$DONE" == "true" ]]; then
      break
    fi
    sleep 1
  done

  RESTORE_END_NS=$(date +%s%N)
  RESTORE_DURATION_S=$(python3 - <<PY
start=$RESTORE_START_NS
end=$RESTORE_END_NS
print((end-start)/1e9)
PY
)

  THROUGHPUT_MB_S=$(python3 - <<PY
bytes_val=$RESTORE_BYTES
duration=$RESTORE_DURATION_S
if duration <= 0:
    print(0)
else:
    print(bytes_val / duration / (1024*1024))
PY
)

  echo "$SIZE_LABEL,$SIZE_BYTES,$PUT_DURATION_S,$RESTORE_DURATION_S,$RESTORE_BYTES,$THROUGHPUT_MB_S" >> "$OUTPUT_CSV"
  echo "Done: throughput=${THROUGHPUT_MB_S} MB/s, restore_duration=${RESTORE_DURATION_S}s"

done

echo ""
echo "Generating throughput plot..."
python3 "${ROOT_DIR}/scripts/plots/throughput_plot.py" "$RESULTS_DIR" || echo "Plot generation failed"

# Shutdown nodes
jq -r '.[] | .control_addr' "$NODES_JSON" | while read -r addr; do
  curl -s "http://$addr/shutdown" >/dev/null 2>&1 || true
done

sleep 2

echo "Complete. Results: $RESULTS_DIR"

#!/usr/bin/env bash
set -uo pipefail

# Purpose: Test failure and repair scenario (shutdown victim, restore from donor)
# Usage: bash scripts/scenarios/failure_repair.sh <RUN_ID> [victim_id] [donor_id]
#   RUN_ID: directory name under artifacts/runs/
#   victim_id: Node ID to fail (default: random leaf node)
#   donor_id: Node ID to snapshot from (default: bootstrap/1)

RUN_ID="${1:-}"
VICTIM_ID="${2:-}"
DONOR_ID="${3:-1}"

# Show help
if [[ "$RUN_ID" == "-h" || "$RUN_ID" == "--help" || -z "$RUN_ID" ]]; then
  echo "Usage: $0 <RUN_ID> [victim_id] [donor_id]"
  echo ""
  echo "Test failure and repair scenario:"
  echo "  1. Shutdown victim node"
  echo "  2. Restart victim with same key"
  echo "  3. Snapshot donor node → manifest"
  echo "  4. Restore from manifest on victim"
  echo "  5. Measure ok/failed/bytes/duration"
  echo ""
  echo "Parameters:"
  echo "  RUN_ID     Directory name under artifacts/runs/"
  echo "  victim_id  Node ID to fail (default: random leaf)"
  echo "  donor_id   Node ID to snapshot from (default: 1/bootstrap)"
  echo ""
  echo "Output:"
  echo "  artifacts/runs/<RUN_ID>/repair.csv"
  exit 0
fi

NODES_JSON="artifacts/runs/$RUN_ID/nodes.json"
if [[ ! -f "$NODES_JSON" ]]; then
  echo "Error: $NODES_JSON not found" >&2
  exit 1
fi

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUTPUT_CSV="artifacts/runs/$RUN_ID/repair.csv"
MANIFEST_FILE="artifacts/runs/$RUN_ID/repair_manifest.txt"
mkdir -p "$(dirname "$OUTPUT_CSV")"

# Auto-select victim if not provided (pick a leaf node, not bootstrap)
if [[ -z "$VICTIM_ID" ]]; then
  NODE_COUNT=$(jq 'length' "$NODES_JSON")
  if [[ $NODE_COUNT -le 1 ]]; then
    echo "Error: Need at least 2 nodes for failure/repair test" >&2
    exit 1
  fi
  # Pick a random node from 2..N
  VICTIM_ID=$((RANDOM % (NODE_COUNT - 1) + 2))
fi

# Get node info
VICTIM_ADDR=$(jq -r ".[] | select(.id == $VICTIM_ID) | .control_addr" "$NODES_JSON")
VICTIM_KEY=$(jq -r ".[] | select(.id == $VICTIM_ID) | .key_path" "$NODES_JSON")
DONOR_ADDR=$(jq -r ".[] | select(.id == $DONOR_ID) | .control_addr" "$NODES_JSON")

if [[ -z "$VICTIM_ADDR" || "$VICTIM_ADDR" == "null" ]]; then
  echo "Error: Victim node $VICTIM_ID not found" >&2
  exit 1
fi

if [[ -z "$DONOR_ADDR" || "$DONOR_ADDR" == "null" ]]; then
  echo "Error: Donor node $DONOR_ID not found" >&2
  exit 1
fi

echo "Failure/Repair Test"
echo "=================="
echo "Run ID: $RUN_ID"
echo "Victim: Node $VICTIM_ID ($VICTIM_ADDR)"
echo "Donor:  Node $DONOR_ID ($DONOR_ADDR)"
echo ""

# Step 1: Shutdown victim
echo "Step 1: Shutting down victim node..."
SHUTDOWN_START=$(date +%s)
curl -sSf "http://$VICTIM_ADDR/shutdown" >/dev/null || true
sleep 2
SHUTDOWN_END=$(date +%s)
SHUTDOWN_DURATION=$((SHUTDOWN_END - SHUTDOWN_START))
echo "  Shutdown complete (${SHUTDOWN_DURATION}s)"
echo ""

# Step 2: Restart victim with same key
echo "Step 2: Restarting victim node..."
RESTART_START=$(date +%s)

# Extract listen address from original node config (or use defaults)
VICTIM_CONTROL="artifacts/runs/$RUN_ID/daemon_$VICTIM_ID.json"
VICTIM_LOG="artifacts/runs/$RUN_ID/daemon_$VICTIM_ID.log"

# Read original listen address if available, otherwise use default
LISTEN_ADDR="/ip4/127.0.0.1/tcp/4001"
if [[ -f "$VICTIM_CONTROL" ]]; then
  # Try to infer from control file or use default
  LISTEN_ADDR="/ip4/127.0.0.1/tcp/4001"
fi

# Get bootstrap seed if available
BOOT_SEED=""
BOOT_NODE=$(jq -r '.[0] | .control_addr' "$NODES_JSON" 2>/dev/null || true)
if [[ -n "$BOOT_NODE" && "$BOOT_NODE" != "null" ]]; then
  BOOT_ID_JSON=$(curl -sSf "http://$BOOT_NODE/id" 2>/dev/null || true)
  if [[ -n "$BOOT_ID_JSON" ]]; then
    BOOT_PEER=$(echo "$BOOT_ID_JSON" | jq -r '.peer' 2>/dev/null || true)
    BOOT_TCP=$(echo "$BOOT_ID_JSON" | jq -r '.addrs[] | select(test("/tcp/"))' 2>/dev/null | head -n1 || true)
    if [[ -n "$BOOT_TCP" && "$BOOT_TCP" != "null" && -n "$BOOT_PEER" && "$BOOT_PEER" != "null" ]]; then
      BOOT_SEED="${BOOT_TCP}/p2p/${BOOT_PEER}"
    fi
  fi
fi

# Start node
if [[ -n "$BOOT_SEED" ]]; then
  env SNG40_SEEDS="$BOOT_SEED" "$ROOT_DIR/bin/node" run \
    --listen "$LISTEN_ADDR" \
    --key "$VICTIM_KEY" \
    --daemon \
    --control "$VICTIM_CONTROL" \
    --log "$VICTIM_LOG" \
    --min-outbound 4 >/dev/null 2>&1 || true
else
  "$ROOT_DIR/bin/node" run \
    --listen "$LISTEN_ADDR" \
    --key "$VICTIM_KEY" \
    --daemon \
    --control "$VICTIM_CONTROL" \
    --log "$VICTIM_LOG" \
    --min-outbound 4 >/dev/null 2>&1 || true
fi

# Wait for control file
for i in {1..50}; do
  if [[ -s "$VICTIM_CONTROL" ]]; then
    break
  fi
  sleep 0.2
done

# Read new control address
if command -v jq >/dev/null 2>&1; then
  NEW_VICTIM_ADDR="$(jq -r '.Addr // .addr' "$VICTIM_CONTROL" 2>/dev/null || true)"
else
  NEW_VICTIM_ADDR="$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(d.get("Addr") or d.get("addr") or "")' "$VICTIM_CONTROL" 2>/dev/null || true)"
fi

# Wait for HTTP endpoint
for i in {1..50}; do
  if curl -sSf -m 1 "http://$NEW_VICTIM_ADDR/health" >/dev/null 2>&1; then
    break
  fi
  sleep 0.2
done

RESTART_END=$(date +%s)
RESTART_DURATION=$((RESTART_END - RESTART_START))
echo "  Restart complete (${RESTART_DURATION}s)"
echo "  New control address: $NEW_VICTIM_ADDR"
echo ""

# Step 3: Snapshot donor node → manifest
echo "Step 3: Snapshotting donor node..."
SNAPSHOT_START=$(date +%s)

# Call /snapshot endpoint and convert JSON to manifest format
SNAPSHOT_JSON=$(curl -sSf "http://$DONOR_ADDR/snapshot?limit=10000" || echo '{"cids":[],"count":0}')
CID_COUNT=$(echo "$SNAPSHOT_JSON" | jq '.count // 0' 2>/dev/null || echo "0")

# Convert JSON array to manifest format (one CID per line)
echo "$SNAPSHOT_JSON" | jq -r '.cids[]?' > "$MANIFEST_FILE" 2>/dev/null || true

SNAPSHOT_END=$(date +%s)
SNAPSHOT_DURATION=$((SNAPSHOT_END - SNAPSHOT_START))
echo "  Snapshot complete (${SNAPSHOT_DURATION}s, $CID_COUNT CIDs)"
echo ""

# Step 4: Restore from manifest on victim
echo "Step 4: Restoring from manifest on victim..."
RESTORE_START=$(date +%s)

# Use node restore --manifest command
RESTORE_OUTPUT=$("$ROOT_DIR/bin/node" restore --manifest "$MANIFEST_FILE" --control "$VICTIM_CONTROL" 2>&1) || true

RESTORE_END=$(date +%s)
RESTORE_DURATION=$((RESTORE_END - RESTORE_START))

# Extract job ID from output (try multiple patterns)
JOB_ID=$(echo "$RESTORE_OUTPUT" | grep -oP 'job: \K[^\s]+' 2>/dev/null || echo "")
if [[ -z "$JOB_ID" ]]; then
  JOB_ID=$(echo "$RESTORE_OUTPUT" | grep -oP 'Restore job: \K[^\s]+' 2>/dev/null || echo "")
fi

if [[ -n "$JOB_ID" ]]; then
  echo "  Restore job submitted: $JOB_ID"
  
  # Poll restore status
  echo "  Polling restore status..."
  MAX_POLLS=300  # 5 minutes max
  POLL_COUNT=0
  RESTORE_OK=0
  RESTORE_FAILED=0
  RESTORE_BYTES=0
  RESTORE_DONE=false
  
  while [[ $POLL_COUNT -lt $MAX_POLLS ]]; do
    STATUS_JSON=$(curl -sSf "http://$NEW_VICTIM_ADDR/restore/status?id=$JOB_ID" 2>/dev/null || echo '{}')
    if [[ -z "$STATUS_JSON" || "$STATUS_JSON" == "{}" ]]; then
      # Job might not exist yet, continue polling
      sleep 1
      POLL_COUNT=$((POLL_COUNT + 1))
      continue
    fi
    
    RESTORE_DONE=$(echo "$STATUS_JSON" | jq -r '.done // false' 2>/dev/null || echo "false")
    
    if [[ "$RESTORE_DONE" == "true" ]]; then
      RESTORE_OK=$(echo "$STATUS_JSON" | jq -r '.ok // 0' 2>/dev/null || echo "0")
      RESTORE_FAILED=$(echo "$STATUS_JSON" | jq -r '.failed // 0' 2>/dev/null || echo "0")
      RESTORE_BYTES=$(echo "$STATUS_JSON" | jq -r '.bytes // 0' 2>/dev/null || echo "0")
      break
    fi
    
    # Show progress every 10 polls
    if [[ $((POLL_COUNT % 10)) -eq 0 && $POLL_COUNT -gt 0 ]]; then
      echo "    Still polling... ($POLL_COUNT/${MAX_POLLS})" >&2
    fi
    
    sleep 1
    POLL_COUNT=$((POLL_COUNT + 1))
  done
  
  if [[ "$RESTORE_DONE" == "true" ]]; then
    echo "  Restore complete: ok=$RESTORE_OK failed=$RESTORE_FAILED bytes=$RESTORE_BYTES"
  else
    echo "  Restore timeout or incomplete (polled ${POLL_COUNT} times)"
    # Try to get final status anyway
    FINAL_STATUS=$(curl -sSf "http://$NEW_VICTIM_ADDR/restore/status?id=$JOB_ID" 2>/dev/null || echo '{}')
    if [[ -n "$FINAL_STATUS" && "$FINAL_STATUS" != "{}" ]]; then
      RESTORE_OK=$(echo "$FINAL_STATUS" | jq -r '.ok // 0' 2>/dev/null || echo "0")
      RESTORE_FAILED=$(echo "$FINAL_STATUS" | jq -r '.failed // 0' 2>/dev/null || echo "0")
      RESTORE_BYTES=$(echo "$FINAL_STATUS" | jq -r '.bytes // 0' 2>/dev/null || echo "0")
    fi
  fi
else
  echo "  Failed to submit restore job"
  echo "  Restore output: $RESTORE_OUTPUT" >&2
  RESTORE_OK=0
  RESTORE_FAILED=0
  RESTORE_BYTES=0
fi

echo ""

# Step 5: Write results to CSV (always, even on error)
TOTAL_DURATION=$((RESTORE_END - SHUTDOWN_START))

# Ensure output directory exists
mkdir -p "$(dirname "$OUTPUT_CSV")"

# CSV header if file doesn't exist
if [[ ! -f "$OUTPUT_CSV" ]]; then
  echo "run_id,victim_id,donor_id,shutdown_duration_s,restart_duration_s,snapshot_duration_s,restore_duration_s,total_duration_s,cid_count,restore_ok,restore_failed,restore_bytes" > "$OUTPUT_CSV" || {
    echo "Error: Failed to write CSV header" >&2
    exit 1
  }
fi

# Write CSV row
echo "$RUN_ID,$VICTIM_ID,$DONOR_ID,$SHUTDOWN_DURATION,$RESTART_DURATION,$SNAPSHOT_DURATION,$RESTORE_DURATION,$TOTAL_DURATION,$CID_COUNT,$RESTORE_OK,$RESTORE_FAILED,$RESTORE_BYTES" >> "$OUTPUT_CSV" || {
  echo "Error: Failed to write CSV row" >&2
  exit 1
}

echo "Failure/Repair test complete!"
echo "Results written to: $OUTPUT_CSV"
echo ""
echo "Summary:"
echo "  Total duration: ${TOTAL_DURATION}s"
echo "  CIDs restored: $RESTORE_OK"
echo "  Failed: $RESTORE_FAILED"
echo "  Bytes: $RESTORE_BYTES"

# Generate plots if Python script is available
if command -v python3 >/dev/null 2>&1; then
  echo ""
  echo "Generating repair scaling plots..."
  if python3 "$ROOT_DIR/scripts/plots/repair_scaling.py" "$RUN_ID" 2>/dev/null; then
    echo "Plots generated successfully"
  else
    echo "Warning: Failed to generate plots (matplotlib may not be installed)" >&2
  fi
fi


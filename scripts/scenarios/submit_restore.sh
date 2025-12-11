#!/usr/bin/env bash
set -euo pipefail

# Purpose: Stabilize /restore submissions with retries, connection checks, and metrics capture.
# Usage: bash scripts/scenarios/submit_restore.sh <RUN_ID> <BOOTSTRAP_NODE_ID> <CID1> [CID2] [CID3] ...

RUN_ID="${1:-}"
BOOTSTRAP_ID="${2:-}"
shift 2 || true
CIDS=("$@")

if [[ -z "$RUN_ID" || -z "$BOOTSTRAP_ID" || ${#CIDS[@]} -eq 0 ]]; then
  echo "Usage: $0 <RUN_ID> <BOOTSTRAP_NODE_ID> <CID1> [CID2] ..." >&2
  echo "  RUN_ID: directory name under artifacts/runs/" >&2
  echo "  BOOTSTRAP_NODE_ID: node ID of bootstrap (usually 1)" >&2
  echo "  CIDS: one or more CIDs to restore" >&2
  exit 1
fi

NODES_JSON="artifacts/runs/$RUN_ID/nodes.json"
if [[ ! -f "$NODES_JSON" ]]; then
  echo "Error: $NODES_JSON not found" >&2
  exit 1
fi

# Get bootstrap address and peer ID
BOOTSTRAP_ADDR=$(jq -r ".[] | select(.id == $BOOTSTRAP_ID) | .control_addr" "$NODES_JSON")
BOOTSTRAP_PEER=$(curl -sSf "http://$BOOTSTRAP_ADDR/id" | jq -r '.peer')

if [[ -z "$BOOTSTRAP_ADDR" || "$BOOTSTRAP_ADDR" == "null" ]]; then
  echo "Error: Bootstrap node $BOOTSTRAP_ID not found" >&2
  exit 1
fi

echo "Bootstrap: node $BOOTSTRAP_ID ($BOOTSTRAP_ADDR, peer: $BOOTSTRAP_PEER)"
echo "CIDs to restore: ${CIDS[*]}"
echo ""

# Function to check if node has live connection to bootstrap
check_bootstrap_connection() {
  local node_addr="$1"
  local neighbors_json=$(curl -sSf "http://$node_addr/neighbors" 2>/dev/null || echo "[]")
  
  # Check if response is valid JSON array
  if ! echo "$neighbors_json" | jq -e 'type == "array"' >/dev/null 2>&1; then
    return 1
  fi
  
  local has_bootstrap=$(echo "$neighbors_json" | jq -r --arg peer "$BOOTSTRAP_PEER" '.[]? | select(.peer == $peer) | .peer' 2>/dev/null | head -n1)
  if [[ -n "$has_bootstrap" && "$has_bootstrap" != "null" && "$has_bootstrap" != "" ]]; then
    return 0
  fi
  return 1
}

# Function to submit restore with retries
submit_restore_with_retries() {
  local node_addr="$1"
  local max_retries=3
  local retry_delay=2
  
  for attempt in $(seq 1 $max_retries); do
    local req_body=$(jq -n --argjson cids "$(printf '%s\n' "${CIDS[@]}" | jq -R . | jq -s .)" '{
      cids: $cids,
      concurrency: 4,
      timeout: "20s",
      byte_budget: 0
    }')
    
    local resp=$(curl -sSf -w "\n%{http_code}" -X POST \
      -H "Content-Type: application/json" \
      -d "$req_body" \
      "http://$node_addr/restore" 2>/dev/null || echo -e "\n000")
    
    local body=$(echo "$resp" | head -n -1)
    local status_code=$(echo "$resp" | tail -n 1)
    
    if [[ "$status_code" == "202" ]]; then
      local job_id=$(echo "$body" | jq -r '.job')
      if [[ -n "$job_id" && "$job_id" != "null" ]]; then
        echo "$job_id"
        return 0
      fi
    fi
    
    if [[ $attempt -lt $max_retries ]]; then
      echo "  Retry $attempt/$max_retries failed (status: $status_code), waiting ${retry_delay}s..." >&2
      sleep "$retry_delay"
      retry_delay=$((retry_delay * 2))
    fi
  done
  
  echo "Failed after $max_retries attempts" >&2
  return 1
}

# Function to poll restore status with timeout
poll_restore_status() {
  local node_addr="$1"
  local job_id="$2"
  local timeout_s="${3:-300}"
  local interval_s=1
  local start_time=$(date +%s)
  local end_time=$((start_time + timeout_s))
  local last_status=""
  local consecutive_errors=0
  
  while [[ $(date +%s) -lt $end_time ]]; do
    local status_json=$(curl -sSf "http://$node_addr/restore/status?id=$job_id" 2>/dev/null)
    local curl_exit=$?
    
    if [[ $curl_exit -ne 0 || -z "$status_json" ]]; then
      consecutive_errors=$((consecutive_errors + 1))
      if [[ $consecutive_errors -ge 5 ]]; then
        echo ""
        echo "  ❌ ERROR: Failed to get status after 5 consecutive attempts" >&2
        return 1
      fi
      sleep "$interval_s"
      continue
    fi
    
    consecutive_errors=0
    
    # Check if response is valid JSON
    if ! echo "$status_json" | jq empty 2>/dev/null; then
      echo ""
      echo "  WARNING: Invalid JSON response: $status_json" >&2
      sleep "$interval_s"
      continue
    fi
    
    local done=$(echo "$status_json" | jq -r '.done // false' 2>/dev/null)
    local ok=$(echo "$status_json" | jq -r '.ok // 0' 2>/dev/null)
    local failed=$(echo "$status_json" | jq -r '.failed // 0' 2>/dev/null)
    local bytes=$(echo "$status_json" | jq -r '.bytes // 0' 2>/dev/null)
    
    # Check for error response
    if echo "$status_json" | jq -e '.error' >/dev/null 2>&1; then
      local error_msg=$(echo "$status_json" | jq -r '.error // "unknown error"')
      echo ""
      echo "  ERROR: $error_msg" >&2
      return 1
    fi
    
    local current_status="ok=$ok failed=$failed bytes=$bytes done=$done"
    if [[ "$current_status" != "$last_status" ]]; then
      printf "\r  Status: %s" "$current_status" >&2
      last_status="$current_status"
    fi
    
    if [[ "$done" == "true" ]]; then
      echo "" >&2
      echo "$status_json"
      return 0
    fi
    
    sleep "$interval_s"
  done
  
  echo "" >&2
  echo "  Timeout after ${timeout_s}s" >&2
  return 1
}

# Process all leaf nodes (non-bootstrap)
LEAF_NODES=$(jq -r ".[] | select(.id != $BOOTSTRAP_ID) | .id" "$NODES_JSON")
RESULTS_DIR="artifacts/runs/$RUN_ID/restore_results"
mkdir -p "$RESULTS_DIR"

SUCCESS_COUNT=0
FAILED_COUNT=0

for leaf_id in $LEAF_NODES; do
  leaf_addr=$(jq -r ".[] | select(.id == $leaf_id) | .control_addr" "$NODES_JSON")
  
  echo "=== Node $leaf_id ($leaf_addr) ==="
  
  # Check for live connection to bootstrap
  echo "  Checking connection to bootstrap..."
  if ! check_bootstrap_connection "$leaf_addr"; then
    echo "  WARNING: No live connection to bootstrap. Waiting 5s and retrying..."
    sleep 5
    if ! check_bootstrap_connection "$leaf_addr"; then
      echo "  FAILED: Still no connection to bootstrap. Skipping restore."
      FAILED_COUNT=$((FAILED_COUNT + 1))
      continue
    fi
  fi
  echo "  Connected to bootstrap"
  
  # Submit restore with retries
  echo "  Submitting restore job..."
  job_id=$(submit_restore_with_retries "$leaf_addr")
  if [[ -z "$job_id" ]]; then
    echo "  FAILED: Could not submit restore job"
    FAILED_COUNT=$((FAILED_COUNT + 1))
    continue
  fi
  echo "  Job submitted: $job_id"
  
  # Poll status until done
  echo "  Polling restore status..."
  echo "  (Check status manually: curl -s \"http://$leaf_addr/restore/status?id=$job_id\" | jq .)"
  status_json=$(poll_restore_status "$leaf_addr" "$job_id" 300)
  poll_exit=$?
  
  if [[ $poll_exit -ne 0 ]]; then
    echo "  Polling failed or timed out. Checking final status..."
    final_status=$(curl -sSf "http://$leaf_addr/restore/status?id=$job_id" 2>/dev/null || echo "{}")
    echo "  Final status: $final_status"
    
    # Check if job exists
    if echo "$final_status" | jq -e '.error' >/dev/null 2>&1; then
      echo "  FAILED: $(echo "$final_status" | jq -r '.error')"
      else
        final_done=$(echo "$final_status" | jq -r '.done // false' 2>/dev/null || echo "false")
        if [[ "$final_done" == "true" ]]; then
          echo "  Restore actually completed!"
          status_json="$final_status"
        else
          echo "  FAILED: Restore did not complete (done=$final_done)"
        fi
      fi
    
    if [[ "$status_json" == "" ]]; then
      FAILED_COUNT=$((FAILED_COUNT + 1))
      continue
    fi
  fi
  
  # Validate status_json is valid JSON before parsing
  if ! echo "$status_json" | jq empty >/dev/null 2>&1; then
    echo "  FAILED: Invalid status JSON: $status_json"
    FAILED_COUNT=$((FAILED_COUNT + 1))
    continue
  fi
  
  ok=$(echo "$status_json" | jq -r '.ok // 0' 2>/dev/null || echo "0")
  failed=$(echo "$status_json" | jq -r '.failed // 0' 2>/dev/null || echo "0")
  bytes=$(echo "$status_json" | jq -r '.bytes // 0' 2>/dev/null || echo "0")
  done=$(echo "$status_json" | jq -r '.done // false' 2>/dev/null || echo "false")
  
  echo "  Restore completed: ok=$ok failed=$failed bytes=$bytes done=$done"
  
  # Capture metrics snapshot right after completion
  echo "  Capturing metrics snapshot..."
  metrics_json=$(curl -sSf "http://$leaf_addr/metrics" 2>/dev/null || echo "{}")
  result_file="$RESULTS_DIR/node_${leaf_id}_restore.json"
  echo "$metrics_json" | jq -c ". + {
    node_id: $leaf_id,
    job_id: \"$job_id\",
    restore_status: $status_json,
    completed_at: $(date +%s)
  }" > "$result_file"
  echo "  Metrics saved to $result_file"
  
  SUCCESS_COUNT=$((SUCCESS_COUNT + 1))
  echo ""
done

# Summary
echo "=== Summary ==="
echo "Successful restores: $SUCCESS_COUNT"
echo "Failed restores: $FAILED_COUNT"
echo "Results saved to: $RESULTS_DIR"

if [[ $SUCCESS_COUNT -gt 0 ]]; then
  echo ""
  echo "All successful restores completed and metrics captured"
fi

if [[ $FAILED_COUNT -gt 0 ]]; then
  echo ""
  echo "  Some restores failed - check logs above"
  exit 1
fi


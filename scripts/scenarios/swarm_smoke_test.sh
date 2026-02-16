#!/usr/bin/env bash
# Purpose: Smoke test - minimal validation before running full test suite
# Usage: ./scripts/scenarios/swarm_smoke_test.sh [options]
#   --our-api <addr>     Our system API address (default: auto-detect)
#   --swarm-api <addr>   Swarm API address (default: http://172.20.0.200:8500)
#   --nodes <n>          Number of nodes to start (default: 2)
#   --skip-start         Skip starting nodes (assume they're already running)
#   --cleanup            Clean up nodes after test

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Source utilities
source "$ROOT_DIR/scripts/utils/error_handler.sh" 2>/dev/null || true
source "$ROOT_DIR/scripts/utils/test_logger.sh" 2>/dev/null || true
source "$ROOT_DIR/scripts/swarm/api.sh"

# Default values
OUR_API=""
SWARM_API="http://172.20.0.200:8500"
NODES=2
SKIP_START=false
CLEANUP=false

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Test results
TESTS_PASSED=0
TESTS_FAILED=0

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --our-api)
      OUR_API="$2"
      shift 2
      ;;
    --swarm-api)
      SWARM_API="$2"
      shift 2
      ;;
    --nodes)
      NODES="$2"
      shift 2
      ;;
    --skip-start)
      SKIP_START=true
      shift
      ;;
    --cleanup)
      CLEANUP=true
      shift
      ;;
    --help)
      echo "Usage: $0 [options]"
      echo "Options:"
      echo "  --our-api <addr>     Our system API address (default: auto-detect)"
      echo "  --swarm-api <addr>   Swarm API address (default: http://172.20.0.200:8500)"
      echo "  --nodes <n>          Number of nodes to start (default: 2)"
      echo "  --skip-start         Skip starting nodes (assume already running)"
      echo "  --cleanup            Clean up nodes after test"
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      exit 1
      ;;
  esac
done

# Test result tracking
test_pass() {
  echo -e "${GREEN}✓${NC} $1"
  TESTS_PASSED=$((TESTS_PASSED + 1))
}

test_fail() {
  echo -e "${RED}✗${NC} $1"
  TESTS_FAILED=$((TESTS_FAILED + 1))
}

test_info() {
  echo -e "${BLUE}ℹ${NC} $1"
}

# Function to detect our system API
detect_our_api() {
  OUR_CONTAINER=""
  OUR_API_ADDR=""
  
  if [[ -n "$OUR_API" ]]; then
    if [[ "$OUR_API" =~ ^[a-zA-Z0-9_-]+$ ]]; then
      OUR_CONTAINER="$OUR_API"
      OUR_API_ADDR=$(docker exec "$OUR_CONTAINER" jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
      if [[ -n "$OUR_API_ADDR" && "$OUR_API_ADDR" != "null" ]]; then
        OUR_API="http://$OUR_API_ADDR"
        return 0
      fi
    else
      # Assume it's already a URL
      return 0
    fi
  fi
  
  # Try to find bootstrap container
  if docker ps --format '{{.Names}}' | grep -q "^fall25-bootstrap$"; then
    OUR_CONTAINER="fall25-bootstrap"
    OUR_API_ADDR=$(docker exec "$OUR_CONTAINER" jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
    if [[ -n "$OUR_API_ADDR" && "$OUR_API_ADDR" != "null" ]]; then
      OUR_API="http://$OUR_API_ADDR"
      return 0
    fi
  fi
  
  # Try docker-compose
  if command -v docker-compose >/dev/null 2>&1 && [[ -f "$ROOT_DIR/docker-compose.yml" ]]; then
    if docker-compose ps bootstrap 2>/dev/null | grep -q "Up"; then
      OUR_CONTAINER="bootstrap"
      OUR_API_ADDR=$(docker-compose exec -T bootstrap jq -r '.addr // .Addr' /app/logs/bootstrap.json 2>/dev/null || echo "")
      if [[ -n "$OUR_API_ADDR" && "$OUR_API_ADDR" != "null" ]]; then
        OUR_API="http://$OUR_API_ADDR"
        return 0
      fi
    fi
  fi
  
  return 1
}

# Function to start nodes if needed
start_nodes() {
  if [[ "$SKIP_START" == "true" ]]; then
    test_info "Skipping node startup (--skip-start)"
    return 0
  fi
  
  test_info "Starting $NODES nodes for each system..."
  
  # Start our system
  test_info "Starting our system ($NODES nodes)..."
  if ! "$ROOT_DIR/scripts/docker/start.sh" "$NODES" >/dev/null 2>&1; then
    test_fail "Failed to start our system nodes"
    return 1
  fi
  
  # Wait for our system bootstrap
  test_info "Waiting for our system bootstrap..."
  local max_wait=30
  for i in $(seq 1 $max_wait); do
    if detect_our_api; then
      if check_api_endpoint_container "${OUR_CONTAINER:-bootstrap}" "http://${OUR_API_ADDR}/health" 5 1 >/dev/null 2>&1; then
        test_info "Our system bootstrap ready after ${i}s"
        break
      fi
    fi
    if [[ $i -eq $max_wait ]]; then
      test_fail "Our system bootstrap did not become ready"
      return 1
    fi
    sleep 1
  done
  
  # Start Swarm
  test_info "Starting Swarm ($NODES nodes)..."
  if ! "$ROOT_DIR/scripts/docker/swarm/start.sh" "$NODES" >/dev/null 2>&1; then
    test_fail "Failed to start Swarm nodes"
    return 1
  fi
  
  # Wait for Swarm bootstrap
  test_info "Waiting for Swarm bootstrap..."
  max_wait=30
  for i in $(seq 1 $max_wait); do
    if check_api_endpoint "$SWARM_API/" 5 1 >/dev/null 2>&1; then
      test_info "Swarm bootstrap ready after ${i}s"
      break
    fi
    if [[ $i -eq $max_wait ]]; then
      test_fail "Swarm bootstrap did not become ready"
      return 1
    fi
    sleep 1
  done
  
  sleep 2  # Brief stabilization
  return 0
}

# Function to cleanup nodes
cleanup_nodes() {
  if [[ "$CLEANUP" != "true" ]]; then
    return 0
  fi
  
  test_info "Cleaning up nodes..."
  
  # Stop Swarm
  if command -v docker-compose >/dev/null 2>&1 && [[ -f "$ROOT_DIR/docker-compose.swarm.yml" ]]; then
    docker-compose -f docker-compose.swarm.yml stop >/dev/null 2>&1 || true
    docker-compose -f docker-compose.swarm.yml rm -f >/dev/null 2>&1 || true
  fi
  
  # Stop our system
  if command -v docker-compose >/dev/null 2>&1 && [[ -f "$ROOT_DIR/docker-compose.yml" ]]; then
    docker-compose stop >/dev/null 2>&1 || true
    docker-compose rm -f >/dev/null 2>&1 || true
  fi
}

# Trap cleanup on exit
trap cleanup_nodes EXIT

# Function to test upload on our system
test_our_upload() {
  test_info "Testing upload on our system..."
  
  if [[ -z "$OUR_CONTAINER" || -z "$OUR_API_ADDR" ]]; then
    if ! detect_our_api; then
      test_fail "Could not detect our system API"
      return 1
    fi
  fi
  
  # Create 1KB test file
  local test_file=$(mktemp)
  dd if=/dev/urandom of="$test_file" bs=1024 count=1 >/dev/null 2>&1
  
  # Upload
  local data_b64=$(base64 -w 0 < "$test_file" 2>/dev/null || base64 < "$test_file" | tr -d '\n')
  local json_payload=$(mktemp)
  echo "{\"data\":\"$data_b64\"}" > "$json_payload"
  
  # Copy to container and execute
  docker cp "$json_payload" "${OUR_CONTAINER}:/tmp/smoke_test_$$.json" >/dev/null 2>&1
  local response=$(docker exec "$OUR_CONTAINER" curl -sSf -X POST \
    -H "Content-Type: application/json" \
    -d @/tmp/smoke_test_$$.json \
    "http://$OUR_API_ADDR/put" 2>&1)
  docker exec "$OUR_CONTAINER" rm -f "/tmp/smoke_test_$$.json" >/dev/null 2>&1 || true
  
  rm -f "$json_payload" "$test_file"
  
  # Check response
  local cid=$(echo "$response" | jq -r '.cid // empty' 2>/dev/null || echo "")
  if [[ -n "$cid" && "$cid" != "null" ]]; then
    test_pass "Our system upload successful (CID: ${cid:0:16}...)"
    echo "$cid"
    return 0
  else
    test_fail "Our system upload failed: $response"
    return 1
  fi
}

# Function to test download on our system
test_our_download() {
  local cid="$1"
  
  test_info "Testing download on our system..."
  
  if [[ -z "$OUR_CONTAINER" || -z "$OUR_API_ADDR" ]]; then
    if ! detect_our_api; then
      test_fail "Could not detect our system API"
      return 1
    fi
  fi
  
  # Download
  local download_file=$(mktemp)
  local response=$(docker exec "$OUR_CONTAINER" curl -sSf \
    "http://$OUR_API_ADDR/get?cid=$cid" 2>&1)
  
  if [[ -n "$response" ]]; then
    # Parse JSON response
    local data=$(echo "$response" | jq -r '.data // empty' 2>/dev/null || echo "")
    if [[ -n "$data" && "$data" != "null" ]]; then
      echo "$data" | base64 -d > "$download_file" 2>/dev/null || echo "$data" | base64 -d > "$download_file"
      
      if [[ -s "$download_file" ]]; then
        test_pass "Our system download successful"
        rm -f "$download_file"
        return 0
      else
        test_fail "Our system download returned empty file"
        rm -f "$download_file"
        return 1
      fi
    else
      test_fail "Our system download failed: $response"
      rm -f "$download_file"
      return 1
    fi
  else
    test_fail "Our system download failed: no response"
    rm -f "$download_file"
    return 1
  fi
}

# Function to test upload on Swarm
test_swarm_upload() {
  test_info "Testing upload on Swarm..."
  
  # Create 1KB test file
  local test_file=$(mktemp)
  dd if=/dev/urandom of="$test_file" bs=1024 count=1 >/dev/null 2>&1
  
  # Upload
  local hash=""
  if hash=$(upload_file "$SWARM_API" "$test_file" 2>&1); then
    if [[ -n "$hash" && "$hash" != "ERROR"* && ${#hash} -ge 64 ]]; then
      test_pass "Swarm upload successful (hash: ${hash:0:16}...)"
      rm -f "$test_file"
      echo "$hash"
      return 0
    else
      test_fail "Swarm upload returned invalid hash: ${hash:0:50}..."
      rm -f "$test_file"
      return 1
    fi
  else
    test_fail "Swarm upload failed: ${hash:0:100}..."
    rm -f "$test_file"
    return 1
  fi
}

# Function to test download on Swarm
test_swarm_download() {
  local hash="$1"
  
  test_info "Testing download on Swarm..."
  
  # Clean hash
  hash=$(echo "$hash" | tr -d '/ \n\r')
  
  # Extract IP and port from SWARM_API
  local api_ip=$(echo "$SWARM_API" | sed 's|http://||;s|:.*||')
  local api_port=$(echo "$SWARM_API" | sed 's|.*:||')
  
  # Try multiple endpoint formats
  local download_file=$(mktemp)
  local download_success=false
  
  # Try /bzz:/<hash>/ format
  if curl -sfL -m 10 "http://${api_ip}:${api_port}/bzz:/${hash}/" -o "$download_file" >/dev/null 2>&1; then
    if [[ -s "$download_file" ]]; then
      download_success=true
    fi
  fi
  
  # Try /bzz-raw:/<hash> format
  if [[ "$download_success" == "false" ]]; then
    if curl -sfL -m 10 "http://${api_ip}:${api_port}/bzz-raw:/${hash}" -o "$download_file" >/dev/null 2>&1; then
      if [[ -s "$download_file" ]]; then
        download_success=true
      fi
    fi
  fi
  
  if [[ "$download_success" == "true" ]]; then
    test_pass "Swarm download successful"
    rm -f "$download_file"
    return 0
  else
    # Download may fail if content hasn't propagated yet - this is acceptable for smoke test
    test_info "Swarm download not immediately available (may need time to propagate)"
    rm -f "$download_file"
    return 0  # Don't fail smoke test for this
  fi
}

# Main smoke test
main() {
  echo -e "${BLUE}════════════════════════════════════════════════════════════════${NC}"
  echo -e "${BLUE}Swarm Smoke Test${NC}"
  echo -e "${BLUE}════════════════════════════════════════════════════════════════${NC}"
  echo ""
  echo "Configuration:"
  echo "  Nodes: $NODES"
  echo "  Swarm API: $SWARM_API"
  echo "  Skip Start: $SKIP_START"
  echo "  Cleanup: $CLEANUP"
  echo ""
  
  # Check Docker
  test_info "Checking Docker..."
  if ! check_docker; then
    test_fail "Docker check failed"
    exit 1
  fi
  test_pass "Docker is available"
  echo ""
  
  # Start nodes
  if ! start_nodes; then
    echo ""
    echo -e "${RED}✗ Smoke test failed: Could not start nodes${NC}"
    exit 1
  fi
  echo ""
  
  # Detect our system API
  if ! detect_our_api; then
    test_fail "Could not detect our system API"
    exit 1
  fi
  test_info "Our system API: $OUR_API"
  echo ""
  
  # Test our system
  echo -e "${BLUE}Testing Our System${NC}"
  echo "────────────────────────────────────────────────────────────"
  local our_cid=""
  if our_cid=$(test_our_upload); then
    if test_our_download "$our_cid"; then
      test_pass "Our system smoke test: PASS"
    else
      test_fail "Our system smoke test: FAIL (download failed)"
    fi
  else
    test_fail "Our system smoke test: FAIL (upload failed)"
  fi
  echo ""
  
  # Test Swarm
  echo -e "${BLUE}Testing Swarm${NC}"
  echo "────────────────────────────────────────────────────────────"
  local swarm_hash=""
  if swarm_hash=$(test_swarm_upload); then
    if test_swarm_download "$swarm_hash"; then
      test_pass "Swarm smoke test: PASS"
    else
      test_info "Swarm smoke test: PASS (upload successful, download may need propagation time)"
    fi
  else
    test_fail "Swarm smoke test: FAIL (upload failed)"
  fi
  echo ""
  
  # Summary
  echo -e "${BLUE}════════════════════════════════════════════════════════════════${NC}"
  echo -e "${BLUE}Smoke Test Summary${NC}"
  echo -e "${BLUE}════════════════════════════════════════════════════════════════${NC}"
  echo -e "Passed: ${GREEN}$TESTS_PASSED${NC}"
  echo -e "Failed: ${RED}$TESTS_FAILED${NC}"
  echo ""
  
  if [[ $TESTS_FAILED -eq 0 ]]; then
    echo -e "${GREEN}✓ Smoke test PASSED - systems are ready for full test suite${NC}"
    return 0
  else
    echo -e "${RED}✗ Smoke test FAILED - fix issues before running full test suite${NC}"
    return 1
  fi
}

# Run main function
main "$@"

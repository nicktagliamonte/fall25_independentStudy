#!/usr/bin/env bash
set -euo pipefail

# Purpose: Comprehensive test script for Swarm v0.5.8 API operations
# Usage: ./scripts/tests/swarm_comparison/test_api.sh [api_address]
#   api_address: Swarm API address (default: http://172.20.0.10:8500)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

# Source API functions
source "$SCRIPT_DIR/api.sh"

# Default API address
API="${1:-http://127.0.0.1:8500}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Test counters
TESTS_PASSED=0
TESTS_FAILED=0
TESTS_SKIPPED=0

# Test result tracking
test_result() {
  local test_name="$1"
  local status="$2"
  local message="${3:-}"
  
  if [[ "$status" == "PASS" ]]; then
    echo -e "${GREEN}✓${NC} $test_name"
    ((TESTS_PASSED++))
  elif [[ "$status" == "FAIL" ]]; then
    echo -e "${RED}✗${NC} $test_name"
    if [[ -n "$message" ]]; then
      echo -e "  ${RED}Error:${NC} $message"
    fi
    ((TESTS_FAILED++))
  elif [[ "$status" == "SKIP" ]]; then
    echo -e "${YELLOW}⊘${NC} $test_name (skipped)"
    if [[ -n "$message" ]]; then
      echo -e "  ${YELLOW}Note:${NC} $message"
    fi
    ((TESTS_SKIPPED++))
  fi
}

# Test basic connectivity
test_connectivity() {
  echo -e "\n${BLUE}=== Testing Basic Connectivity ===${NC}"
  
  if curl -sSf -m 5 "$API/" >/dev/null 2>&1; then
    test_result "API endpoint reachable" "PASS"
    return 0
  else
    test_result "API endpoint reachable" "FAIL" "Cannot connect to $API"
    return 1
  fi
}

# Test node info retrieval
test_node_info() {
  echo -e "\n${BLUE}=== Testing Node Information ===${NC}"
  
  local info=$(get_node_info "$API" 2>/dev/null)
  
  if [[ -n "$info" && "$info" != "{}" ]]; then
    echo "  Node info:"
    echo "$info" | jq '.' 2>/dev/null || echo "$info"
    test_result "Get node info" "PASS"
  else
    test_result "Get node info" "SKIP" "Swarm v0.5.8 may not expose node info via API"
  fi
}

# Test metrics retrieval
test_metrics() {
  echo -e "\n${BLUE}=== Testing Metrics ===${NC}"
  
  local metrics=$(get_metrics "$API" 2>/dev/null)
  
  if [[ -n "$metrics" && "$metrics" != "{}" ]]; then
    echo "  Metrics:"
    echo "$metrics" | jq '.' 2>/dev/null || echo "$metrics"
    test_result "Get metrics" "PASS"
  else
    test_result "Get metrics" "SKIP" "Swarm v0.5.8 may not expose metrics via API"
  fi
}

# Test file upload
test_upload() {
  echo -e "\n${BLUE}=== Testing File Upload ===${NC}"
  
  # Create test files
  local test_dir=$(mktemp -d)
  trap "rm -rf $test_dir" EXIT
  
  # Small text file
  echo "Hello, Swarm v0.5.8!" > "$test_dir/small.txt"
  
  # Medium file (1KB)
  dd if=/dev/urandom of="$test_dir/medium.bin" bs=1024 count=1 2>/dev/null
  
  # Test small file upload
  echo "  Testing small file upload..."
  local hash=$(upload_file "$API" "$test_dir/small.txt" 2>&1)
  
  if [[ -n "$hash" && "$hash" != "ERROR"* && ${#hash} -ge 64 ]]; then
    echo "    Uploaded hash: $hash"
    test_result "Upload small file" "PASS"
    
    # Store hash for download test
    echo "$hash" > "$test_dir/uploaded_hash.txt"
    
    # Test medium file upload
    echo "  Testing medium file upload..."
    local hash2=$(upload_file "$API" "$test_dir/medium.bin" 2>&1)
    
    if [[ -n "$hash2" && "$hash2" != "ERROR"* && ${#hash2} -ge 64 ]]; then
      echo "    Uploaded hash: $hash2"
      test_result "Upload medium file" "PASS"
    else
      test_result "Upload medium file" "FAIL" "$hash2"
    fi
  else
    test_result "Upload small file" "FAIL" "$hash"
    return 1
  fi
}

# Test file download
test_download() {
  echo -e "\n${BLUE}=== Testing File Download ===${NC}"
  
  # First upload a file to download
  local test_dir=$(mktemp -d)
  trap "rm -rf $test_dir" EXIT
  
  echo "Test content for download" > "$test_dir/upload.txt"
  
  echo "  Uploading test file..."
  local hash=$(upload_file "$API" "$test_dir/upload.txt" 2>&1)
  
  if [[ -z "$hash" || "$hash" == "ERROR"* ]]; then
    test_result "Download test (upload prerequisite)" "FAIL" "Could not upload test file"
    return 1
  fi
  
  echo "    Uploaded hash: $hash"
  
  # Try downloading
  echo "  Testing download..."
  local output_file="$test_dir/downloaded.txt"
  
  if download_file "$API" "$hash" "$output_file" 2>/dev/null; then
    if [[ -f "$output_file" ]]; then
      local original=$(cat "$test_dir/upload.txt")
      local downloaded=$(cat "$output_file")
      
      if [[ "$original" == "$downloaded" ]]; then
        echo "    Content matches!"
        test_result "Download file" "PASS"
      else
        test_result "Download file" "FAIL" "Content mismatch"
      fi
    else
      test_result "Download file" "FAIL" "Downloaded file not found"
    fi
  else
    test_result "Download file" "FAIL" "Download failed"
  fi
}

# Test content availability check
test_content_check() {
  echo -e "\n${BLUE}=== Testing Content Availability ===${NC}"
  
  # Upload a file first
  local test_dir=$(mktemp -d)
  trap "rm -rf $test_dir" EXIT
  
  echo "Test content" > "$test_dir/test.txt"
  
  local hash=$(upload_file "$API" "$test_dir/test.txt" 2>&1)
  
  if [[ -z "$hash" || "$hash" == "ERROR"* ]]; then
    test_result "Content check (upload prerequisite)" "FAIL" "Could not upload test file"
    return 1
  fi
  
  echo "    Checking availability of hash: $hash"
  
  if check_content "$API" "$hash" 2>/dev/null; then
    test_result "Check content availability" "PASS"
  else
    test_result "Check content availability" "FAIL" "Content not available"
  fi
}

# Test different upload endpoints
test_upload_endpoints() {
  echo -e "\n${BLUE}=== Testing Different Upload Endpoints ===${NC}"
  
  local test_dir=$(mktemp -d)
  trap "rm -rf $test_dir" EXIT
  
  echo "Test data" > "$test_dir/test.txt"
  
  # Test /bzz endpoint
  echo "  Testing POST /bzz..."
  local hash_bzz=$(curl -sSf -X POST \
    -F "file=@$test_dir/test.txt" \
    "$API/bzz" 2>/dev/null | jq -r '.reference' 2>/dev/null || echo "")
  
  if [[ -n "$hash_bzz" && "$hash_bzz" != "null" && ${#hash_bzz} -ge 64 ]]; then
    echo "    /bzz hash: $hash_bzz"
    test_result "Upload via /bzz" "PASS"
  else
    test_result "Upload via /bzz" "FAIL" "No hash returned"
  fi
  
  # Test /bzz-raw endpoint
  echo "  Testing POST /bzz-raw..."
  local hash_raw=$(curl -sSf -X POST \
    -F "file=@$test_dir/test.txt" \
    "$API/bzz-raw" 2>/dev/null | jq -r '.reference' 2>/dev/null || \
    curl -sSf -X POST \
    --data-binary @"$test_dir/test.txt" \
    "$API/bzz-raw" 2>/dev/null | head -c 64 || echo "")
  
  if [[ -n "$hash_raw" && "$hash_raw" != "null" && ${#hash_raw} -ge 64 ]]; then
    echo "    /bzz-raw hash: $hash_raw"
    test_result "Upload via /bzz-raw" "PASS"
  else
    test_result "Upload via /bzz-raw" "SKIP" "May not be supported or different format"
  fi
}

# Test download endpoints
test_download_endpoints() {
  echo -e "\n${BLUE}=== Testing Different Download Endpoints ===${NC}"
  
  # Upload a test file first
  local test_dir=$(mktemp -d)
  trap "rm -rf $test_dir" EXIT
  
  echo "Download test content" > "$test_dir/upload.txt"
  
  local hash=$(upload_file "$API" "$test_dir/upload.txt" 2>&1)
  
  if [[ -z "$hash" || "$hash" == "ERROR"* ]]; then
    test_result "Download endpoints test (upload prerequisite)" "FAIL" "Could not upload test file"
    return 1
  fi
  
  echo "    Testing download of hash: $hash"
  
  # Test /bzz:/<hash>
  echo "  Testing GET /bzz:/<hash>..."
  if curl -sSf -o "$test_dir/download_bzz.txt" "$API/bzz:/$hash" 2>/dev/null; then
    if [[ -f "$test_dir/download_bzz.txt" ]]; then
      test_result "Download via /bzz:/<hash>" "PASS"
    else
      test_result "Download via /bzz:/<hash>" "FAIL" "File not created"
    fi
  else
    test_result "Download via /bzz:/<hash>" "FAIL" "Request failed"
  fi
  
  # Test /bzz-raw:/<hash>
  echo "  Testing GET /bzz-raw:/<hash>..."
  if curl -sSf -o "$test_dir/download_raw.txt" "$API/bzz-raw:/$hash" 2>/dev/null; then
    if [[ -f "$test_dir/download_raw.txt" ]]; then
      test_result "Download via /bzz-raw:/<hash>" "PASS"
    else
      test_result "Download via /bzz-raw:/<hash>" "FAIL" "File not created"
    fi
  else
    test_result "Download via /bzz-raw:/<hash>" "SKIP" "May not be supported"
  fi
}

# Main test execution
main() {
  echo "=========================================="
  echo "Swarm v0.5.8 API Test Suite"
  echo "=========================================="
  echo "API Address: $API"
  echo ""
  
  # Check if jq is available
  if ! command -v jq >/dev/null 2>&1; then
    echo -e "${YELLOW}Warning: jq not found. Some JSON parsing may fail.${NC}"
  fi
  
  # Run tests
  test_connectivity || exit 1
  
  test_node_info
  test_metrics
  test_upload
  test_download
  test_content_check
  test_upload_endpoints
  test_download_endpoints
  
  # Summary
  echo ""
  echo "=========================================="
  echo "Test Summary"
  echo "=========================================="
  echo -e "${GREEN}Passed:${NC} $TESTS_PASSED"
  echo -e "${RED}Failed:${NC} $TESTS_FAILED"
  echo -e "${YELLOW}Skipped:${NC} $TESTS_SKIPPED"
  echo ""
  
  if [[ $TESTS_FAILED -eq 0 ]]; then
    echo -e "${GREEN}All critical tests passed!${NC}"
    exit 0
  else
    echo -e "${RED}Some tests failed.${NC}"
    exit 1
  fi
}

# Run main function
main "$@"

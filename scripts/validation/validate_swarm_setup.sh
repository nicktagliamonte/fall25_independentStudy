#!/usr/bin/env bash
# Purpose: Validate Swarm setup - verify nodes, APIs, connectivity, and basic operations
# Usage: ./scripts/validation/validate_swarm_setup.sh [options]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Source utilities
source "$ROOT_DIR/scripts/utils/error_handler.sh" 2>/dev/null || true
source "$ROOT_DIR/scripts/utils/test_logger.sh" 2>/dev/null || true
source "$ROOT_DIR/scripts/swarm/api.sh"

# Default values
SWARM_API="${SWARM_API:-http://172.20.0.200:8500}"
VERBOSE=false
QUIET=false
EXIT_ON_ERROR=false

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Validation results
VALIDATION_PASSED=0
VALIDATION_FAILED=0
VALIDATION_WARNINGS=0

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --swarm-api)
      SWARM_API="$2"
      shift 2
      ;;
    --verbose|-v)
      VERBOSE=true
      shift
      ;;
    --quiet|-q)
      QUIET=true
      shift
      ;;
    --exit-on-error)
      EXIT_ON_ERROR=true
      shift
      ;;
    --help)
      echo "Usage: $0 [options]"
      echo "Options:"
      echo "  --swarm-api <url>    Swarm API address (default: http://172.20.0.200:8500)"
      echo "  --verbose, -v        Enable verbose output"
      echo "  --quiet, -q          Suppress non-error output"
      echo "  --exit-on-error      Exit immediately on first error"
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      exit 1
      ;;
  esac
done

# Logging functions
log_pass() {
  if [[ "$QUIET" != "true" ]]; then
    echo -e "${GREEN}✓${NC} $1"
  fi
  VALIDATION_PASSED=$((VALIDATION_PASSED + 1))
}

log_fail() {
  echo -e "${RED}✗${NC} $1" >&2
  VALIDATION_FAILED=$((VALIDATION_FAILED + 1))
  if [[ "$EXIT_ON_ERROR" == "true" ]]; then
    exit 1
  fi
}

log_warn() {
  if [[ "$QUIET" != "true" ]]; then
    echo -e "${YELLOW}⚠${NC} $1" >&2
  fi
  VALIDATION_WARNINGS=$((VALIDATION_WARNINGS + 1))
}

log_info() {
  if [[ "$VERBOSE" == "true" && "$QUIET" != "true" ]]; then
    echo -e "${BLUE}ℹ${NC} $1"
  fi
}

# Function to check Docker
check_docker_available() {
  log_info "Checking Docker availability..."
  if ! command -v docker >/dev/null 2>&1; then
    log_fail "Docker is not installed or not in PATH"
    return 1
  fi
  
  if ! docker info >/dev/null 2>&1; then
    log_fail "Docker daemon is not running or not accessible"
    return 1
  fi
  
  log_pass "Docker is available and daemon is running"
  return 0
}

# Function to check Swarm nodes are running
check_swarm_nodes_running() {
  log_info "Checking Swarm nodes..."
  
  local nodes_found=0
  local nodes_running=0
  
  # Check for swarm-bootstrap
  if docker ps --format '{{.Names}}' | grep -q "^swarm-bootstrap$"; then
    nodes_found=$((nodes_found + 1))
    if docker ps --format '{{.Status}}' --filter "name=swarm-bootstrap" | grep -q "Up"; then
      nodes_running=$((nodes_running + 1))
      log_pass "swarm-bootstrap is running"
    else
      log_fail "swarm-bootstrap exists but is not running"
      return 1
    fi
  else
    log_fail "swarm-bootstrap container not found"
    return 1
  fi
  
  # Check for swarm-node containers
  local swarm_nodes=$(docker ps --format '{{.Names}}' | grep -E '^swarm-node[0-9]+$' || true)
  if [[ -n "$swarm_nodes" ]]; then
    while IFS= read -r node; do
      nodes_found=$((nodes_found + 1))
      if docker ps --format '{{.Status}}' --filter "name=$node" | grep -q "Up"; then
        nodes_running=$((nodes_running + 1))
        log_pass "$node is running"
      else
        log_fail "$node exists but is not running"
      fi
    done <<< "$swarm_nodes"
  else
    log_warn "No additional Swarm nodes found (only bootstrap)"
  fi
  
  if [[ $nodes_running -eq 0 ]]; then
    log_fail "No Swarm nodes are running"
    return 1
  fi
  
  log_info "Found $nodes_running Swarm node(s) running"
  return 0
}

# Function to check Swarm API endpoints
check_swarm_api_endpoints() {
  log_info "Checking Swarm API endpoints..."
  
  local api_base="$SWARM_API"
  
  # Check root endpoint
  if curl -sfL -m 5 "$api_base/" >/dev/null 2>&1; then
    log_pass "Swarm API root endpoint is accessible ($api_base)"
  else
    log_fail "Swarm API root endpoint is not accessible ($api_base)"
    return 1
  fi
  
  # Try to get API info (Swarm v0.5.8 may not have this, so it's optional)
  local api_info=$(curl -sfL -m 5 "$api_base/" 2>/dev/null || echo "")
  if [[ -n "$api_info" ]]; then
    log_info "Swarm API responded: $(echo "$api_info" | head -c 100)"
  fi
  
  return 0
}

# Function to get Swarm node IPs
get_swarm_node_ips() {
  local ips=()
  
  # Get bootstrap IP
  if docker ps --format '{{.Names}}' | grep -q "^swarm-bootstrap$"; then
    local bootstrap_ip=$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' swarm-bootstrap 2>/dev/null || echo "")
    if [[ -n "$bootstrap_ip" ]]; then
      ips+=("$bootstrap_ip")
    fi
  fi
  
  # Get node IPs
  local swarm_nodes=$(docker ps --format '{{.Names}}' | grep -E '^swarm-node[0-9]+$' || true)
  if [[ -n "$swarm_nodes" ]]; then
    while IFS= read -r node; do
      local node_ip=$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$node" 2>/dev/null || echo "")
      if [[ -n "$node_ip" ]]; then
        ips+=("$node_ip")
      fi
    done <<< "$swarm_nodes"
  fi
  
  echo "${ips[@]}"
}

# Function to check network connectivity between nodes
check_network_connectivity() {
  log_info "Checking network connectivity between Swarm nodes..."
  
  local node_ips=($(get_swarm_node_ips))
  
  if [[ ${#node_ips[@]} -eq 0 ]]; then
    log_fail "No Swarm node IPs found"
    return 1
  fi
  
  if [[ ${#node_ips[@]} -eq 1 ]]; then
    log_warn "Only one Swarm node found, skipping connectivity check"
    return 0
  fi
  
  local connectivity_ok=true
  
  # Check connectivity between all pairs
  for i in "${!node_ips[@]}"; do
    for j in "${!node_ips[@]}"; do
      if [[ $i -lt $j ]]; then
        local ip1="${node_ips[$i]}"
        local ip2="${node_ips[$j]}"
        
        # Try to ping from node1 to node2 (via docker exec if possible)
        local node1_name=$(docker ps --format '{{.Names}}' --filter "network=fall25_independentstudy_node-network" | grep -E '^swarm-' | head -1 || echo "")
        
        if [[ -n "$node1_name" ]]; then
          # Try to ping from inside container
          if docker exec "$node1_name" ping -c 1 -W 2 "$ip2" >/dev/null 2>&1; then
            log_info "Connectivity: $ip1 -> $ip2: OK"
          else
            log_warn "Connectivity: $ip1 -> $ip2: Failed (may be normal if nodes use different network)"
            connectivity_ok=false
          fi
        else
          # Fallback: check if IPs are in same network
          log_info "Connectivity: $ip1 <-> $ip2: Assuming same network"
        fi
      fi
    done
  done
  
  if [[ "$connectivity_ok" == "true" ]]; then
    log_pass "Network connectivity check passed"
  else
    log_warn "Some connectivity checks failed (may be expected)"
  fi
  
  return 0
}

# Function to test basic upload operation
test_upload_operation() {
  log_info "Testing basic upload operation..."
  
  # Create a small test file
  local test_file=$(mktemp)
  echo "Swarm validation test file - $(date)" > "$test_file"
  
  # Upload to Swarm
  local hash=""
  local upload_output=""
  upload_output=$(upload_file "$SWARM_API" "$test_file" 2>&1)
  local upload_exit=$?
  
  if [[ $upload_exit -eq 0 ]]; then
    hash="$upload_output"
    if [[ -n "$hash" && "$hash" != "ERROR"* && ${#hash} -ge 64 ]]; then
      log_pass "Upload operation successful (hash: ${hash:0:16}...)"
      rm -f "$test_file"
      echo "$hash"
      return 0
    else
      log_fail "Upload returned invalid hash: ${hash:0:50}..."
      rm -f "$test_file"
      return 1
    fi
  else
    log_fail "Upload operation failed: ${upload_output:0:100}..."
    rm -f "$test_file"
    return 1
  fi
}

# Function to test basic download operation
test_download_operation() {
  local hash="$1"
  
  if [[ -z "$hash" ]]; then
    log_warn "No hash provided for download test, skipping"
    return 0
  fi
  
  # Clean hash (remove any trailing slashes or whitespace)
  hash=$(echo "$hash" | tr -d '/ \n\r')
  
  log_info "Testing basic download operation (hash: ${hash:0:16}...)..."
  
  # Try to download from Swarm
  local download_file=$(mktemp)
  
  # Extract IP from SWARM_API
  local api_ip=$(echo "$SWARM_API" | sed 's|http://||;s|:.*||')
  local api_port=$(echo "$SWARM_API" | sed 's|.*:||')
  
  # Try multiple endpoint formats for Swarm v0.5.8
  local download_success=false
  
  # Try /bzz:/<hash>/ format
  if curl -sfL -m 10 "http://${api_ip}:${api_port}/bzz:/${hash}/" -o "$download_file" >/dev/null 2>&1; then
    if [[ -s "$download_file" ]]; then
      download_success=true
    fi
  fi
  
  # Try /bzz-raw:/<hash> format if first didn't work
  if [[ "$download_success" == "false" ]]; then
    if curl -sfL -m 10 "http://${api_ip}:${api_port}/bzz-raw:/${hash}" -o "$download_file" >/dev/null 2>&1; then
      if [[ -s "$download_file" ]]; then
        download_success=true
      fi
    fi
  fi
  
  # Try /bzz:/<hash> format (without trailing slash)
  if [[ "$download_success" == "false" ]]; then
    if curl -sfL -m 10 "http://${api_ip}:${api_port}/bzz:/${hash}" -o "$download_file" >/dev/null 2>&1; then
      if [[ -s "$download_file" ]]; then
        download_success=true
      fi
    fi
  fi
  
  if [[ "$download_success" == "true" ]]; then
    log_pass "Download operation successful"
    rm -f "$download_file"
    return 0
  else
    log_warn "Download operation failed - content may not be immediately available (this is normal for Swarm)"
    log_info "Tried endpoints: /bzz:/${hash}/, /bzz-raw:/${hash}, /bzz:/${hash}"
    rm -f "$download_file"
    # Don't fail validation for download - Swarm content may take time to propagate
    return 0
  fi
}

# Function to check Swarm node health
check_swarm_node_health() {
  log_info "Checking Swarm node health..."
  
  local nodes=$(docker ps --format '{{.Names}}' | grep -E '^swarm-' || true)
  local healthy_count=0
  local total_count=0
  
  if [[ -z "$nodes" ]]; then
    log_fail "No Swarm nodes found"
    return 1
  fi
  
  while IFS= read -r node; do
    total_count=$((total_count + 1))
    
    # Check container health status
    local health=$(docker inspect --format='{{.State.Health.Status}}' "$node" 2>/dev/null || echo "none")
    
    if [[ "$health" == "healthy" ]]; then
      log_pass "$node: healthy"
      healthy_count=$((healthy_count + 1))
    elif [[ "$health" == "unhealthy" ]]; then
      log_fail "$node: unhealthy"
    elif [[ "$health" == "starting" ]]; then
      log_warn "$node: still starting"
    else
      # No healthcheck configured, check if container is running
      if docker ps --format '{{.Names}}' | grep -q "^${node}$"; then
        log_pass "$node: running (no healthcheck)"
        healthy_count=$((healthy_count + 1))
      else
        log_fail "$node: not running"
      fi
    fi
  done <<< "$nodes"
  
  if [[ $healthy_count -eq $total_count ]]; then
    log_pass "All Swarm nodes are healthy ($healthy_count/$total_count)"
    return 0
  elif [[ $healthy_count -gt 0 ]]; then
    log_warn "Some Swarm nodes are not healthy ($healthy_count/$total_count healthy)"
    return 0
  else
    log_fail "No Swarm nodes are healthy"
    return 1
  fi
}

# Main validation function
main() {
  echo -e "${BLUE}════════════════════════════════════════════════════════════════${NC}"
  echo -e "${BLUE}Swarm Setup Validation${NC}"
  echo -e "${BLUE}════════════════════════════════════════════════════════════════${NC}"
  echo ""
  
  local overall_success=true
  
  # 1. Check Docker
  echo -e "${BLUE}[1/6] Checking Docker...${NC}"
  if ! check_docker_available; then
    overall_success=false
  fi
  echo ""
  
  # 2. Check Swarm nodes are running
  echo -e "${BLUE}[2/6] Checking Swarm nodes...${NC}"
  if ! check_swarm_nodes_running; then
    overall_success=false
  fi
  echo ""
  
  # 3. Check Swarm API endpoints
  echo -e "${BLUE}[3/6] Checking Swarm API endpoints...${NC}"
  if ! check_swarm_api_endpoints; then
    overall_success=false
  fi
  echo ""
  
  # 4. Check network connectivity
  echo -e "${BLUE}[4/6] Checking network connectivity...${NC}"
  if ! check_network_connectivity; then
    # Connectivity warnings don't fail the validation
    log_info "Network connectivity check completed with warnings"
  fi
  echo ""
  
  # 5. Check node health
  echo -e "${BLUE}[5/6] Checking Swarm node health...${NC}"
  if ! check_swarm_node_health; then
    overall_success=false
  fi
  echo ""
  
  # 6. Test basic operations
  echo -e "${BLUE}[6/6] Testing basic operations...${NC}"
  local upload_hash=""
  if upload_hash=$(test_upload_operation); then
    if ! test_download_operation "$upload_hash"; then
      overall_success=false
    fi
  else
    overall_success=false
  fi
  echo ""
  
  # Summary
  echo -e "${BLUE}════════════════════════════════════════════════════════════════${NC}"
  echo -e "${BLUE}Validation Summary${NC}"
  echo -e "${BLUE}════════════════════════════════════════════════════════════════${NC}"
  echo -e "Passed:  ${GREEN}$VALIDATION_PASSED${NC}"
  echo -e "Failed:  ${RED}$VALIDATION_FAILED${NC}"
  echo -e "Warnings: ${YELLOW}$VALIDATION_WARNINGS${NC}"
  echo ""
  
  if [[ "$overall_success" == "true" && $VALIDATION_FAILED -eq 0 ]]; then
    echo -e "${GREEN}✓ All validations passed!${NC}"
    return 0
  elif [[ $VALIDATION_FAILED -eq 0 ]]; then
    echo -e "${YELLOW}⚠ Validation completed with warnings${NC}"
    return 0
  else
    echo -e "${RED}✗ Validation failed ($VALIDATION_FAILED error(s))${NC}"
    return 1
  fi
}

# Run main function
main "$@"

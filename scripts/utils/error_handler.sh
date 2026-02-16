#!/usr/bin/env bash
# Purpose: Error handling utilities for test scripts
# Usage: source scripts/utils/error_handler.sh

# Initialize error log directory
# Check if results_dir.sh was sourced and use its structure if available
if [[ -n "${RESULTS_DIR:-}" && -n "${LOGS_DIR:-}" ]]; then
  # Use results directory structure
  ERROR_LOG_DIR="$LOGS_DIR"
  ERROR_LOG_FILE="$LOGS_DIR/errors.log"
else
  # Fallback to legacy structure
  ERROR_LOG_DIR="${ERROR_LOG_DIR:-artifacts/swarm_tests}"
  RUN_ID="${RUN_ID:-$(date +%s)}"
  ERROR_LOG_FILE="$ERROR_LOG_DIR/$RUN_ID/errors.log"
fi

# Create error log directory
mkdir -p "$(dirname "$ERROR_LOG_FILE")"

# Colors for output
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Function to log error
log_error() {
  local message="$1"
  local context="${2:-}"
  local timestamp=$(date '+%Y-%m-%d %H:%M:%S')
  
  local log_entry="[$timestamp] ERROR: $message"
  if [[ -n "$context" ]]; then
    log_entry="$log_entry (Context: $context)"
  fi
  
  # Write to error log file
  echo "$log_entry" >> "$ERROR_LOG_FILE"
  
  # Also print to stderr
  echo -e "${RED}$log_entry${NC}" >&2
}

# Function to log warning
log_warning() {
  local message="$1"
  local context="${2:-}"
  local timestamp=$(date '+%Y-%m-%d %H:%M:%S')
  
  local log_entry="[$timestamp] WARNING: $message"
  if [[ -n "$context" ]]; then
    log_entry="$log_entry (Context: $context)"
  fi
  
  echo "$log_entry" >> "$ERROR_LOG_FILE"
  echo -e "${YELLOW}$log_entry${NC}" >&2
}

# Function to check Docker container health
check_container_health() {
  local container="$1"
  local max_attempts="${2:-5}"
  local attempt=1
  
  while [[ $attempt -le $max_attempts ]]; do
    # Check if container exists and is running
    if ! docker ps --format '{{.Names}}' | grep -q "^${container}$"; then
      log_error "Container not found: $container" "attempt $attempt/$max_attempts"
      if [[ $attempt -lt $max_attempts ]]; then
        sleep $((attempt * 2))  # Exponential backoff
        attempt=$((attempt + 1))
        continue
      fi
      return 1
    fi
    
    # Check container health status
    local health=$(docker inspect --format='{{.State.Health.Status}}' "$container" 2>/dev/null || echo "none")
    
    if [[ "$health" == "healthy" ]]; then
      return 0
    elif [[ "$health" == "unhealthy" ]]; then
      log_error "Container is unhealthy: $container" "attempt $attempt/$max_attempts"
      if [[ $attempt -lt $max_attempts ]]; then
        sleep $((attempt * 2))
        attempt=$((attempt + 1))
        continue
      fi
      return 1
    elif [[ "$health" == "starting" ]]; then
      log_warning "Container is still starting: $container" "attempt $attempt/$max_attempts"
      sleep $((attempt * 2))
      attempt=$((attempt + 1))
      continue
    else
      # No healthcheck or status unknown - check if process is running
      if docker ps --format '{{.Names}}' | grep -q "^${container}$"; then
        return 0  # Container is running, assume OK
      else
        log_error "Container not running: $container" "attempt $attempt/$max_attempts"
        if [[ $attempt -lt $max_attempts ]]; then
          sleep $((attempt * 2))
          attempt=$((attempt + 1))
          continue
        fi
        return 1
      fi
    fi
  done
  
  return 1
}

# Function to verify API endpoint is accessible
check_api_endpoint() {
  local url="$1"
  local timeout="${2:-5}"
  local max_attempts="${3:-3}"
  local attempt=1
  
  while [[ $attempt -le $max_attempts ]]; do
    if curl -sfL -m "$timeout" "$url" >/dev/null 2>&1; then
      return 0
    fi
    
    log_warning "API endpoint not accessible: $url" "attempt $attempt/$max_attempts"
    if [[ $attempt -lt $max_attempts ]]; then
      sleep $((attempt * 2))  # Exponential backoff
      attempt=$((attempt + 1))
      continue
    fi
    
    log_error "API endpoint failed after $max_attempts attempts: $url"
    return 1
  done
  
  return 1
}

# Function to verify API endpoint with container context
check_api_endpoint_container() {
  local container="$1"
  local url="$2"
  local timeout="${3:-5}"
  local max_attempts="${4:-3}"
  local attempt=1
  
  while [[ $attempt -le $max_attempts ]]; do
    # Try docker-compose first
    if command -v docker-compose >/dev/null 2>&1; then
      if docker-compose exec -T "$container" curl -sfL -m "$timeout" "$url" >/dev/null 2>&1; then
        return 0
      fi
    fi
    
    # Fallback to docker exec
    if docker exec "$container" curl -sfL -m "$timeout" "$url" >/dev/null 2>&1; then
      return 0
    fi
    
    log_warning "API endpoint not accessible in container $container: $url" "attempt $attempt/$max_attempts"
    if [[ $attempt -lt $max_attempts ]]; then
      sleep $((attempt * 2))
      attempt=$((attempt + 1))
      continue
    fi
    
    log_error "API endpoint failed after $max_attempts attempts in container $container: $url"
    return 1
  done
  
  return 1
}

# Function to retry an operation with exponential backoff
retry_with_backoff() {
  local max_attempts="$1"
  local delay="${2:-1}"  # Initial delay in seconds
  local max_delay="${3:-60}"  # Maximum delay in seconds
  shift 3
  local command=("$@")
  
  local attempt=1
  local current_delay=$delay
  
  while [[ $attempt -le $max_attempts ]]; do
    if "${command[@]}" 2>>"$ERROR_LOG_FILE"; then
      return 0
    fi
    
    if [[ $attempt -lt $max_attempts ]]; then
      log_warning "Command failed, retrying..." "attempt $attempt/$max_attempts: ${command[*]}"
      sleep "$current_delay"
      current_delay=$((current_delay * 2))
      if [[ $current_delay -gt $max_delay ]]; then
        current_delay=$max_delay
      fi
      attempt=$((attempt + 1))
    else
      log_error "Command failed after $max_attempts attempts: ${command[*]}"
      return 1
    fi
  done
  
  return 1
}

# Function to handle timeout gracefully
with_timeout() {
  local timeout="$1"
  shift
  local command=("$@")
  
  # Use timeout command if available
  if command -v timeout >/dev/null 2>&1; then
    if timeout "$timeout" "${command[@]}" 2>>"$ERROR_LOG_FILE"; then
      return 0
    else
      local exit_code=$?
      if [[ $exit_code -eq 124 ]]; then
        log_error "Command timed out after ${timeout}s: ${command[*]}"
      else
        log_error "Command failed with exit code $exit_code: ${command[*]}"
      fi
      return $exit_code
    fi
  else
    # Fallback: run command and kill after timeout using background process
    "${command[@]}" &
    local pid=$!
    local elapsed=0
    while kill -0 "$pid" 2>/dev/null && [[ $elapsed -lt $timeout ]]; do
      sleep 1
      elapsed=$((elapsed + 1))
    done
    
    if kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
      log_error "Command timed out after ${timeout}s: ${command[*]}"
      return 124
    else
      wait "$pid"
      return $?
    fi
  fi
}

# Function to check if Docker is available
check_docker() {
  if ! command -v docker >/dev/null 2>&1; then
    log_error "Docker is not installed or not in PATH"
    return 1
  fi
  
  if ! docker info >/dev/null 2>&1; then
    log_error "Docker daemon is not running or not accessible"
    return 1
  fi
  
  return 0
}

# Function to check if docker-compose is available
check_docker_compose() {
  if ! command -v docker-compose >/dev/null 2>&1; then
    log_warning "docker-compose not found, some features may be limited"
    return 1
  fi
  return 0
}

# Function to validate required tools
check_required_tools() {
  local tools=("$@")
  local missing=()
  
  for tool in "${tools[@]}"; do
    if ! command -v "$tool" >/dev/null 2>&1; then
      missing+=("$tool")
    fi
  done
  
  if [[ ${#missing[@]} -gt 0 ]]; then
    log_error "Required tools not found: ${missing[*]}"
    return 1
  fi
  
  return 0
}

# Function to get error log file path
get_error_log_file() {
  echo "$ERROR_LOG_FILE"
}

# Function to get error log directory
get_error_log_dir() {
  echo "$(dirname "$ERROR_LOG_FILE")"
}

# Export functions for use in other scripts
export -f log_error
export -f log_warning
export -f check_container_health
export -f check_api_endpoint
export -f check_api_endpoint_container
export -f retry_with_backoff
export -f with_timeout
export -f check_docker
export -f check_docker_compose
export -f check_required_tools
export -f get_error_log_file
export -f get_error_log_dir

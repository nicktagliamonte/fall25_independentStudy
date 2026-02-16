#!/usr/bin/env bash
# Purpose: Structured logging utilities for test scripts
# Usage: source scripts/utils/test_logger.sh

# Initialize logging directory
# Check if results_dir.sh was sourced and use its structure if available
if [[ -n "${RESULTS_DIR:-}" && -n "${LOGS_DIR:-}" ]]; then
  # Use results directory structure
  LOG_DIR="$LOGS_DIR"
  LOG_FILE="$LOGS_DIR/test.log"
  SUMMARY_LOG="$LOGS_DIR/summary.log"
else
  # Fallback to legacy structure
  LOG_DIR="${LOG_DIR:-artifacts/swarm_tests}"
  RUN_ID="${RUN_ID:-$(date +%s)}"
  LOG_FILE="$LOG_DIR/$RUN_ID/test.log"
  SUMMARY_LOG="$LOG_DIR/$RUN_ID/summary.log"
fi

# Create log directory
mkdir -p "$(dirname "$LOG_FILE")"

# Colors for console output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
MAGENTA='\033[0;35m'
NC='\033[0m' # No Color

# Log level constants
LOG_LEVEL_DEBUG=0
LOG_LEVEL_INFO=1
LOG_LEVEL_WARN=2
LOG_LEVEL_ERROR=3

# Current log level (default: INFO)
LOG_LEVEL="${LOG_LEVEL:-$LOG_LEVEL_INFO}"

# Function to get current timestamp
get_timestamp() {
  date '+%Y-%m-%d %H:%M:%S.%3N' 2>/dev/null || date '+%Y-%m-%d %H:%M:%S'
}

# Function to format duration
format_duration() {
  local seconds="$1"
  if command -v bc >/dev/null 2>&1; then
    if (( $(echo "$seconds < 1" | bc -l 2>/dev/null || echo 0) )); then
      printf "%.0fms" "$(echo "$seconds * 1000" | bc -l 2>/dev/null || echo 0)"
    elif (( $(echo "$seconds < 60" | bc -l 2>/dev/null || echo 0) )); then
      printf "%.2fs" "$seconds"
    else
      local mins=$(( ${seconds%.*} / 60 ))
      local secs=$(echo "$seconds - $mins * 60" | bc -l 2>/dev/null || echo "$seconds")
      printf "%dm %.2fs" "$mins" "$secs"
    fi
  else
    # Fallback without bc
    if (( $(awk "BEGIN {print ($seconds < 1)}") )); then
      printf "%.0fms" "$(awk "BEGIN {print ($seconds * 1000)}")"
    elif (( $(awk "BEGIN {print ($seconds < 60)}") )); then
      printf "%.2fs" "$seconds"
    else
      local mins=$(( ${seconds%.*} / 60 ))
      local secs=$(awk "BEGIN {print ($seconds - $mins * 60)}")
      printf "%dm %.2fs" "$mins" "$secs"
    fi
  fi
}

# Internal function to write log entry
_write_log() {
  local level="$1"
  local message="$2"
  local context="${3:-}"
  local timestamp=$(get_timestamp)
  
  # Build log entry
  local log_entry="[$timestamp] [$RUN_ID] [$level] $message"
  if [[ -n "$context" ]]; then
    log_entry="$log_entry | Context: $context"
  fi
  
  # Write to log file
  echo "$log_entry" >> "$LOG_FILE"
  
  # Write to console with color based on level
  case "$level" in
    DEBUG)
      if [[ $LOG_LEVEL -le $LOG_LEVEL_DEBUG ]]; then
        echo -e "${CYAN}$log_entry${NC}" >&2
      fi
      ;;
    INFO)
      if [[ $LOG_LEVEL -le $LOG_LEVEL_INFO ]]; then
        echo -e "${BLUE}$log_entry${NC}"
      fi
      ;;
    WARN)
      if [[ $LOG_LEVEL -le $LOG_LEVEL_WARN ]]; then
        echo -e "${YELLOW}$log_entry${NC}" >&2
      fi
      ;;
    ERROR)
      if [[ $LOG_LEVEL -le $LOG_LEVEL_ERROR ]]; then
        echo -e "${RED}$log_entry${NC}" >&2
      fi
      ;;
    SUCCESS)
      echo -e "${GREEN}$log_entry${NC}"
      ;;
    *)
      echo "$log_entry"
      ;;
  esac
}

# Function to log test start
log_test_start() {
  local test_name="$1"
  local params="${2:-}"
  local timestamp=$(get_timestamp)
  
  # Write detailed log
  _write_log "INFO" "Test started: $test_name" "$params"
  
  # Write summary entry
  echo "[$timestamp] START: $test_name | Params: $params" >> "$SUMMARY_LOG"
  
  # Also write to console with special formatting
  echo ""
  echo -e "${CYAN}═══════════════════════════════════════════════════════════════════════════════${NC}"
  echo -e "${CYAN}▶ Starting Test: ${MAGENTA}$test_name${NC}"
  if [[ -n "$params" ]]; then
    echo -e "${CYAN}  Parameters: ${NC}$params"
  fi
  echo -e "${CYAN}═══════════════════════════════════════════════════════════════════════════════${NC}"
  echo ""
}

# Function to log test end
log_test_end() {
  local test_name="$1"
  local result="$2"  # PASS, FAIL, SKIP, etc.
  local duration="${3:-0}"  # Duration in seconds
  local timestamp=$(get_timestamp)
  
  # Format duration
  local duration_str=""
  if [[ -n "$duration" && "$duration" != "0" ]]; then
    duration_str=$(format_duration "$duration")
  fi
  
  # Determine result color and status
  local result_color=""
  local result_symbol=""
  case "$result" in
    PASS|SUCCESS|OK)
      result_color="$GREEN"
      result_symbol="✓"
      ;;
    FAIL|FAILURE|ERROR)
      result_color="$RED"
      result_symbol="✗"
      ;;
    SKIP|SKIPPED)
      result_color="$YELLOW"
      result_symbol="⊘"
      ;;
    *)
      result_color="$YELLOW"
      result_symbol="?"
      ;;
  esac
  
  # Write detailed log
  local log_message="Test completed: $test_name | Result: $result"
  if [[ -n "$duration_str" ]]; then
    log_message="$log_message | Duration: $duration_str"
  fi
  _write_log "INFO" "$log_message"
  
  # Write summary entry
  local summary_entry="[$timestamp] END: $test_name | Result: $result"
  if [[ -n "$duration_str" ]]; then
    summary_entry="$summary_entry | Duration: $duration_str"
  fi
  echo "$summary_entry" >> "$SUMMARY_LOG"
  
  # Write to console with special formatting
  echo ""
  echo -e "${CYAN}═══════════════════════════════════════════════════════════════════════════════${NC}"
  echo -e "${CYAN}◀ Test Complete: ${MAGENTA}$test_name${NC}"
  echo -e "  ${result_color}${result_symbol} Result: $result${NC}"
  if [[ -n "$duration_str" ]]; then
    echo -e "  ${BLUE}⏱  Duration: $duration_str${NC}"
  fi
  echo -e "${CYAN}═══════════════════════════════════════════════════════════════════════════════${NC}"
  echo ""
}

# Function to log error (compatible with error_handler.sh interface)
log_error() {
  local message="$1"
  local context="${2:-}"
  _write_log "ERROR" "$message" "$context"
}

# Additional convenience logging functions
log_info() {
  local message="$1"
  local context="${2:-}"
  _write_log "INFO" "$message" "$context"
}

log_warn() {
  local message="$1"
  local context="${2:-}"
  _write_log "WARN" "$message" "$context"
}

log_debug() {
  local message="$1"
  local context="${2:-}"
  _write_log "DEBUG" "$message" "$context"
}

log_success() {
  local message="$1"
  local context="${2:-}"
  _write_log "SUCCESS" "$message" "$context"
}

# Function to get log file path
get_log_file() {
  echo "$LOG_FILE"
}

# Function to get summary log file path
get_summary_log_file() {
  echo "$SUMMARY_LOG"
}

# Function to get log directory
get_log_dir() {
  echo "$(dirname "$LOG_FILE")"
}

# Export functions for use in other scripts
export -f log_test_start
export -f log_test_end
export -f log_error
export -f log_info
export -f log_warn
export -f log_debug
export -f log_success
export -f get_log_file
export -f get_summary_log_file
export -f get_log_dir
export -f get_timestamp
export -f format_duration

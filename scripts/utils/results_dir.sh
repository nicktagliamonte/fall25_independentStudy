#!/usr/bin/env bash
# Purpose: Create and manage results directory structure for test runs
# Usage: source scripts/utils/results_dir.sh

# Initialize results directory
RESULTS_BASE_DIR="${RESULTS_BASE_DIR:-artifacts/swarm_comparison_tests}"
TIMESTAMP="${TIMESTAMP:-$(date +%Y%m%d_%H%M%S)}"
RUN_ID="${RUN_ID:-$(date +%s)}"

# Create main results directory for this run
RESULTS_DIR="$RESULTS_BASE_DIR/$TIMESTAMP"

# Subdirectories
OUR_SYSTEM_DIR="$RESULTS_DIR/our_system"
SWARM_DIR="$RESULTS_DIR/swarm"
COMPARISON_DIR="$RESULTS_DIR/comparison"
PLOTS_DIR="$RESULTS_DIR/plots"
LOGS_DIR="$RESULTS_DIR/logs"

# Function to create results directory structure
create_results_structure() {
  local base_dir="${1:-$RESULTS_DIR}"
  
  # Create main directory
  mkdir -p "$base_dir"
  
  # Create subdirectories
  mkdir -p "$base_dir/our_system"
  mkdir -p "$base_dir/swarm"
  mkdir -p "$base_dir/comparison"
  mkdir -p "$base_dir/plots"
  mkdir -p "$base_dir/logs"
  
  # Create README in main directory
  cat > "$base_dir/README.md" <<EOF
# Test Results: $TIMESTAMP

**Run ID**: $RUN_ID  
**Timestamp**: $TIMESTAMP  
**Created**: $(date '+%Y-%m-%d %H:%M:%S')

## Directory Structure

- \`our_system/\`: Results from our system tests
- \`swarm/\`: Results from Swarm tests
- \`comparison/\`: Aggregated comparison data and analysis
- \`plots/\`: Generated visualizations and charts
- \`logs/\`: Test execution logs

## Usage

Results are organized by system and test type. Each subdirectory may contain:
- CSV files with raw test data
- JSON files with structured results
- Log files with execution details
- Other artifacts specific to the test

EOF
  
  echo "$base_dir"
}

# Function to get results directory path
get_results_dir() {
  echo "$RESULTS_DIR"
}

# Function to get subdirectory paths
get_our_system_dir() {
  echo "$OUR_SYSTEM_DIR"
}

get_swarm_dir() {
  echo "$SWARM_DIR"
}

get_comparison_dir() {
  echo "$COMPARISON_DIR"
}

get_plots_dir() {
  echo "$PLOTS_DIR"
}

get_logs_dir() {
  echo "$LOGS_DIR"
}

# Function to initialize results structure (creates directories)
init_results_dir() {
  local custom_timestamp="${1:-}"
  local custom_run_id="${2:-}"
  
  # Override timestamp if provided
  if [[ -n "$custom_timestamp" ]]; then
    TIMESTAMP="$custom_timestamp"
    RESULTS_DIR="$RESULTS_BASE_DIR/$TIMESTAMP"
    OUR_SYSTEM_DIR="$RESULTS_DIR/our_system"
    SWARM_DIR="$RESULTS_DIR/swarm"
    COMPARISON_DIR="$RESULTS_DIR/comparison"
    PLOTS_DIR="$RESULTS_DIR/plots"
    LOGS_DIR="$RESULTS_DIR/logs"
  fi
  
  # Override run ID if provided
  if [[ -n "$custom_run_id" ]]; then
    RUN_ID="$custom_run_id"
  fi
  
  # Create structure
  create_results_structure "$RESULTS_DIR"
  
  # Export for use in other scripts
  export RESULTS_DIR
  export OUR_SYSTEM_DIR
  export SWARM_DIR
  export COMPARISON_DIR
  export PLOTS_DIR
  export LOGS_DIR
  export RUN_ID
  export TIMESTAMP
  
  echo "$RESULTS_DIR"
}

# Function to save test metadata
save_test_metadata() {
  local metadata_file="$RESULTS_DIR/metadata.json"
  local test_name="$1"
  local test_params="${2:-{}}"
  local start_time="${3:-$(date +%s)}"
  local end_time="${4:-}"
  local result="${5:-}"
  
  # Ensure results directory exists
  mkdir -p "$RESULTS_DIR"
  
  # Create metadata JSON object
  local metadata="{"
  metadata+="\"run_id\": \"$RUN_ID\","
  metadata+="\"timestamp\": \"$TIMESTAMP\","
  metadata+="\"test_name\": \"$test_name\","
  metadata+="\"test_params\": $test_params,"
  metadata+="\"start_time\": $start_time"
  
  if [[ -n "$end_time" ]]; then
    metadata+=",\"end_time\": $end_time"
  fi
  
  if [[ -n "$result" ]]; then
    metadata+=",\"result\": \"$result\""
  fi
  
  metadata+="}"
  
  # Append to metadata file (or create if doesn't exist)
  if [[ -f "$metadata_file" ]]; then
    # Read existing metadata and append
    if command -v jq >/dev/null 2>&1; then
      local existing=$(cat "$metadata_file" 2>/dev/null || echo "[]")
      if [[ "$existing" == "["* ]]; then
        # It's an array, append to it
        echo "$existing" | jq ". + [$metadata]" > "$metadata_file.tmp" 2>/dev/null && mv "$metadata_file.tmp" "$metadata_file" || {
          # Fallback if jq fails
          echo "$existing" | sed 's/\]$/, '"$metadata"']/' > "$metadata_file.tmp" && mv "$metadata_file.tmp" "$metadata_file"
        }
      else
        # Convert to array and append
        echo "[$existing, $metadata]" | jq '.' > "$metadata_file.tmp" 2>/dev/null && mv "$metadata_file.tmp" "$metadata_file" || {
          echo "[$existing, $metadata]" > "$metadata_file"
        }
      fi
    else
      # Fallback without jq
      local existing=$(cat "$metadata_file" 2>/dev/null || echo "[]")
      if [[ "$existing" == "["* ]]; then
        echo "$existing" | sed 's/\]$/, '"$metadata"']/' > "$metadata_file.tmp" && mv "$metadata_file.tmp" "$metadata_file"
      else
        echo "[$existing, $metadata]" > "$metadata_file"
      fi
    fi
  else
    # Create new array
    if command -v jq >/dev/null 2>&1; then
      echo "[$metadata]" | jq '.' > "$metadata_file" 2>/dev/null || echo "[$metadata]" > "$metadata_file"
    else
      echo "[$metadata]" > "$metadata_file"
    fi
  fi
}

# Function to get path for saving results by system
get_result_path() {
  local system="$1"  # "our_system" or "swarm"
  local filename="$2"
  local subdir="${3:-}"  # Optional subdirectory
  
  local base_dir=""
  case "$system" in
    our_system|our)
      base_dir="$OUR_SYSTEM_DIR"
      ;;
    swarm)
      base_dir="$SWARM_DIR"
      ;;
    comparison|compare)
      base_dir="$COMPARISON_DIR"
      ;;
    plots|plot)
      base_dir="$PLOTS_DIR"
      ;;
    logs|log)
      base_dir="$LOGS_DIR"
      ;;
    *)
      base_dir="$RESULTS_DIR"
      ;;
  esac
  
  if [[ -n "$subdir" ]]; then
    mkdir -p "$base_dir/$subdir"
    echo "$base_dir/$subdir/$filename"
  else
    echo "$base_dir/$filename"
  fi
}

# Auto-create structure when sourced (if not already created)
if [[ -z "${RESULTS_DIR_CREATED:-}" ]]; then
  init_results_dir >/dev/null 2>&1 || true
  export RESULTS_DIR_CREATED=1
fi

# Export functions
export -f create_results_structure
export -f get_results_dir
export -f get_our_system_dir
export -f get_swarm_dir
export -f get_comparison_dir
export -f get_plots_dir
export -f get_logs_dir
export -f init_results_dir
export -f save_test_metadata
export -f get_result_path

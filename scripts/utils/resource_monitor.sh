#!/usr/bin/env bash
set -euo pipefail

# Purpose: Run docker stats in a loop during tests, appending to CSV with timestamp.
# Usage: ./scripts/utils/resource_monitor.sh --output <file> [--interval <sec>] [container1 container2 ...]
#   --output <file>   Output CSV path (required)
#   --interval <sec>  Sample interval in seconds (default: 5)
#   Containers: names or patterns; if none given, uses all running (fall25-*, swarm-*)
# Run in background; kill to stop. Output: timestamp,container,cpu_pct,mem_usage_mb

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

OUTPUT_FILE=""
INTERVAL=5
CONTAINERS=()

while [[ $# -gt 0 ]]; do
  case $1 in
    --output)
      OUTPUT_FILE="$2"
      shift 2
      ;;
    --interval)
      INTERVAL="$2"
      shift 2
      ;;
    --help)
      echo "Usage: $0 --output <csv> [--interval <sec>] [container1 container2 ...]"
      exit 0
      ;;
    *)
      CONTAINERS+=("$1")
      shift
      ;;
  esac
done

if [[ -z "$OUTPUT_FILE" ]]; then
  echo "Error: --output <file> required" >&2
  exit 1
fi

# Resolve container list: explicit names, or default to fall25-* and swarm-*
if [[ ${#CONTAINERS[@]} -eq 0 ]]; then
  CONTAINERS=($(docker ps --format '{{.Names}}' 2>/dev/null | grep -E '^(fall25-|swarm-)' || true))
fi

if [[ ${#CONTAINERS[@]} -eq 0 ]]; then
  echo "Warning: No matching containers found; will sample when containers appear" >&2
fi

# Write CSV header
mkdir -p "$(dirname "$OUTPUT_FILE")"
echo "timestamp,container,cpu_pct,mem_usage_mb" > "$OUTPUT_FILE"

# Parse mem string (e.g. "50.23MiB / 1.5GiB") to MB
to_mb() {
  local s="$1"
  local part=$(echo "$s" | sed 's/\/.*//' | tr -d ' ')
  local num=$(echo "$part" | grep -oE '[0-9]+\.?[0-9]*' | head -1 || echo "0")
  local unit=$(echo "$part" | grep -oE 'KiB|MiB|GiB' | head -1 || echo "MiB")
  case "$unit" in
    KiB) echo "scale=2; $num / 1024" | bc 2>/dev/null || echo "$num";;
    MiB) echo "$num";;
    GiB) echo "scale=2; $num * 1024" | bc 2>/dev/null || echo "$num";;
    *)   echo "$num";;
  esac
}

# Track whether we had explicit containers (don't refresh) vs auto-detect (refresh each loop)
HAD_EXPLICIT_CONTAINERS=$([[ ${#CONTAINERS[@]} -gt 0 ]] && echo 1 || echo 0)

while true; do
  ts=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  if [[ "$HAD_EXPLICIT_CONTAINERS" -eq 0 ]]; then
    CONTAINERS=($(docker ps --format '{{.Names}}' 2>/dev/null | grep -E '^(fall25-|swarm-)' || true))
  fi
  if [[ ${#CONTAINERS[@]} -gt 0 ]]; then
    for c in "${CONTAINERS[@]}"; do
      line=$(docker stats --no-stream --format "{{.Name}},{{.CPUPerc}},{{.MemUsage}}" "$c" 2>/dev/null || true)
      if [[ -n "$line" ]]; then
        name=$(echo "$line" | cut -d',' -f1)
        cpu=$(echo "$line" | cut -d',' -f2 | tr -d '%')
        mem_raw=$(echo "$line" | cut -d',' -f3-)
        mem_mb=$(to_mb "$mem_raw" 2>/dev/null || echo "")
        echo "$ts,$name,$cpu,$mem_mb" >> "$OUTPUT_FILE"
      fi
    done
  fi
  sleep "$INTERVAL"
done

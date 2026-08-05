#!/usr/bin/env bash
set -euo pipefail

# Purpose: Run docker stats in a loop during tests, appending to CSV with timestamp.
# Usage: ./scripts/utils/resource_monitor.sh --output <file> [--interval <sec>] [container1 container2 ...]
#   --output <file>   Output CSV path (required)
#   --interval <sec>  Sample interval in seconds (default: 5)
#   Containers: names or patterns; if none given, uses all running Tarsus/Bee containers
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

# Resolve container list: explicit names, or default to fall25-* and swarm-*.
AUTO_DETECT=0
if [[ ${#CONTAINERS[@]} -eq 0 ]]; then
  AUTO_DETECT=1
  CONTAINERS=($(docker ps --format '{{.Names}}' 2>/dev/null | grep -E '^(fall25-|swarm-|bee-)' || true))
fi

if [[ ${#CONTAINERS[@]} -eq 0 ]]; then
  echo "Warning: No matching containers found; will sample when containers appear" >&2
fi

# Write CSV header
mkdir -p "$(dirname "$OUTPUT_FILE")"
echo "timestamp,container,cpu_pct,mem_usage_mb" > "$OUTPUT_FILE"

while true; do
  ts=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  if [[ "$AUTO_DETECT" -eq 1 ]]; then
    CONTAINERS=($(docker ps --format '{{.Names}}' 2>/dev/null | grep -E '^(fall25-|swarm-|bee-)' || true))
  fi
  if [[ ${#CONTAINERS[@]} -gt 0 ]]; then
    # One docker-stats call samples every container concurrently. The previous
    # per-container loop took O(N) blocking daemon round trips per sample and
    # could spend an entire short campaign measuring itself, especially at
    # 50--100 nodes.
    docker stats --no-stream \
      --format "{{.Name}},{{.CPUPerc}},{{.MemUsage}}" \
      "${CONTAINERS[@]}" 2>/dev/null |
      awk -F',' -v ts="$ts" '
        function to_mb(raw, value, unit) {
          sub(/[[:space:]]*\/.*/, "", raw)
          gsub(/[[:space:]]/, "", raw)
          value = raw
          gsub(/[[:alpha:]]/, "", value)
          unit = raw
          gsub(/[0-9.]/, "", unit)
          if (unit == "KiB") return value / 1024
          if (unit == "GiB") return value * 1024
          return value
        }
        {
          gsub(/%/, "", $2)
          printf "%s,%s,%s,%.2f\n", ts, $1, $2, to_mb($3)
        }
      ' >>"$OUTPUT_FILE" || true
  fi
  sleep "$INTERVAL"
done

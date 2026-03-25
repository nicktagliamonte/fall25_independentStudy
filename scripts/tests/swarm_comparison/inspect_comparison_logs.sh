#!/usr/bin/env bash
set -euo pipefail

# Purpose: Scan comparison run logs for first likely failure (timeout, compose, health, OOM).
# Usage: inspect_comparison_logs.sh <results_dir>

[[ "${1:-}" != "" ]] || { echo "Usage: $0 <results_dir>" >&2; exit 1; }
DIR="${1/#\~/$HOME}"
[[ -d "$DIR" ]] || { echo "Not a directory: $DIR" >&2; exit 1; }

pat='ERROR|error:|timed out|TIMEOUT|exit 124|Cannot connect|connection refused|No space left|OOMKilled|out of memory|Killed process|unhealthy|dependency failed|Build failed|OCI runtime'

echo "=== inspect_comparison_logs: $DIR ==="
shopt -s nullglob
for f in "$DIR"/our_startup_n*.log "$DIR"/swarm_startup_n*.log "$DIR"/upload_n*.log; do
  [[ -f "$f" ]] || continue
  if grep -qiE "$pat" "$f" 2>/dev/null; then
    echo ""
    echo "--- $f (first matches) ---"
    grep -niE "$pat" "$f" | head -25 || true
  fi
done

if [[ -f "$DIR/summary_report.txt" ]]; then
  echo ""
  echo "=== summary_report.txt (head) ==="
  head -40 "$DIR/summary_report.txt"
fi

echo ""
ROOT_GUESS="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
if [[ -f "$ROOT_GUESS/docker-compose.swarm.yml" ]]; then
  "$ROOT_GUESS/scripts/docker/swarm/verify_generated_ips.sh" "$ROOT_GUESS/docker-compose.swarm.yml" || true
fi
echo "Done."

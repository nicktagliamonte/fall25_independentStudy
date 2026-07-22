#!/usr/bin/env bash
set -euo pipefail

# Purpose: Combine multiple catalog growth CSVs (same schema) by averaging upload_ms and
#          download_total_ms for each files_on_network row. Requires bc; skips non-numeric cells.

if ! command -v bc >/dev/null 2>&1; then
  echo "Error: bc required" >&2
  exit 1
fi

if [[ $# -lt 2 ]]; then
  echo "Usage: $0 <output.csv> <pass1.csv> [pass2.csv ...]" >&2
  exit 1
fi

DEST="$1"
shift
PASS_FILES=("$@")
NTRIALS=${#PASS_FILES[@]}

for p in "${PASS_FILES[@]}"; do
  if [[ ! -f "$p" ]]; then
    echo "Error: missing pass file: $p" >&2
    exit 1
  fi
done

MAX_F=0
for p in "${PASS_FILES[@]}"; do
  mf=$(awk -F, 'NR>1 && $3 ~ /^[0-9]+$/ { if ($3+0 > m) m = $3+0 } END { print m+0 }' "$p")
  [[ "$mf" -gt "$MAX_F" ]] && MAX_F="$mf"
done

if [[ "$MAX_F" -lt 1 ]]; then
  echo "Error: could not infer files_on_network range from pass files" >&2
  exit 1
fi

echo "system,node_count,files_on_network,payload_size,upload_ms,download_total_ms" > "$DEST"

for f in $(seq 1 "$MAX_F"); do
  u_sum="0"
  d_sum="0"
  nu=0
  nd=0
  sys=""
  nc=""
  psz=""
  for p in "${PASS_FILES[@]}"; do
    line=$(awk -F, -v k="$f" 'NR>1 && $3 == k { print; exit }' "$p")
    [[ -z "$line" ]] && continue
    IFS=',' read -r sys nc _fn psz um dm <<< "$line"
    if [[ "$um" != "ERROR" && -n "$um" && "$um" =~ ^[0-9]*\.?[0-9]+$ ]]; then
      u_sum=$(echo "$u_sum + $um" | bc -l)
      nu=$((nu + 1))
    fi
    if [[ "$dm" != "ERROR" && -n "$dm" && "$dm" =~ ^[0-9]*\.?[0-9]+$ ]]; then
      d_sum=$(echo "$d_sum + $dm" | bc -l)
      nd=$((nd + 1))
    fi
  done
  um_out="ERROR"
  dm_out="ERROR"
  [[ "$nu" -eq "$NTRIALS" ]] && um_out=$(echo "scale=6; $u_sum / $NTRIALS" | bc -l)
  [[ "$nd" -eq "$NTRIALS" ]] && dm_out=$(echo "scale=6; $d_sum / $NTRIALS" | bc -l)
  echo "$sys,$nc,$f,$psz,$um_out,$dm_out" >> "$DEST"
done

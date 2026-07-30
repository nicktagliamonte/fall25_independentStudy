#!/usr/bin/env bash
set -euo pipefail

[[ $# -eq 1 ]] || { echo "usage: $0 RUN_DIR" >&2; exit 2; }
run_dir=$1
[[ -s "$run_dir/plan.tsv" ]] || { echo "missing plan.tsv" >&2; exit 1; }

planned=$(( $(wc -l <"$run_dir/plan.tsv") - 1 ))
complete=$(find "$run_dir/cells" -mindepth 2 -maxdepth 2 -name COMPLETE -type f | wc -l)
[[ "$complete" -eq "$planned" ]] || {
  echo "complete cells=$complete, planned=$planned" >&2
  exit 1
}

while IFS= read -r marker; do
  "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/validate_cell.sh" "$(dirname "$marker")"
done < <(find "$run_dir/cells" -mindepth 2 -maxdepth 2 -name COMPLETE -type f | sort)

if [[ -s "$run_dir/resolved.env" ]] &&
  grep -q '^RUN_RESILIENCE=true$' "$run_dir/resolved.env"; then
  resilience_complete=$(find "$run_dir/resilience" -mindepth 2 -maxdepth 2 \
    -name COMPLETE -type f | wc -l)
  [[ "$resilience_complete" -eq 1 ]] || {
    echo "resilience complete cells=$resilience_complete, expected=1" >&2
    exit 1
  }
  resilience_marker=$(find "$run_dir/resilience" -mindepth 2 -maxdepth 2 \
    -name COMPLETE -type f)
  "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/validate_resilience_cell.sh" \
    "$(dirname "$resilience_marker")"
fi

"$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/analyze_campaign.py" "$run_dir"
for artifact in analysis/queries_all.csv analysis/population_all.csv analysis/query_summary.csv analysis/query_summary.tex analysis/analysis.json; do
  [[ -s "$run_dir/$artifact" ]] || { echo "missing analysis artifact: $artifact" >&2; exit 1; }
done
echo "validated campaign $run_dir"

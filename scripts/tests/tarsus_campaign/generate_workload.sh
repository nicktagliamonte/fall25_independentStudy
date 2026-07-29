#!/usr/bin/env bash
set -euo pipefail

[[ $# -eq 2 ]] || { echo "usage: $0 CATALOG_SIZE OUTPUT_DIR" >&2; exit 2; }
catalog_size=$1
output_dir=$2
[[ "$catalog_size" =~ ^[1-9][0-9]*$ ]] || { echo "catalog size must be positive" >&2; exit 2; }
mkdir -p "$output_dir"

awk -v count="$catalog_size" '
  BEGIN {
    for (i = 0; i < count; i++) {
      group = i % 100
      stage = i % 7
      if (i % 100 == 0) tag = "needle-rare"
      else if (i % 10 == 0) tag = "needle-medium"
      else if (i % 2 == 0) tag = "needle-common"
      else tag = "neutral"
      printf "workflow/group-%03d/stage-%02d/artifact-%08d-%s\n", group, stage, i, tag
    }
  }
' >"$output_dir/names.txt"

first=$(sed -n '1p' "$output_dir/names.txt")
middle=$(sed -n "$((catalog_size / 2 + 1))p" "$output_dir/names.txt")
last=$(sed -n "${catalog_size}p" "$output_dir/names.txt")
{
  printf 'exact-first\texact\tone\t%s\n' "$first"
  printf 'exact-middle\texact\tone\t%s\n' "$middle"
  printf 'exact-last\texact\tone\t%s\n' "$last"
  printf 'prefix-one-percent\tprefix\t0.01\tworkflow/group-000/*\n'
  printf 'substring-rare\tsubstring\t0.01\t*needle-rare*\n'
  printf 'substring-medium\tsubstring\t0.09\t*needle-medium*\n'
  printf 'substring-common\tsubstring\t0.40\t*needle-common*\n'
} >"$output_dir/patterns.tsv"

sha256sum "$output_dir/names.txt" "$output_dir/patterns.tsv" >"$output_dir/SHA256SUMS"


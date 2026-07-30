#!/usr/bin/env bash
set -euo pipefail

[[ $# -ge 5 && "$4" == "--" ]] || {
  echo "usage: $0 GC_THRESH1 GC_THRESH2 GC_THRESH3 -- COMMAND [ARG ...]" >&2
  exit 2
}

temporary_gc1=$1
temporary_gc2=$2
temporary_gc3=$3
shift 4

for value in "$temporary_gc1" "$temporary_gc2" "$temporary_gc3"; do
  [[ "$value" =~ ^[1-9][0-9]*$ ]] || {
    echo "neighbor limits must be positive integers: $value" >&2
    exit 2
  }
done
[[ "$temporary_gc1" -le "$temporary_gc2" &&
  "$temporary_gc2" -le "$temporary_gc3" ]] || {
  echo "neighbor limits must be nondecreasing" >&2
  exit 2
}

original_gc1=$(sysctl -n net.ipv4.neigh.default.gc_thresh1)
original_gc2=$(sysctl -n net.ipv4.neigh.default.gc_thresh2)
original_gc3=$(sysctl -n net.ipv4.neigh.default.gc_thresh3)
restored=false

set_neighbor_limits() {
  local gc1=$1
  local gc2=$2
  local gc3=$3
  docker run --rm --privileged --network host alpine:3.21 sh -c \
    'sysctl -w net.ipv4.neigh.default.gc_thresh1="$1" net.ipv4.neigh.default.gc_thresh2="$2" net.ipv4.neigh.default.gc_thresh3="$3" >/dev/null' \
    sh "$gc1" "$gc2" "$gc3"
  [[ "$(sysctl -n net.ipv4.neigh.default.gc_thresh1)" == "$gc1" &&
    "$(sysctl -n net.ipv4.neigh.default.gc_thresh2)" == "$gc2" &&
    "$(sysctl -n net.ipv4.neigh.default.gc_thresh3)" == "$gc3" ]]
}

restore_neighbor_limits() {
  if [[ "$restored" == "true" ]]; then
    return
  fi
  if ! set_neighbor_limits "$original_gc1" "$original_gc2" "$original_gc3"; then
    return 1
  fi
  restored=true
}

trap 'restore_neighbor_limits || true' EXIT
set_neighbor_limits "$temporary_gc1" "$temporary_gc2" "$temporary_gc3"

command_status=0
"$@" || command_status=$?

if ! restore_neighbor_limits; then
  echo "failed to restore host neighbor limits" >&2
  exit 1
fi
trap - EXIT
exit "$command_status"

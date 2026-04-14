#!/usr/bin/env bash
if command -v gtimeout >/dev/null 2>&1; then
  shopt -s expand_aliases
  alias timeout=gtimeout
elif ! command -v timeout >/dev/null 2>&1; then
  function timeout() {
    if [[ "$1" == "-k" ]]; then shift 2; fi
    local limit=$1; shift
    perl -e 'alarm shift; exec @ARGV' "$limit" "$@"
  }
fi
timeout -k 10 5 sleep 2
echo "Success? $?"

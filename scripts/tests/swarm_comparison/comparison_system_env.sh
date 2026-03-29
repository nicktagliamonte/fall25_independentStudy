#!/usr/bin/env bash
# Purpose: Normalize SWARM_COMPARISON_SYSTEM for vn-IPFS vs Swarm comparison tests.
# run_comparison.sh sets SWARM_COMPARISON_SYSTEM to both|vnipfs|swarm before invoking test scripts.
# Source this file after CLI parsing, then call cmp_resolve_system_flags.

cmp_resolve_system_flags() {
  local s="${SWARM_COMPARISON_SYSTEM:-both}"
  s="${s,,}"
  case "$s" in
    vnipfs|vn-ipfs|ours|our|vn)
      CMP_INCLUDE_OUR=1
      CMP_INCLUDE_SWARM=0
      ;;
    swarm|bee)
      CMP_INCLUDE_OUR=0
      CMP_INCLUDE_SWARM=1
      ;;
    both|all|"")
      CMP_INCLUDE_OUR=1
      CMP_INCLUDE_SWARM=1
      ;;
    *)
      CMP_INCLUDE_OUR=1
      CMP_INCLUDE_SWARM=1
      ;;
  esac
}

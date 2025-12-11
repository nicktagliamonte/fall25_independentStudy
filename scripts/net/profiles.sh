#!/usr/bin/env bash
set -euo pipefail

have_tc() { command -v tc >/dev/null 2>&1; }
have_ip() { command -v ip >/dev/null 2>&1; }
have_sudo() { command -v sudo >/dev/null 2>&1; }

# Track applied qdiscs for cleanup
PROFILE_STATE_DIR="/tmp/fall25_net_profiles"
mkdir -p "$PROFILE_STATE_DIR"

apply_profile() {
  local run_id="${1:-}"
  local profile="${2:-none}"
  local groups="${3:-}"
  local delay_ms="${4:-80}"
  local loss_pct="${5:-3}"
  local rate_mbit="${6:-0}"

  if [[ "$profile" == "none" || -z "$profile" ]]; then
    return 0
  fi

  if ! have_tc || ! have_ip; then
    echo "[net] ERROR: tc/ip not available; install iproute2 package" >&2
    return 1
  fi

  if ! have_sudo; then
    echo "[net] ERROR: sudo not available; network profiles require root access" >&2
    echo "[net] See docs/NET_PROFILES.md for manual steps" >&2
    return 1
  fi

  local state_file="$PROFILE_STATE_DIR/${run_id}.state"
  echo "$profile" > "$state_file"

  case "$profile" in
    wan)
      apply_wan_profile "$run_id" "$delay_ms" "$rate_mbit"
      ;;
    lossy)
      apply_lossy_profile "$run_id" "$delay_ms" "$loss_pct"
      ;;
    partition)
      apply_partition_profile "$run_id" "$groups"
      ;;
    *)
      echo "[net] ERROR: unknown profile '$profile'" >&2
      return 1
      ;;
  esac
}

apply_wan_profile() {
  local run_id="$1"
  local delay_ms="${2:-80}"
  local rate_mbit="${3:-0}"

  echo "[net] Applying WAN profile: delay=${delay_ms}ms"
  if [[ "$rate_mbit" -gt 0 ]]; then
    echo "[net]   rate=${rate_mbit}Mbit"
  fi

  # Apply netem to loopback interface
  # Note: This affects all traffic on loopback; for isolation use netns
  if sudo tc qdisc show dev lo | grep -q "netem"; then
    sudo tc qdisc del dev lo root 2>/dev/null || true
  fi

  if [[ "$rate_mbit" -gt 0 ]]; then
    sudo tc qdisc add dev lo root netem delay ${delay_ms}ms rate ${rate_mbit}mbit
  else
    sudo tc qdisc add dev lo root netem delay ${delay_ms}ms
  fi

  echo "[net] WAN profile applied (affects all loopback traffic)"
}

apply_lossy_profile() {
  local run_id="$1"
  local delay_ms="${2:-80}"
  local loss_pct="${3:-3}"

  echo "[net] Applying lossy profile: delay=${delay_ms}ms loss=${loss_pct}%"

  if sudo tc qdisc show dev lo | grep -q "netem"; then
    sudo tc qdisc del dev lo root 2>/dev/null || true
  fi

  sudo tc qdisc add dev lo root netem delay ${delay_ms}ms loss ${loss_pct}%

  echo "[net] Lossy profile applied (affects all loopback traffic)"
}

apply_partition_profile() {
  local run_id="$1"
  local groups="${2:-}"

  echo "[net] Applying partition profile: groups=$groups"
  echo "[net] WARNING: Partition profile requires network namespaces for proper isolation"
  echo "[net] Current implementation applies delay/loss to loopback (not true partition)"
  echo "[net] See docs/NET_PROFILES.md for manual netns-based partitioning"

  # For now, apply a high delay to simulate partition
  if sudo tc qdisc show dev lo | grep -q "netem"; then
    sudo tc qdisc del dev lo root 2>/dev/null || true
  fi

  sudo tc qdisc add dev lo root netem delay 500ms loss 10%

  echo "[net] Partition simulation applied (high delay/loss on loopback)"
}

clear_profile() {
  local run_id="${1:-}"

  if ! have_tc || ! have_sudo; then
    return 0
  fi

  local state_file="$PROFILE_STATE_DIR/${run_id}.state"
  if [[ ! -f "$state_file" ]]; then
    return 0
  fi

  echo "[net] Clearing network profile for run_id=$run_id"

  # Remove netem qdisc from loopback
  if sudo tc qdisc show dev lo | grep -q "netem"; then
    sudo tc qdisc del dev lo root 2>/dev/null || true
    echo "[net] Network profile cleared"
  fi

  rm -f "$state_file"
}

# Cleanup function for trap
cleanup_all_profiles() {
  echo "[net] Cleaning up all network profiles..."
  for state_file in "$PROFILE_STATE_DIR"/*.state; do
    if [[ -f "$state_file" ]]; then
      run_id=$(basename "$state_file" .state)
      clear_profile "$run_id"
    fi
  done
}

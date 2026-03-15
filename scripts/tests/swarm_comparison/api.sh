#!/usr/bin/env bash
# Purpose: Swarm/Bee HTTP API wrapper functions

# Don't use strict mode - functions handle their own errors
# This allows the script to be sourced without exiting on errors

# Default API address (Swarm v0.5.8 uses port 8500)
SWARM_API_ADDR="${SWARM_API_ADDR:-http://localhost:8500}"

# Upload a file to Swarm
# Usage: upload_file api_addr file_path [container_name]
# Returns: hash (Swarm reference)
# Swarm v0.5.8 HTTP API: POST to /bzz:/ with raw bytes returns content hash
upload_file() {
  local api_addr="${1:-$SWARM_API_ADDR}"
  local file_path="${2:-}"
  
  if [[ -z "$file_path" || ! -f "$file_path" ]]; then
    echo "ERROR: File not found: $file_path" >&2
    return 1
  fi
  
  # Strip trailing slash from api_addr for URL construction
  local base="${api_addr%/}"
  
  # Swarm v0.5.8 HTTP API: POST to /bzz:/ returns hex hash (per mainframe-swarm-guide)
  local hash
  hash=$(curl -sSf -m 120 -X POST \
    -H "Content-Type: application/octet-stream" \
    --data-binary "@$file_path" \
    "$base/bzz:/" 2>&1)
  
  # Response is raw hex string (64 chars for Swarm hash)
  if [[ "$hash" =~ ^[a-fA-F0-9]{64,}$ ]]; then
    echo "${hash:0:64}"
    return 0
  fi
  
  echo "ERROR: Swarm upload failed. Response: $hash" >&2
  return 1
}

# Note: Swarm v0.5.8 does not use postage stamps
# These functions are kept for compatibility but are no-ops
get_postage_batches() {
  echo "[]"
}

get_first_postage_batch() {
  echo ""
}

create_postage_batch() {
  echo "ERROR: Swarm v0.5.8 does not use postage stamps" >&2
  return 1
}

# Download a file from Swarm
# Usage: download_file api_addr hash output_path
# Returns: 0 on success, 1 on failure
download_file() {
  local api_addr="${1:-$SWARM_API_ADDR}"
  local hash="${2:-}"
  local output_path="${3:-}"
  
  if [[ -z "$hash" ]]; then
    echo "ERROR: Hash required" >&2
    return 1
  fi
  
  if [[ -z "$output_path" ]]; then
    echo "ERROR: Output path required" >&2
    return 1
  fi
  
  # Swarm v0.5.8 API: GET /bzz:/<hash>/ returns content directly
  if curl -sSfL -o "$output_path" "$api_addr/bzz:/$hash/" 2>/dev/null; then
    if [[ -f "$output_path" && -s "$output_path" ]]; then
      if ! grep -q "<a href=" "$output_path" 2>/dev/null; then
        return 0
      fi
    fi
  fi
  
  if curl -sSfL -o "$output_path" "$api_addr/bzz:/$hash" 2>/dev/null; then
    if [[ -f "$output_path" && -s "$output_path" ]]; then
      if ! grep -q "<a href=" "$output_path" 2>/dev/null; then
        return 0
      fi
    fi
  fi
  
  if curl -sSfL -o "$output_path" "$api_addr/bzz-raw:/$hash" 2>/dev/null; then
    if [[ -f "$output_path" && -s "$output_path" ]]; then
      return 0
    fi
  fi
  
  echo "ERROR: Failed to download hash $hash" >&2
  return 1
}

get_node_info() {
  local api_addr="${1:-$SWARM_API_ADDR}"
  curl -sSf "$api_addr/" 2>/dev/null || echo "{}"
}

get_metrics() {
  local api_addr="${1:-$SWARM_API_ADDR}"
  local metrics=$(curl -sSf "$api_addr/" 2>/dev/null || echo "{}")
  echo "$metrics" | jq -c ". + {status: \"ok\", peer_count: 0}" 2>/dev/null || echo "{\"status\": \"ok\"}"
}

get_peers() {
  echo "[]"
}

check_content() {
  local api_addr="${1:-$SWARM_API_ADDR}"
  local hash="${2:-}"
  
  if [[ -z "$hash" ]]; then
    echo "ERROR: Hash required" >&2
    return 1
  fi
  
  if curl -sSf -o /dev/null -w "%{http_code}" "$api_addr/bzz:/$hash" | grep -q "200" || \
     curl -sSf -o /dev/null -w "%{http_code}" "$api_addr/bzz-raw:/$hash" | grep -q "200"; then
    return 0
  else
    return 1
  fi
}

get_peer_id() {
  echo ""
}

get_overlay_address() {
  echo ""
}

get_peer_count() {
  local api_addr="${1:-$SWARM_API_ADDR}"
  get_peers "$api_addr" | jq 'length' 2>/dev/null || echo "0"
}

export -f upload_file
export -f download_file
export -f get_node_info
export -f get_metrics
export -f get_peers
export -f check_content
export -f get_peer_id
export -f get_overlay_address
export -f get_peer_count
export -f get_postage_batches
export -f get_first_postage_batch
export -f create_postage_batch

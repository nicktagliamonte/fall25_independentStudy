#!/usr/bin/env bash
# Purpose: Swarm/Bee HTTP API wrapper functions

# Don't use strict mode - functions handle their own errors
# This allows the script to be sourced without exiting on errors

# Default API address (Swarm v0.5.8 uses port 8500)
SWARM_API_ADDR="${SWARM_API_ADDR:-http://localhost:8500}"

# Upload a file to Swarm
# Usage: upload_file api_addr file_path [container_name]
# Returns: hash (Swarm reference)
# Note: Swarm v0.5.8 uses CLI tool 'swarm up' for uploads, not direct HTTP POST
upload_file() {
  local api_addr="${1:-$SWARM_API_ADDR}"
  local file_path="${2:-}"
  local container_name="${3:-}"
  
  if [[ -z "$file_path" || ! -f "$file_path" ]]; then
    echo "ERROR: File not found: $file_path" >&2
    return 1
  fi
  
  # Determine container name from API address if not provided
  if [[ -z "$container_name" ]]; then
    if [[ "$api_addr" == "http://172.20.0.200:8500" ]] || [[ "$api_addr" == *"172.20.0.200"* ]]; then
      container_name="swarm-bootstrap"
    elif [[ "$api_addr" == *"172.20.0."* ]]; then
      # Extract IP address part
      local ip_part=$(echo "$api_addr" | grep -oE '172\.20\.0\.([0-9]+)' | cut -d. -f4)
      if [[ "$ip_part" == "200" ]]; then
        container_name="swarm-bootstrap"
      elif [[ "$ip_part" =~ ^[0-9]+$ && $ip_part -ge 201 ]]; then
        container_name="swarm-node$((ip_part - 200))"
      else
        container_name="swarm-bootstrap"  # Default fallback
      fi
    else
      container_name="swarm-bootstrap"  # Default fallback
    fi
  fi
  
  # Find docker-compose file (could be docker-compose.swarm.yml or generated)
  local compose_file="docker-compose.swarm.yml"
  if [[ ! -f "$compose_file" ]]; then
    # Try to find it in current directory or script directory
    local script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    local root_dir="$(cd "$script_dir/../.." && pwd)"
    compose_file="$root_dir/docker-compose.swarm.yml"
  fi
  
  # Copy file into container, upload via CLI, then extract hash
  local temp_file="/tmp/swarm_upload_$(basename "$file_path")_$$"
  
  # Copy file to container using docker cp (more reliable than docker-compose exec)
  if ! docker cp "$file_path" "${container_name}:${temp_file}" 2>/dev/null; then
    echo "ERROR: Failed to copy file to container $container_name. Is the container running?" >&2
    return 1
  fi
  
  # Upload via swarm CLI tool
  local compose_cmd="docker-compose"
  if command -v docker-compose >/dev/null 2>&1; then
    compose_cmd="docker-compose"
  elif docker compose version >/dev/null 2>&1; then
    compose_cmd="docker compose"
  fi
  
  local hash=$($compose_cmd -f "$compose_file" exec -T "$container_name" \
    /app/swarm up "$temp_file" 2>&1 | grep -oE '[a-fA-F0-9]{64,}' | head -1 || echo "")
  
  # Clean up temp file in container
  $compose_cmd -f "$compose_file" exec -T "$container_name" \
    rm -f "$temp_file" 2>/dev/null || true
  
  if [[ -z "$hash" || ${#hash} -lt 64 ]]; then
    echo "ERROR: Failed to upload file via swarm CLI. Output may contain error messages." >&2
    return 1
  fi
  
  echo "$hash"
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
  # GET /bzz-raw:/<hash> returns manifest JSON
  # Try bzz endpoint with trailing slash first (direct content)
  if curl -sSfL -o "$output_path" "$api_addr/bzz:/$hash/" 2>/dev/null; then
    # Check if file was created and has content (not just HTML redirect)
    if [[ -f "$output_path" && -s "$output_path" ]]; then
      # Check if it's not an HTML redirect page
      if ! grep -q "<a href=" "$output_path" 2>/dev/null; then
        return 0
      fi
    fi
  fi
  
  # Try bzz endpoint without trailing slash (will follow redirect)
  if curl -sSfL -o "$output_path" "$api_addr/bzz:/$hash" 2>/dev/null; then
    if [[ -f "$output_path" && -s "$output_path" ]]; then
      if ! grep -q "<a href=" "$output_path" 2>/dev/null; then
        return 0
      fi
    fi
  fi
  
  # Try bzz-raw endpoint (returns manifest, but might work for some content)
  if curl -sSfL -o "$output_path" "$api_addr/bzz-raw:/$hash" 2>/dev/null; then
    if [[ -f "$output_path" && -s "$output_path" ]]; then
      # If it's JSON manifest, that's still a valid response
      return 0
    fi
  fi
  
  echo "ERROR: Failed to download hash $hash" >&2
  return 1
}

# Get node information (peer ID and status)
# Usage: get_node_info api_addr
# Returns: JSON with peer_id, overlay_address, underlay_addresses, etc.
get_node_info() {
  local api_addr="${1:-$SWARM_API_ADDR}"
  
  # Swarm v0.5.8 API: GET / (root endpoint) or GET /bzz-raw:/<hash> for content
  # Swarm doesn't have a dedicated /addresses endpoint like Bee
  # We'll return basic info from the root endpoint
  curl -sSf "$api_addr/" 2>/dev/null || echo "{}"
}

# Get node metrics/status
# Usage: get_metrics api_addr
# Returns: JSON with metrics
get_metrics() {
  local api_addr="${1:-$SWARM_API_ADDR}"
  
  # Swarm v0.5.8 API: GET / (root endpoint)
  # Swarm doesn't have dedicated /health or /status endpoints like Bee
  local metrics=$(curl -sSf "$api_addr/" 2>/dev/null || echo "{}")
  
  # Swarm v0.5.8 doesn't expose peer count via API easily
  # Return basic status
  echo "$metrics" | jq -c ". + {status: \"ok\", peer_count: 0}" 2>/dev/null || echo "{\"status\": \"ok\"}"
}

# Get peer connections
# Usage: get_peers api_addr
# Returns: JSON array of peers
get_peers() {
  local api_addr="${1:-$SWARM_API_ADDR}"
  
  # Swarm v0.5.8 doesn't expose peers via API like Bee does
  # Return empty array
  echo "[]"
}

# Check if content is available on a node
# Usage: check_content api_addr hash
# Returns: 0 if available, 1 if not
check_content() {
  local api_addr="${1:-$SWARM_API_ADDR}"
  local hash="${2:-}"
  
  if [[ -z "$hash" ]]; then
    echo "ERROR: Hash required" >&2
    return 1
  fi
  
  # Try to access content (HEAD request would be better but Swarm may not support it)
  if curl -sSf -o /dev/null -w "%{http_code}" "$api_addr/bzz:/$hash" | grep -q "200" || \
     curl -sSf -o /dev/null -w "%{http_code}" "$api_addr/bzz-raw:/$hash" | grep -q "200"; then
    return 0
  else
    return 1
  fi
}

# Helper: Extract peer ID from node info
# Usage: get_peer_id api_addr
# Note: Swarm v0.5.8 doesn't expose peer ID via API like Bee does
get_peer_id() {
  local api_addr="${1:-$SWARM_API_ADDR}"
  # Swarm v0.5.8 doesn't have a standard way to get peer ID via API
  # Return empty string - peer ID would need to be extracted from logs or config
  echo ""
}

# Helper: Extract overlay address from node info
# Usage: get_overlay_address api_addr
# Note: Swarm v0.5.8 doesn't expose overlay address via API like Bee does
get_overlay_address() {
  local api_addr="${1:-$SWARM_API_ADDR}"
  # Swarm v0.5.8 doesn't have a standard way to get overlay address via API
  echo ""
}

# Helper: Get peer count
# Usage: get_peer_count api_addr
get_peer_count() {
  local api_addr="${1:-$SWARM_API_ADDR}"
  get_peers "$api_addr" | jq 'length' 2>/dev/null || echo "0"
}

# Export functions for use in other scripts
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

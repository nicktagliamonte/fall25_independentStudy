#!/usr/bin/env bash
set -euo pipefail

# Purpose: Generate per-node Swarm configuration from TOML template
# Usage: ./scripts/docker/swarm/generate_config.sh [options]
#   --template <file>    TOML template file (default: config.toml.template)
#   --node-id <id>       Node identifier (e.g., swarm-node1, swarm-bootstrap)
#   --ip-address <ip>    Node IP address
#   --output-dir <dir>   Output directory for configs (default: /tmp/swarm_configs)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Default values
TEMPLATE_FILE="$SCRIPT_DIR/config.toml.template"
NODE_ID=""
IP_ADDRESS=""
OUTPUT_DIR="/tmp/swarm_configs"
GENERATE_ALL=false

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --template)
      TEMPLATE_FILE="$2"
      shift 2
      ;;
    --node-id)
      NODE_ID="$2"
      shift 2
      ;;
    --ip-address)
      IP_ADDRESS="$2"
      shift 2
      ;;
    --output-dir)
      OUTPUT_DIR="$2"
      shift 2
      ;;
    --generate-all)
      GENERATE_ALL=true
      shift
      ;;
    --help)
      echo "Usage: $0 [options]"
      echo "Options:"
      echo "  --template <file>    TOML template file (default: config.toml.template)"
      echo "  --node-id <id>       Node identifier (e.g., swarm-node1)"
      echo "  --ip-address <ip>    Node IP address"
      echo "  --output-dir <dir>   Output directory (default: /tmp/swarm_configs)"
      echo "  --generate-all       Generate configs for all nodes in docker-compose"
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      exit 1
      ;;
  esac
done

# Check if TOML parser is available (python3 with tomllib or tomli)
if ! python3 -c "import tomllib" 2>/dev/null && ! python3 -c "import tomli" 2>/dev/null; then
  echo "Warning: TOML parser not available. Using environment variable approach." >&2
  USE_TOML=false
else
  USE_TOML=true
fi

# Function to generate TOML config for a node
generate_toml_config() {
  local node_id="$1"
  local ip_address="$2"
  local http_port="${3:-8500}"
  local p2p_port="${4:-30399}"
  
  # Determine if this is bootstrap or regular node
  local is_bootstrap=false
  if [[ "$node_id" == "swarm-bootstrap" ]]; then
    is_bootstrap=true
  fi
  
  # Generate bootnode address if this is not bootstrap
  local bootnodes="[]"
  if [[ "$is_bootstrap" == "false" ]]; then
    # Bootstrap is at 172.20.0.200:30399
    bootnodes="[\"enode://PLACEHOLDER_PEER_ID@172.20.0.200:30399\"]"
  fi
  
  cat <<EOF
# Swarm v0.5.8 Configuration for $node_id
# Generated: $(date)
# IP Address: $ip_address

[swarm]
data_dir = "/app/data"

[swarm.api]
http_addr = "0.0.0.0"
http_port = $http_port

[swarm.p2p]
p2p_port = $p2p_port
bootnodes = $bootnodes

[swarm.account]
password = "swarm-test-password"
bzz_account = ""

[swarm.logging]
verbosity = 4
debug = false

[swarm.network]
network_id = ""

[node_overrides]
node_id = "$node_id"
ip_address = "$ip_address"
http_port = $http_port
EOF
}

# Function to generate environment variables from config
generate_env_config() {
  local node_id="$1"
  local ip_address="$2"
  local http_port="${3:-8500}"
  
  # Determine if this is bootstrap or regular node
  local is_bootstrap=false
  if [[ "$node_id" == "swarm-bootstrap" ]]; then
    is_bootstrap=true
  fi
  
  # Generate bootnode address if this is not bootstrap
  local bootnode=""
  if [[ "$is_bootstrap" == "false" ]]; then
    # Bootstrap is at 172.20.0.200:30399
    bootnode="enode://PLACEHOLDER_PEER_ID@172.20.0.200:30399"
  fi
  
  # Output environment variables in format suitable for docker-compose
  cat <<EOF
# Generated config for $node_id
SWARM_DATA_DIR=/app/data
SWARM_HTTP_ADDR=0.0.0.0:$http_port
SWARM_HTTP_PORT=$http_port
SWARM_PASSWORD=swarm-test-password
SWARM_VERBOSITY=4
SWARM_DEBUG=false
EOF

  if [[ -n "$bootnode" ]]; then
    echo "SWARM_BOOTNODE=$bootnode"
  fi
}

# Function to generate config files (both TOML and .env)
generate_config_file() {
  local node_id="$1"
  local ip_address="$2"
  local http_port="${3:-8500}"
  local p2p_port="${4:-30399}"
  
  mkdir -p "$OUTPUT_DIR"
  
  # Generate TOML config
  local toml_file="$OUTPUT_DIR/${node_id}.toml"
  generate_toml_config "$node_id" "$ip_address" "$http_port" "$p2p_port" > "$toml_file"
  echo "Generated TOML: $toml_file"
  
  # Generate .env file (for docker-compose)
  local env_file="$OUTPUT_DIR/${node_id}.env"
  echo "# Swarm configuration for $node_id" > "$env_file"
  echo "# Generated: $(date)" >> "$env_file"
  echo "# IP Address: $ip_address" >> "$env_file"
  echo "" >> "$env_file"
  generate_env_config "$node_id" "$ip_address" "$http_port" >> "$env_file"
  echo "Generated ENV: $env_file"
}

# Generate configs for all nodes if requested
if [[ "$GENERATE_ALL" == "true" ]]; then
  echo "Generating configs for all Swarm nodes..."
  
  # Bootstrap node
  generate_config_file "swarm-bootstrap" "172.20.0.200" "8500" "30399"
  
  # Regular nodes (assuming up to 10 nodes)
  for i in $(seq 1 10); do
    local ip_last=$((200 + i))
    generate_config_file "swarm-node${i}" "172.20.0.${ip_last}" "8500" "30399"
  done
  
  echo ""
  echo "All configs generated in: $OUTPUT_DIR"
  echo "  - TOML files: *.toml (documentation/reference)"
  echo "  - ENV files: *.env (for docker-compose)"
  
# Generate single node config
elif [[ -n "$NODE_ID" ]]; then
  if [[ -z "$IP_ADDRESS" ]]; then
    echo "Error: --ip-address required when generating single node config" >&2
    exit 1
  fi
  
  generate_config_file "$NODE_ID" "$IP_ADDRESS" "8500" "30399"
  
else
  echo "Error: Must provide --node-id/--ip-address or --generate-all" >&2
  exit 1
fi

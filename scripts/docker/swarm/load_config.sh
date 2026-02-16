#!/usr/bin/env bash
# Purpose: Load Swarm configuration from TOML or environment variables
# Usage: source scripts/docker/swarm/load_config.sh [config_file]
#   Then use variables like $SWARM_DATA_DIR, $SWARM_HTTP_PORT, etc.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG_FILE="${1:-$SCRIPT_DIR/config.toml.template}"

# Default values (matching entrypoint.sh defaults)
SWARM_DATA_DIR="${SWARM_DATA_DIR:-/app/data}"
SWARM_HTTP_ADDR="${SWARM_HTTP_ADDR:-0.0.0.0:8500}"
SWARM_HTTP_PORT="${SWARM_HTTP_PORT:-8500}"
SWARM_PASSWORD="${SWARM_PASSWORD:-swarm-test-password}"
SWARM_BOOTNODE="${SWARM_BOOTNODE:-}"
SWARM_BZZ_ACCOUNT="${SWARM_BZZ_ACCOUNT:-}"
SWARM_VERBOSITY="${SWARM_VERBOSITY:-4}"
SWARM_DEBUG="${SWARM_DEBUG:-false}"
SWARM_P2P_PORT="${SWARM_P2P_PORT:-30399}"

# Try to load from TOML if file exists and parser is available
if [[ -f "$CONFIG_FILE" ]]; then
  if python3 -c "import tomllib" 2>/dev/null || python3 -c "import tomli" 2>/dev/null; then
    # Parse TOML and set environment variables
    python3 <<PYTHON_SCRIPT
import sys
import json

try:
    import tomllib
except ImportError:
    import tomli as tomllib

with open("$CONFIG_FILE", "rb") as f:
    config = tomllib.load(f)

swarm = config.get("swarm", {})
api = swarm.get("api", {})
p2p = swarm.get("p2p", {})
account = swarm.get("account", {})
logging = swarm.get("logging", {})

# Output as shell variable assignments
if "data_dir" in swarm:
    print(f"export SWARM_DATA_DIR=\"{swarm['data_dir']}\"")

if "http_addr" in api:
    print(f"export SWARM_HTTP_ADDR=\"{api['http_addr']}\"")

if "http_port" in api:
    print(f"export SWARM_HTTP_PORT=\"{api['http_port']}\"")

if "p2p_port" in p2p:
    print(f"export SWARM_P2P_PORT=\"{p2p['p2p_port']}\"")

if "bootnodes" in p2p and p2p["bootnodes"]:
    print(f"export SWARM_BOOTNODE=\"{','.join(p2p['bootnodes'])}\"")

if "password" in account:
    print(f"export SWARM_PASSWORD=\"{account['password']}\"")

if "bzz_account" in account and account["bzz_account"]:
    print(f"export SWARM_BZZ_ACCOUNT=\"{account['bzz_account']}\"")

if "verbosity" in logging:
    print(f"export SWARM_VERBOSITY=\"{logging['verbosity']}\"")

if "debug" in logging:
    print(f"export SWARM_DEBUG=\"{str(logging['debug']).lower()}\"")
PYTHON_SCRIPT
  fi
fi

# Export all variables
export SWARM_DATA_DIR
export SWARM_HTTP_ADDR
export SWARM_HTTP_PORT
export SWARM_PASSWORD
export SWARM_BOOTNODE
export SWARM_BZZ_ACCOUNT
export SWARM_VERBOSITY
export SWARM_DEBUG
export SWARM_P2P_PORT

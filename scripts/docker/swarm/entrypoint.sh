#!/bin/sh
set -e

# Purpose: Entrypoint script for Swarm v0.5.8 node

DATA_DIR="${SWARM_DATA_DIR:-/app/data}"
HTTP_ADDR="${SWARM_HTTP_ADDR:-0.0.0.0:8500}"
HTTP_PORT="${SWARM_HTTP_PORT:-8500}"
BOOTNODE="${SWARM_BOOTNODE:-}"
PASSWORD="${SWARM_PASSWORD:-swarm-test-password}"
BZZ_ACCOUNT="${SWARM_BZZ_ACCOUNT:-}"

# Create password file if it doesn't exist
PASSWORD_FILE="$DATA_DIR/password"
if [ ! -f "$PASSWORD_FILE" ]; then
  mkdir -p "$DATA_DIR"
  echo "$PASSWORD" > "$PASSWORD_FILE"
  chmod 600 "$PASSWORD_FILE"
fi

# Parse HTTP_ADDR if provided (format: host:port or :port)
if [[ "$HTTP_ADDR" == *:* ]]; then
  HTTP_HOST="${HTTP_ADDR%%:*}"
  HTTP_PORT="${HTTP_ADDR##*:}"
  # Handle :port format (empty host means bind to all interfaces)
  if [ -z "$HTTP_HOST" ]; then
    HTTP_HOST="0.0.0.0"
  fi
else
  # Assume it's just a port number
  HTTP_PORT="$HTTP_ADDR"
  HTTP_HOST="0.0.0.0"
fi

# Build Swarm command
CMD="/app/swarm"

# Add data directory
CMD="$CMD --datadir $DATA_DIR"

# Add password file
CMD="$CMD --password $PASSWORD_FILE"

# Add HTTP API address and port separately
# Swarm v0.5.8 uses --httpaddr for interface and --bzzport for port
CMD="$CMD --httpaddr $HTTP_HOST"
CMD="$CMD --bzzport $HTTP_PORT"

# Add BZZ account if specified (required if multiple accounts exist)
if [ -n "$BZZ_ACCOUNT" ]; then
  CMD="$CMD --bzzaccount $BZZ_ACCOUNT"
fi

# Add bootnode if provided (Swarm uses --bootnodes, plural)
if [ -n "$BOOTNODE" ]; then
  CMD="$CMD --bootnodes $BOOTNODE"
fi

# Add verbosity if set
if [ -n "$SWARM_VERBOSITY" ]; then
  CMD="$CMD --verbosity $SWARM_VERBOSITY"
fi

# Add debug flag if set
if [ "${SWARM_DEBUG:-}" = "true" ]; then
  CMD="$CMD --debug"
fi

echo "Starting Swarm v0.5.8 node..."
echo "Command: $CMD"
echo "Data directory: $DATA_DIR"
echo "HTTP API address: $HTTP_ADDR"
if [ -n "$BOOTNODE" ]; then
  echo "Bootnode: $BOOTNODE"
fi
if [ -n "$BZZ_ACCOUNT" ]; then
  echo "BZZ Account: $BZZ_ACCOUNT"
fi

exec $CMD


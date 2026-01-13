#!/usr/bin/env bash
set -euo pipefail

# Purpose: View logs from Docker nodes
# Usage: ./scripts/docker/logs.sh [service] [--follow]
#   service: specific service name (bootstrap, node2, etc.) or omit for all
#   --follow: follow log output

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

SERVICE="${1:-}"
FOLLOW="${2:-}"

if [[ -n "$SERVICE" ]]; then
  if [[ "$FOLLOW" == "--follow" ]] || [[ "$SERVICE" == "--follow" ]]; then
    docker-compose logs -f "$SERVICE"
  else
    docker-compose logs "$SERVICE"
  fi
else
  if [[ "$FOLLOW" == "--follow" ]] || [[ "$SERVICE" == "--follow" ]]; then
    docker-compose logs -f
  else
    docker-compose logs
  fi
fi

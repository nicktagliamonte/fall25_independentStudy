#!/usr/bin/env bash
set -euo pipefail

# Purpose: Stop all Docker nodes
# Usage: ./scripts/docker/stop.sh

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

echo "Stopping all Docker nodes..."
docker-compose down

echo "Done!"

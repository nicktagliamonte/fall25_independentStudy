#!/usr/bin/env bash
set -euo pipefail

# Purpose: Clean up all Docker resources (containers, volumes, networks)
# Usage: ./scripts/docker/clean.sh

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

echo "Stopping all containers..."
docker-compose down -v 2>/dev/null || true

echo "Removing any remaining containers..."
docker ps -a --filter "name=fall25-" --format "{{.Names}}" | xargs -r docker rm -f 2>/dev/null || true

echo "Removing volumes..."
docker volume ls --filter "name=fall25" --format "{{.Name}}" | xargs -r docker volume rm 2>/dev/null || true
docker volume ls --filter "name=^bootstrap-" --format "{{.Name}}" | xargs -r docker volume rm 2>/dev/null || true
docker volume ls --filter "name=^node" --format "{{.Name}}" | xargs -r docker volume rm 2>/dev/null || true

echo "Removing network..."
docker network rm fall25_independentstudy_node-network 2>/dev/null || true

echo "Done! All Docker resources cleaned up."

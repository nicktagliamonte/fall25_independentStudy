#!/usr/bin/env bash
# Purpose: Resolve Swarm HTTP base URL from docker-published port on swarm-bootstrap (host loopback).

swarm_publish_base_url() {
  if docker ps --format '{{.Names}}' 2>/dev/null | grep -q '^swarm-bootstrap$'; then
    local line
    line=$(docker port swarm-bootstrap 8500 2>/dev/null | head -1 || true)
    [[ -z "$line" ]] && line=$(docker port swarm-bootstrap 8500/tcp 2>/dev/null | head -1 || true)
    if [[ -n "$line" ]]; then
      local port
      port=$(echo "$line" | grep -oE '[0-9]+$' | head -1)
      if [[ -n "$port" ]]; then
        echo "http://127.0.0.1:${port}"
        return 0
      fi
    fi
  fi
  echo "http://127.0.0.1:8500"
}

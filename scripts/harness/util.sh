#!/usr/bin/env bash
set -euo pipefail

rand_port() {
  local port
  while :; do
    port=$(( ( RANDOM % 10000 ) + 20000 ))
    local in_use=0
    
    if command -v lsof >/dev/null 2>&1; then
      # macOS/Linux with lsof
      if lsof -iTCP:$port -sTCP:LISTEN -n -P >/dev/null 2>&1; then in_use=1; fi
    elif command -v ss >/dev/null 2>&1; then
      # Linux with ss
      if ss -ltn "( sport = :$port )" | grep -q ":$port"; then in_use=1; fi
    else
      # Fallback to netstat
      if netstat -an | grep -q -e "\.$port " -e ":$port " -e "\.$port$"; then in_use=1; fi
    fi
    
    if [ "$in_use" -eq 0 ]; then
      echo "$port"
      return 0
    fi
  done
}


wait_http() {
  local url="$1"
  local timeout="${2:-10}"
  local start ts
  start=$(date +%s)
  while :; do
    if curl -sSf -m 1 "http://$url/health" >/dev/null 2>&1; then
      return 0
    fi
    ts=$(date +%s)
    if (( ts - start >= timeout )); then
      echo "wait_http timeout for $url" >&2
      return 1
    fi
    sleep 0.2
  done
}

write_json() {
  local path="$1"
  local content="$2"
  mkdir -p "$(dirname "$path")"
  printf "%s\n" "$content" > "$path"
}



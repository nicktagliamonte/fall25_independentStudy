#!/usr/bin/env bash
set -euo pipefail

# Purpose: Ensure docker-compose.swarm.yml has no invalid IPv4 last octets (post start.sh generation).
# Usage: verify_generated_ips.sh [path/to/docker-compose.swarm.yml]

COMPOSE="${1:-}"
if [[ -z "$COMPOSE" ]]; then
  ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
  COMPOSE="$ROOT_DIR/docker-compose.swarm.yml"
fi
[[ -f "$COMPOSE" ]] || { echo "No file: $COMPOSE" >&2; exit 1; }

bad=0
while read -r line; do
  ip=$(echo "$line" | sed -n 's/.*ipv4_address:[[:space:]]*\([0-9.]*\).*/\1/p')
  [[ -z "$ip" ]] && continue
  IFS='.' read -r o1 o2 o3 o4 <<< "$ip"
  for o in "$o1" "$o2" "$o3" "$o4"; do
    if [[ "$o" =~ ^[0-9]+$ ]] && [[ "$o" -le 255 ]]; then
      continue
    fi
    echo "Invalid IP: $ip (line: $line)" >&2
    bad=$((bad + 1))
    break
  done
done < <(grep 'ipv4_address:' "$COMPOSE" || true)

if [[ "$bad" -gt 0 ]]; then
  echo "verify_generated_ips: $bad invalid address(es) in $COMPOSE" >&2
  exit 1
fi
echo "verify_generated_ips: OK ($COMPOSE)"
exit 0

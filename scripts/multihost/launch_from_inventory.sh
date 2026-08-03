#!/usr/bin/env bash
set -euo pipefail

inventory=${1:?usage: launch_from_inventory.sh INVENTORY.csv}
revision=$(git rev-parse HEAD)

mapfile -t rows < <(awk -F, 'NF && $1 !~ /^#/ {print}' "$inventory")
if (( ${#rows[@]} < 2 || ${#rows[@]} > 5 )); then
  echo "inventory must contain 2-5 hosts" >&2
  exit 1
fi

for row in "${rows[@]}"; do
  IFS=, read -r host user listen_ip advertise_ip control_port peer_port workdir <<<"$row"
  target="$user@$host"
  ssh "$target" "test -x '$workdir/bin/node'"
  ssh "$target" "mkdir -p '$workdir/multihost-data' '$workdir/multihost-logs'"
  ssh "$target" "nohup '$workdir/bin/node' run --listen '/ip4/$listen_ip/tcp/$peer_port' --advertise '/ip4/$advertise_ip/tcp/$peer_port' --key '$workdir/multihost-data/node.key' --store '$workdir/multihost-data/store' --control '$workdir/multihost-logs/control.json' --log '$workdir/multihost-logs/node.log' --no-default-bootstrap --cluster-nodes '${#rows[@]}' --min-outbound 1 --max-connections 8 >'$workdir/multihost-logs/launcher.log' 2>&1 &"
done

first=${rows[0]}
IFS=, read -r first_host first_user _ first_advertise first_control first_peer first_workdir <<<"$first"
first_target="$first_user@$first_host"
for _ in $(seq 1 60); do
  first_id=$(ssh "$first_target" "test -f '$first_workdir/multihost-logs/control.json' && addr=\$(jq -r .addr '$first_workdir/multihost-logs/control.json') && curl -sf \"http://\$addr/id\" | jq -r .peer" 2>/dev/null || true)
  [[ -n "$first_id" && "$first_id" != null ]] && break
  sleep 1
done
[[ -n "${first_id:-}" && "$first_id" != null ]] || { echo "first host did not become ready" >&2; exit 1; }

for row in "${rows[@]:1}"; do
  IFS=, read -r host user _ _ _ _ workdir <<<"$row"
  target="$user@$host"
  ssh "$target" "addr=\$(jq -r .addr '$workdir/multihost-logs/control.json'); curl -fsS -H 'Content-Type: application/json' -d '{\"addr\":\"/ip4/$first_advertise/tcp/$first_peer\",\"peer\":\"$first_id\",\"timeout\":\"20s\",\"protect\":true}' \"http://\$addr/connect\" >/dev/null"
done

echo "launched revision $revision on ${#rows[@]} inventory hosts; bootstrap peer $first_id"

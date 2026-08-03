#!/usr/bin/env bash
set -euo pipefail

inventory=${1:?usage: smoke_check.sh INVENTORY.csv EVIDENCE_DIR}
evidence=${2:?usage: smoke_check.sh INVENTORY.csv EVIDENCE_DIR}
mkdir -p "$evidence"
git rev-parse HEAD > "$evidence/git-revision.txt"
cp "$inventory" "$evidence/inventory.csv"

index=0
while IFS=, read -r host user _ _ _ _ workdir; do
  [[ -z "$host" || "$host" == \#* ]] && continue
  index=$((index+1)); target="$user@$host"
  ssh "$target" "addr=\$(jq -r .addr '$workdir/multihost-logs/control.json'); curl -fsS \"http://\$addr/health\"" > "$evidence/host${index}-health.txt"
  ssh "$target" "addr=\$(jq -r .addr '$workdir/multihost-logs/control.json'); curl -fsS \"http://\$addr/id\"" > "$evidence/host${index}-id.json"
  ssh "$target" "addr=\$(jq -r .addr '$workdir/multihost-logs/control.json'); curl -fsS \"http://\$addr/neighbors\"" > "$evidence/host${index}-neighbors.json"
  ssh "$target" "tail -n 500 '$workdir/multihost-logs/node.log'" > "$evidence/host${index}-node.log"
done < "$inventory"

cat > "$evidence/CHECKLIST.md" <<'EOF'
# Multi-host evidence checklist

- [ ] All hosts report the frozen Git revision and expected binary digest.
- [ ] Advertised addresses in `/id` exactly match the inventory.
- [ ] A private named object reaches its configured provider target.
- [ ] Resolve and retrieval reproduce the source SHA-256 digest.
- [ ] One provider process is stopped and the crash time recorded.
- [ ] Retrieval after the crash reproduces the source digest.
- [ ] Repair returns every manifest/chunk to its policy target.
- [ ] Logs, request JSON, timings, and replica status are preserved.
- [ ] No regional, geographic, or administrative-independence claim is made.
EOF

echo "captured topology evidence in $evidence; complete the crash/retrieval checklist manually"

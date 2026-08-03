# Explicit-inventory multi-host smoke test

This path is deliberately limited to 3–5 independently administered hosts.
It makes no cloud, region, or failure-domain assumption. Every node binds a
local listener and advertises the inventory address that the other hosts can
actually dial; no container hairpin or public-IP discovery is used.

1. Copy `inventory.example.csv` and fill 3–5 rows. Install the same frozen
   `bin/node` on every row's `workdir`.
2. Run `./scripts/multihost/launch_from_inventory.sh inventory.csv`.
3. Run `./scripts/multihost/smoke_check.sh inventory.csv EVIDENCE_DIR`.
4. Preserve the inventory, `git-revision.txt`, node IDs, health/neighbor JSON,
   create/update/resolve output, replica status, crash timestamp, post-crash
   retrieval digest, and logs in the evidence directory.

The smoke test is evidence of cross-host crash/retrieval only. It is not
evidence of regional independence, geographic diversity, Byzantine agreement,
or arbitrary-partition availability.

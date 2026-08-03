# Tarsus

Tarsus is a peer-to-peer storage prototype providing globally addressable,
searchable mutable names over immutable, encrypted, content-addressed data.
Each signed name has independent replication, RTT-placement, authorization,
encryption, retention, searchability, locking, versioning, and deletion
policy. The integrated Linda-style tuple space remains the coordination plane
for offers, work, repair, and application queues.

The tuple plane supports:

- `put(name, value)` to publish one tuple instance;
- `read(pattern)` to return one match without consuming it; and
- `get(pattern)` to atomically remove and return one match at its exact-name
  owner.

Exact tuple names have deterministic Kademlia owners. Prefix and substring
queries use hash-sharded Prefix Hash Trees and Bloom summaries to discover
candidate names, then contact exact owners for authoritative verification.
Tuple metadata and provider locations remain separate from bulk content, which
is fetched directly and verified against its content key.

## Build and test

Requirements are Go, Docker, Docker Compose, `jq`, and `curl`.

```bash
go build ./cmd/node
go build ./cmd/tarsusctl
go test ./...
go vet ./...
```

The highest-risk coordination packages can also be checked with the race
detector:

```bash
go test -race ./internal/tuplespace ./internal/pht ./internal/gateway
```

## Local cluster

Start a fresh private cluster with:

```bash
./scripts/docker/start.sh 10
```

The script generates `docker-compose.yml`, builds the node image, disables
public bootstrap peers, and establishes a protected bounded-degree topology.
Use `scripts/docker/status.sh`, `logs.sh`, and `stop.sh` to inspect or stop it.

## Experiments

The production experiment harness is in
[`scripts/tests/tarsus_campaign`](scripts/tests/tarsus_campaign/README.md). It
generates versioned, resumable cells, records request-scoped mutation and query
counters, and validates every accepted artifact before analysis.

The prior tuple-centered manuscript is preserved as
[`paper/tupleFinal.tex`](paper/tupleFinal.tex). The mutable-name manuscript is
[`paper/final.tex`](paper/final.tex).

```bash
python3 scripts/tests/tarsus_campaign/analyze_campaign.py RUN_DIR
python3 scripts/tests/tarsus_campaign/plot_campaign.py RUN_DIR paper/figures
```

## Documentation

- [`docs/API.md`](docs/API.md): HTTP storage API
- [`docs/MUTABLE_NAMES.md`](docs/MUTABLE_NAMES.md): signed record and consistency specification
- [`docs/THREAT_MODEL.md`](docs/THREAT_MODEL.md): guarantees and explicit boundaries
- [`docs/GATEWAY_QUERY_API.md`](docs/GATEWAY_QUERY_API.md): tuple query path
- [`docs/REPLICATION.md`](docs/REPLICATION.md): placement and repair
- [`docs/TOKEN_PROTOCOL.md`](docs/TOKEN_PROTOCOL.md): provider-location records
- [`docs/TARSUS_REWRITE_CHANGES.txt`](docs/TARSUS_REWRITE_CHANGES.txt): brief
  implementation-change summary

The raw `/put` and `/get` content APIs remain available. Named-object clients
use `/v1/names/*` or `tarsusctl object ...`; chunking, encryption, signing, and
reconstruction happen client-side. Tarsus is not a POSIX file system and does
not claim consensus availability during arbitrary network partitions.

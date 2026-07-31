# Tarsus

Tarsus is a peer-to-peer, content-addressed storage prototype with an
integrated Linda-style tuple space. It is an independent implementation; it
does not embed or depend on Linda or IPFS.

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

The manuscript source is [`paper/final.tex`](paper/final.tex). The campaign
plotter regenerates its figures from a validated run:

```bash
python3 scripts/tests/tarsus_campaign/analyze_campaign.py RUN_DIR
python3 scripts/tests/tarsus_campaign/plot_campaign.py RUN_DIR paper/figures
```

## Documentation

- [`docs/API.md`](docs/API.md): HTTP storage API
- [`docs/GATEWAY_QUERY_API.md`](docs/GATEWAY_QUERY_API.md): tuple query path
- [`docs/REPLICATION.md`](docs/REPLICATION.md): placement and repair
- [`docs/TOKEN_PROTOCOL.md`](docs/TOKEN_PROTOCOL.md): provider-location records
- [`docs/TARSUS_REWRITE_CHANGES.txt`](docs/TARSUS_REWRITE_CHANGES.txt): brief
  implementation-change summary

Tarsus currently provides immutable content objects and tuple coordination. It
is not a POSIX file system and does not claim consensus availability during
arbitrary network partitions.

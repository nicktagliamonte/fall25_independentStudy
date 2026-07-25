# Node CLI Reference

The `node` binary (`cmd/node`, entry point `pkg/node.Run`) is a single executable dispatching on its first argument as a subcommand: `node <subcommand> [flags]`. Most subcommands that talk to a running node accept `--control` (default `/tmp/fall25_node/daemon.json`) pointing at the control-file written by `run --daemon`, and either operate against that running daemon (`--daemon` flag) or spin up an inline, one-shot node for the single operation.

This reference covers the flags as implemented in `pkg/node/run.go`; see `docs/API.md` for the underlying HTTP control-server endpoints these commands wrap once a node is running.

---

## run

Starts a node (foreground or backgrounded) and keeps it alive as a peer, serving the control-server HTTP API.

| Flag | Default | Purpose |
|---|---|---|
| `--listen` | (none) | Multiaddr to listen on (repeatable). |
| `--daemon` | `false` | Run in the background and return immediately (writes `--control`). |
| `--log` | `""` | When backgrounding, append logs to this file. |
| `--control` | `/tmp/fall25_node/daemon.json` | Path to write the control-endpoint file. |
| `--key` | `""` | Path to a persistent private key (optional; generates one if unset). |
| `--store` | `""` | Path to a persistent blockstore (optional; ephemeral in-memory store if unset). |
| `--seed` | (none) | Seed peer multiaddr (repeatable). |
| `--seed-file` | `""` | Path to a file of seed multiaddrs, one per line. |
| `--min-outbound` | `DefaultMinOutbound` | Target minimum outbound connections (see `docs/REPLICATION.md`'s dial-maintenance section). |
| `--cluster-nodes` | `0` | Expected cluster size N; caps `--min-outbound` at N−1. `0` uses `CLUSTER_NODE_COUNT` env or peerstore size. |
| `--dial-timeout` | `10s` | Per-dial timeout. |
| `--stale-age` | `24h` | Peers considered stale (candidates for eviction) after this long unseen. |
| `--max-fail` | `8` | Evict a peer after this many consecutive dial failures. |
| `--max-known` | `5000` | Soft cap on peers tracked in the `PeerStore`. |
| `--per-ip-dial-limit` | `3` | Maximum concurrent outbound dials to a single IP. |

## put

Stores a block, either inline (spins up a temporary node) or against a running daemon.

| Flag | Default | Purpose |
|---|---|---|
| `--listen` | (none) | Multiaddr to listen on (repeatable, inline mode only). |
| `--data` | `""` | Inline data to store as a block. |
| `--file` | `""` | Path to a file to store as a block (alternative to `--data`). |
| `--serve` | `false` | Keep the node running afterward to serve inbound DirectFetch requests. |
| `--control` | `/tmp/fall25_node/daemon.json` | Daemon control file (used with `--daemon`). |
| `--daemon` | `false` | Use the running daemon at `--control` instead of an inline node. |
| `--http-debug` | `""` | Optional `host:port` to serve a `/cid/<cid>` debug HTTP handler. |

## get

Fetches a block by CID from a specific provider (peer-to-peer), or via a running daemon.

| Flag | Default | Purpose |
|---|---|---|
| `--listen` | (none) | Multiaddr to listen on (repeatable, inline mode only). |
| `--cid` | `""` | Content ID to fetch. |
| `--from-addr` | `""` | Provider's multiaddr. |
| `--from-peer` | `""` | Provider's peer ID. |
| `--timeout` | `20s` | Fetch timeout. |
| `--control` | `/tmp/fall25_node/daemon.json` | Daemon control file. |
| `--daemon` | `false` | Use the running daemon instead of an inline node. |
| `--out` | `""` | Write fetched bytes to this file (otherwise prints/discards). |

## connect

Dials a peer from a running (or inline) node.

| Flag | Default | Purpose |
|---|---|---|
| `--listen` | (none) | Multiaddr to listen on (repeatable, inline mode only). |
| `--addr` | `""` | Remote peer's multiaddr. |
| `--peer` | `""` | Remote peer ID. |
| `--timeout` | `10s` | Dial timeout. |
| `--control` | `/tmp/fall25_node/daemon.json` | Daemon control file. |
| `--daemon` | `false` | Use the running daemon instead of an inline node. |

## snapshot

Lists locally-indexed block CIDs (wraps `GET /snapshot`, see `docs/API.md`).

| Flag | Default | Purpose |
|---|---|---|
| `--control` | `/tmp/fall25_node/daemon.json` | Daemon control file. |
| `--limit` | `1000` | Max CIDs to return. |
| `--cursor` | `""` | Pagination cursor (start after this value). |

## neighbors

Prints the running daemon's current libp2p connections (wraps `GET /neighbors`; despite the name, this is live connections, not a DHT routing-table view — see `docs/API.md`).

| Flag | Default | Purpose |
|---|---|---|
| `--control` | `/tmp/fall25_node/daemon.json` | Daemon control file. |

## keygen

Generates a new libp2p private key and writes it to a file (for later use with `run --key`), without starting a node.

| Flag | Default | Purpose |
|---|---|---|
| `--out` | `""` | Path to write the generated private key (PEM). |

## lookup-key

Performs an isolated DHT token lookup (`GetToken` only, no block fetch) directly against a bootstrap peer, without needing a running daemon — used by the comparison-test harness to measure lookup hop count/latency (see `docs/REPLICATION.md`'s hop-count section).

| Flag | Default | Purpose |
|---|---|---|
| `--bootstrap` | `""` | Bootstrap peer multiaddr(s), comma-separated; extra peers speed up cold routing-table fill. |
| `--key` | `""` | 64-hex-char key to look up. |
| `--timeout` | `30s` | Deadline for the `GetToken` lookup itself (connect and DHT bootstrap use separate budgets). |

## restore

Re-fetches a list of blocks by CID from the network into local storage (wraps `POST /restore`, see `docs/API.md`).

| Flag | Default | Purpose |
|---|---|---|
| `--manifest` | `""` | Path to a file of CIDs (one per line), or a single CID directly. |
| `--concurrency` | `4` | Parallel fetches. |
| `--timeout` | `20s` | Per-CID timeout. |
| `--control` | `/tmp/fall25_node/daemon.json` | Daemon control file. |

## shutdown

Gracefully stops a running daemon (wraps `POST /shutdown`).

| Flag | Default | Purpose |
|---|---|---|
| `--control` | `/tmp/fall25_node/daemon.json` | Daemon control file. |

---

## See also

- `docs/API.md` — the HTTP control-server endpoints most of these subcommands wrap once a node is running.
- `docs/LOCAL_MULTI_NODE.md` — a worked example driving several of these subcommands (`make local`, `/connect`, `/neighbors`) across a local multi-node cluster.
- `docs/REPLICATION.md` — background on `--min-outbound`/`--cluster-nodes` dial-maintenance tuning.

# fall25_independentStudy

## Symmetric node CLI

Build the symmetric node:

```bash
go build ./cmd/node
```

### Run a serving node

```bash
./node run --listen "/ip4/0.0.0.0/tcp/0" --listen "/ip4/0.0.0.0/udp/0/quic-v1"
```

Prints your `PeerID` and `Addr` entries. Keep this running to serve inbound Bitswap wants.

### Put a block (optionally keep serving)

```bash
./node put --data "hello from A" --serve \
  --listen "/ip4/0.0.0.0/tcp/0" --listen "/ip4/0.0.0.0/udp/0/quic-v1"
```

Outputs `CID` and multihash hex, plus local `PeerID` and addrs.

You can also provide a file:

```bash
./node put --file ./path/to/file.bin --serve
```

### Connect to a peer

```bash
./node connect --addr "<peer_multiaddr>" --peer "<peer_id>" --timeout 10s
```

### Get a block from a peer

```bash
./node get \
  --cid "<cid>" \
  --from-addr "<peer_multiaddr>" \
  --from-peer "<peer_id>" \
  --timeout 20s
```

### A ↔ B demo (two terminals)

1. Terminal A: run or put-serve
   ```bash
   ./node put --data "hello from A" --serve
   # Note CID_A, A_peerID, and an A_addr printed above
   ```
2. Terminal B: fetch from A
   ```bash
   ./node get --cid CID_A --from-addr A_addr --from-peer A_peerID
   ```
3. Terminal B: put-serve
   ```bash
   ./node put --data "hello from B" --serve
   # Note CID_B, B_peerID, and B_addr
   ```
4. Terminal A: fetch from B
   ```bash
   ./node get --cid CID_B --from-addr B_addr --from-peer B_peerID
   ```

Notes:
- If `--listen` is omitted, defaults are TCP/0 and QUIC-v1/0.
- QUIC may be blocked in some environments; TCP is sufficient.

## Non-goals and prohibitions

- DHTs: This project MUST NOT use any distributed hash table in any capacity. No exceptions. This is intentionally “IPFS minus DHT.” Discovery, routing, and provider lookups MUST be implemented via alternative mechanisms (explicit peers, ACAN, rendezvous/relays, etc.). The term "Kademlia" is explicitly out of scope.

## Daemon vs no-daemon workflows

### Daemon mode (default, background; recommended for two machines)

1) Start the node as a background daemon (Machine A and Machine B):

```bash
./node run --background --log node.log \
  --listen "/ip4/0.0.0.0/tcp/0" --listen "/ip4/0.0.0.0/udp/0/quic-v1"
# Output: "Started node in background. PID: <pid>"
```

2) Get the libp2p PeerID and addrs from the log (on each machine):

```bash
tail -n +1 node.log
# Look for lines prefixed with "PeerID:" and "Addr:"; copy one multiaddr and the peer ID
```

3) Put from Machine A via the daemon (returns immediately):

```bash
./node put --data "hello from A"
# Prints CID and multihash; no need for --serve (daemon is already serving)
```

4) Get from Machine B via the daemon:

```bash
./node get \
  --cid "<CID_FROM_A>" \
  --from-addr "<A_multiaddr>" \
  --from-peer "<A_peerID>"
```

5) Reverse direction (Machine B puts, Machine A gets) using the same pattern.

Daemon details:
- A control file is written at `/tmp/fall25_node/daemon.json` with the HTTP control address. CLI subcommands (`put`, `connect`, `get`) auto-detect and talk to the daemon unless `--no-daemon` is set.
- Use `--control <path>` to override the control file location.

### No-daemon mode (foreground; two terminals on one machine)

1) Terminal A: start a serving node by putting with `--serve` (blocks):

```bash
./node put --data "hello from A" --serve \
  --listen "/ip4/0.0.0.0/tcp/0" --listen "/ip4/0.0.0.0/udp/0/quic-v1" \
  --no-daemon
# Copy the printed PeerID and an Addr; note the CID
```

2) Terminal B: fetch from A (foreground one-shot):

```bash
./node get \
  --cid "<CID_FROM_A>" \
  --from-addr "<A_multiaddr>" \
  --from-peer "<A_peerID>" \
  --no-daemon
```

3) To test the opposite direction in no-daemon mode, run another `put --serve` on Terminal B and `get` on Terminal A.

Notes:
- `--no-daemon` forces the subcommand to run inline (ignores any running daemon), which is useful for local, isolated tests.
- For backgrounded runs, consider `--log node.log` so you can easily copy PeerID/addrs for the counterpart.

### Exact commands (no-daemon, absolute paths)

- Terminal A:
```bash
/home/nicktagliamonte/Desktop/fall25_independentStudy/node put --file /home/nicktagliamonte/Desktop/fall25_independentStudy/docs/dailyPlan.txt --serve --no-daemon --listen "/ip4/0.0.0.0/tcp/0" --listen "/ip4/0.0.0.0/udp/0/quic-v1"
```

- Terminal B:
```bash
/home/nicktagliamonte/Desktop/fall25_independentStudy/node get --cid "<CID_FROM_A>" --from-addr "<ADDR_FROM_A>" --from-peer "<PEERID_FROM_A>" --no-daemon --out /home/nicktagliamonte/Desktop/out.txt
```

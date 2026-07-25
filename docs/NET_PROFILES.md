# Network Profiles Documentation

## Overview

Network profiles simulate various network conditions (latency, packet loss, bandwidth limits, partitions) for testing distributed system behavior.

## Requirements

- **sudo access**: Network shaping requires root privileges
- **iproute2**: `tc` and `ip` commands must be installed
  - Fedora/RHEL: `sudo dnf install iproute`
  - Ubuntu/Debian: `sudo apt-get install iproute2`
  - macOS: Not supported (use Linux or manual netns setup)

## Usage

### Basic Usage

```bash
# Apply WAN profile (80ms delay)
NET_PROFILE=wan make local

# Apply lossy profile (80ms delay, 3% loss)
NET_PROFILE=lossy DELAY_MS=80 LOSS_PCT=3 make local

# Apply partition simulation
NET_PROFILE=partition GROUPS="1,2|3,4,5" make local

# No network shaping
NET_PROFILE=none make local
# or simply omit NET_PROFILE
```

### Available Profiles

1. **`wan`**: Simulates WAN conditions
   - Parameters: `DELAY_MS` (default: 80ms), `RATE_MBIT` (optional bandwidth limit)
   - Example: `NET_PROFILE=wan DELAY_MS=100 RATE_MBIT=10 make local`

2. **`lossy`**: Simulates lossy network
   - Parameters: `DELAY_MS` (default: 80ms), `LOSS_PCT` (default: 3%)
   - Example: `NET_PROFILE=lossy DELAY_MS=50 LOSS_PCT=5 make local`

3. **`partition`**: Simulates network partition
   - Parameters: `GROUPS` (comma-separated node groups, e.g., "1,2|3,4,5")
   - Note: Current implementation applies high delay/loss to all traffic
   - For true partition isolation, see "Manual Network Namespaces" below

4. **`none`**: No network shaping (default)

## Current Implementation

The current implementation applies `tc netem` to the loopback interface (`lo`), which affects **all** traffic on localhost. This is a simplified approach that works for basic testing but has limitations:

- **All nodes share the same network conditions** (not per-node isolation)
- **Affects all localhost traffic**, not just test nodes
- **Automatically cleaned up** when the harness exits

## Manual Network Namespaces (Advanced)

For per-node network isolation, you can manually set up network namespaces:

### Setup Network Namespaces

```bash
# Create namespaces for each node
for i in {1..5}; do
  sudo ip netns add node$i
  sudo ip netns exec node$i ip link set lo up
done

# Create veth pairs to connect namespaces
for i in {1..5}; do
  sudo ip link add veth$i type veth peer name veth$i-peer
  sudo ip link set veth$i-peer netns node$i
  sudo ip netns exec node$i ip addr add 10.0.0.$i/24 dev veth$i-peer
  sudo ip netns exec node$i ip link set veth$i-peer up
done

# Apply netem to veth interfaces
sudo tc qdisc add dev veth1 root netem delay 50ms loss 2%
sudo tc qdisc add dev veth2 root netem delay 100ms loss 5%
# ... etc
```

### Run Nodes in Namespaces

```bash
# Start node in namespace
sudo ip netns exec node1 ./bin/node run --listen /ip4/10.0.0.1/tcp/2893 ...
```

### Cleanup

```bash
# Remove namespaces
for i in {1..5}; do
  sudo ip netns del node$i
done

# Remove veth pairs
for i in {1..5}; do
  sudo ip link del veth$i 2>/dev/null || true
done
```

## Troubleshooting

### "tc/ip not available"
Install iproute2 package for your distribution.

### "sudo not available"
Network profiles require root access. Either:
1. Run with sudo: `sudo NET_PROFILE=wan make local`
2. Use manual netns setup (see above)
3. Skip network profiles: `NET_PROFILE=none make local`

### Profile not applying
- Check if netem is active: `sudo tc qdisc show dev lo`
- Verify sudo access: `sudo -v`
- Check logs for error messages

### Profile persists after test
Profiles are automatically cleaned up on harness exit. If cleanup fails:
```bash
sudo tc qdisc del dev lo root
```

## Go-level partition detection (separate from simulation)

Everything above is shell/Makefile-level network *simulation* (`tc netem`). Separately, `internal/net/partition.go` implements Go-level partition *detection* that runs inside the node process itself, independent of whether a simulated profile is active:

- **`PeerConnectivityMonitor`**: samples the libp2p host's connected-peer count on an interval (default 10s) and fires a `PartitionEvent` (kind `PartitionEventConnectivity`) when the count drops by at least a configured percentage from a floor of at least `minPeers`. A subsequent rise is treated as recovery.
- **`DHTNeighborMonitor`**: same idea, but samples DHT routing-table k-bucket size (via `KBucketLastSeenTracker`) instead of raw connection count, firing `PartitionEventDHTNeighbors`.
- Both report through the same `OnPartitionEvent` callback type, carrying `{PrevCount, NowCount, Kind}`, so upper layers can react to partition detection uniformly regardless of which signal triggered it.

This is a live signal a running node can act on (e.g. to trigger more aggressive dial maintenance or repair); it is not affected by, and does not require, the `tc netem` simulation described above — you can exercise it against real network conditions in a multi-host deployment, or use `NET_PROFILE=partition` to provoke it in a local test. See `docs/HANDSHAKE_PROTOCOL.md` for the related (but distinct) connection-admission layer.

## Future Improvements

- Per-node network namespaces with automatic setup
- Port-based filtering for selective shaping
- Integration with container runtimes (Docker, Podman)
- Network topology visualization


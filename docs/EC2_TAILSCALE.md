EC2/Tailscale Deployment Recipe
================================

This document describes how to deploy nodes across EC2 instances using Tailscale for networking.

Prerequisites
-------------
- EC2 instances (or any remote hosts) with SSH access
- Tailscale installed on all hosts
- Node binary built (`make build`)
- SSH keys for EC2 access

Setup Steps
-----------

### 1. Install Tailscale on All Hosts

On each EC2 instance:

```bash
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up --authkey <YOUR_AUTH_KEY> --hostname sng40-<N>
```

Note the Tailscale IPs (100.x.y.z range) for each host.

### 2. Generate Node Keys (Optional)

You can pre-generate keys or let the scripts create them:

```bash
# On local machine or EC2 instance
./bin/node keygen --out ~/.sng40/ec2/bootstrap.key
./bin/node keygen --out ~/.sng40/ec2/peer1.key
./bin/node keygen --out ~/.sng40/ec2/peer2.key
# ... etc
```

### 3. Deploy Binary to EC2 Instances

Copy the binary to each EC2 instance:

```bash
# Example: copy to EC2 instance
scp -i ~/Downloads/sng40_1.pem bin/node ubuntu@44.210.149.116:~/sng40/
scp -i ~/Downloads/sng40_2.pem bin/node ubuntu@44.192.80.237:~/sng40/
# ... etc
```

Or build directly on each instance:

```bash
# On each EC2 instance
git clone <repo>
cd <repo>
make build
```

### 4. Start Bootstrap Node

On the bootstrap host (e.g., your laptop or first EC2 instance):

```bash
# Get Tailscale IP
TAILSCALE_IP=$(ip addr show tailscale0 | grep -oP 'inet \K[0-9.]+' | head -n1)

# Start bootstrap
bash scripts/harness/ec2_bootstrap.sh \
  --key-path ~/.sng40/ec2/bootstrap.key \
  --listen "/ip4/0.0.0.0/tcp/4001"

# Note the seed output:
# export SNG40_SEEDS="/ip4/100.x.y.z/tcp/4001/p2p/12D3KooW..."
```

### 5. Start Peer Nodes

On each peer EC2 instance:

```bash
# Set seed from bootstrap
export SNG40_SEEDS="/ip4/<BOOTSTRAP_TAILSCALE_IP>/tcp/4001/p2p/<BOOTSTRAP_PEER_ID>"

# Start peer
bash scripts/harness/ec2_peer.sh \
  --key-path ~/.sng40/ec2/peer1.key \
  --seed "$SNG40_SEEDS" \
  --listen "/ip4/0.0.0.0/tcp/4001" \
  --node-id 2
```

Repeat for each peer with different `--node-id` and key files.

### 6. Optional: Apply Network Profiles

To simulate inter-region latency/loss on EC2 instances:

```bash
# On each EC2 instance (requires sudo)
sudo tc qdisc add dev tailscale0 root netem delay 80ms loss 1%
```

Or use the profiles script:

```bash
# On each instance
. scripts/net/profiles.sh
apply_profile <run_id> wan 80 0
```

Manual Steps (Alternative)
---------------------------

If you prefer manual control:

1. **Start bootstrap:**
   ```bash
   ./bin/node run \
     --listen "/ip4/0.0.0.0/tcp/4001" \
     --key ~/.sng40/ec2/bootstrap.key \
     --daemon \
     --control /tmp/fall25_node/bootstrap.json \
     --log /tmp/fall25_node/bootstrap.log
   ```

2. **Get bootstrap seed:**
   ```bash
   CTRL_ADDR=$(jq -r '.addr' /tmp/fall25_node/bootstrap.json)
   ID_JSON=$(curl -s "http://$CTRL_ADDR/id")
   PEER_ID=$(echo "$ID_JSON" | jq -r '.peer')
   TAILSCALE_IP=$(ip addr show tailscale0 | grep -oP 'inet \K[0-9.]+' | head -n1)
   SEED="/ip4/$TAILSCALE_IP/tcp/4001/p2p/$PEER_ID"
   export SNG40_SEEDS="$SEED"
   ```

3. **Start peers:**
   ```bash
   env SNG40_SEEDS="$SEED" ./bin/node run \
     --listen "/ip4/0.0.0.0/tcp/4001" \
     --key ~/.sng40/ec2/peer1.key \
     --daemon \
     --control /tmp/fall25_node/peer1.json \
     --log /tmp/fall25_node/peer1.log
   ```

Troubleshooting
---------------

- **Nodes can't connect:** Check Tailscale status (`tailscale status`), firewall rules, and that seed multiaddr uses Tailscale IP
- **Control file not created:** Check logs (`--log` path), ensure ports are open
- **Wrong PeerID:** Ensure `--key` path is consistent across restarts

Example Topology
---------------

```
Bootstrap (Laptop, Tailscale: 100.1.1.1)
  ├── Peer 1 (EC2 us-east-1, Tailscale: 100.99.173.11)
  ├── Peer 2 (EC2 us-west-2, Tailscale: 100.102.6.95)
  └── Peer 3 (EC2 eu-west-1, Tailscale: 100.126.19.118)
```

All nodes connect via Tailscale overlay network (100.x.y.z addresses).


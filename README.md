# Prototype Node (early)

Minimal P2P content node for quick testing

## Build
```
go build -o node ./cmd/node/main.go
```

## Quick start
- Terminal A (serve):
```
./node put --data "hello" --serve
```
- Terminal B (fetch):
```
./node get --cid "<CID_FROM_A>" --from-addr "/ip4/<IP>/tcp/2893" --from-peer "<PEERID_FROM_A>" --out /tmp/out.txt
```

## Run a node
```
./node run
```
- Prints PeerID, local addrs, and Public Addr (if detectable)
- Default ports: TCP 2893, UDP 2894 (QUIC)

## Connect (manual dial)
```
./node connect --addr "/ip4/<IP>/tcp/2893" --peer "<PEERID>"
```

## Notes
- Ensure 2893/tcp and 2894/udp are open on the remote host (cloud SG/NACL + firewall).
- If Public Addr isn’t printed, the host may not have a public IPv4; use a public IP or port-forward.
- For daemonized run: `./node run --daemon` (logs: /tmp/fall25_node/daemon.log)

## Public vs Private networks (application-layer admission)

This node can run open (public) or require admission (private) using per-node tokens signed by a CA. By default, if no token env vars are set, the node runs open (any libp2p peer can connect; streams are still gated by verack).

### Public mode (default)
- Do nothing; just run `./node run`. No token required.

### Private mode (token-gated)
1) Prepare a CA keypair (issuer machine):
```
openssl genpkey -algorithm ED25519 -out ~/.sng40/ca.key
openssl pkey -in ~/.sng40/ca.key -pubout -outform DER | tail -c 32 | base64 -w0
```
Copy the base64 output; this is `SNG40_CA_PUBS` (one or more, comma-separated).

2) Get the node’s PeerID (run once to print it), then stop the node.

3) Issue a token for that PeerID (issuer machine):
```
printf "%s" "<PEER_ID>" > /tmp/peer.txt
openssl pkeyutl -sign -inkey ~/.sng40/ca.key -rawin -in /tmp/peer.txt -out /tmp/sig.bin
base64 -w0 /tmp/sig.bin
```
Use the base64 signature as `SNG40_TOKEN`.

4) Start the node with env vars:
```
export SNG40_CA_PUBS="<base64_ca_pub>[,<base64_ca_pub_2>]"
export SNG40_TOKEN="<base64_signature_over_peerid>"
./node run
```
- To force private mode even if envs are unset/mis-set, set `SNG40_REQUIRE_TOKEN=true`.

### How it works (PoC)
- Verack carries a token (base64 ed25519 signature over the node’s PeerID). Peers verify tokens against any trusted CA in `SNG40_CA_PUBS`. If valid, streams are allowed; otherwise the connection is dropped at handshake.
- In public mode, no token is required; the node behaves like IPFS’s public net at the application layer.

## State head/height (monotone G-set)
- The node maintains a tiny append-only event log (DAG-CBOR) for a grow-only set of facts.
- Today’s fact: `peer_added` when a peer successfully completes verack.
- The current head CID and height are advertised in the handshake to signal progress; no equality checks are required.
- Head advances via `peer_added` appends; repeated appends for the same peer are harmless.

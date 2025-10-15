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

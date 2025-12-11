# Local multi-node quickstart (one host)

Prereqs: bash, curl, jq, Go toolchain.

1) Build and start N nodes (writes node registry)

```bash
cd /home/nicktagliamonte/Desktop/fall25_independentStudy && make build && RUN=$(date +%s) && N=5 TOPOLOGY=star MIN_OUTBOUND=4 RUN_ID=$RUN DURATION_S=60 make local
```

2) Connect all peers to the bootstrap and print neighbor counts

```bash
/usr/bin/bash -lc 'RUN='"$RUN"'; N=$(jq "length" artifacts/runs/$RUN/nodes.json); ADDR=$(jq -r ".[0].control_addr" artifacts/runs/$RUN/nodes.json); IDJSON=$(curl -s "http://$ADDR/id"); BOOT_PEER=$(printf "%s" "$IDJSON" | jq -r .peer); BOOT_TCP=$(printf "%s" "$IDJSON" | jq -r ".addrs[] | select(test(\"/tcp/\"))" | head -n1); for i in $(seq 2 $N); do NA=$(jq -r ".[$((i-1))].control_addr" artifacts/runs/$RUN/nodes.json); curl -s -X POST -H "Content-Type: application/json" -d "{\"addr\":\"$BOOT_TCP\",\"peer\":\"$BOOT_PEER\",\"timeout\":\"10s\"}" "http://$NA/connect" >/dev/null || true; done; for i in $(seq 1 $N); do NA=$(jq -r ".[$((i-1))].control_addr" artifacts/runs/$RUN/nodes.json); printf "node %d neighbors: " "$i"; curl -s "http://$NA/neighbors" | jq "length"; done'
```

Expected output (star topology example):

```
node 1 neighbors: 4
node 2 neighbors: 1
node 3 neighbors: 1
node 4 neighbors: 1
node 5 neighbors: 1
```

Notes
- The above listed star topology example should not be present in the final version of the program -- we have introduced peer discovery on handshake ack such that leaf nodes will be introduced and connect to one another on connection to the bootstrap
- The node registry is at `artifacts/runs/$RUN/nodes.json`; per-node logs and control files are in the same directory.
- To export a seed for new processes on the same host:

```bash
/usr/bin/bash -lc 'RUN='"$RUN"'; ADDR=$(jq -r ".[0].control_addr" artifacts/runs/$RUN/nodes.json); IDJSON=$(curl -s "http://$ADDR/id"); PEER=$(printf "%s" "$IDJSON" | jq -r .peer); TCP=$(printf "%s" "$IDJSON" | jq -r ".addrs[] | select(test(\"/tcp/\"))" | head -n1); echo "export SNG40_SEEDS=\"$TCP/p2p/$PEER\""'
```




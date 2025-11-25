Cross-machine test (Chameleon host → Local machine)

Remote (Chameleon) - Terminal A
1) Open ports (firewalld):
sudo firewall-cmd --add-port=2893/tcp --add-port=2894/udp --permanent && sudo firewall-cmd --reload

2) Start a serving put (prints CID, PeerID, Public Addr):
./node put --data "hello from chameleon" --serve

Local - Terminal B
3) Fetch to a file:
./node get --cid "<CID_FROM_A>" --from-addr "/ip4/<PUBLIC_IP>/tcp/2893" --from-peer "<PEERID_FROM_A>" --out /tmp/test-out.txt

4) Verify:
cat /tmp/test-out.txt

Optional checks
- Port open (from local): `nc -vz <PUBLIC_IP> 2893`

================================================================================================================================================================================================

Plan for next:
1. network topology
| Node   | Host        | Role        | Notes                                 | Connection Method                                      | Tailscale IP    | Tailscale Start Command               |
| ------ | ----------- | ----------- | ------------------------------------- | ------------------------------------------------------ | --------------- | ------------------------------------- | 
| Peer 0 | Laptop      | Bootstrap   | Publicly reachable or via VPN overlay |                                                        |                 |                                       |
| Peer 1 | Desktop     | Normal peer | Boots from Peer 0                     |                                                        |                 |                                       |
| Peer 2 | Cloud VM #1 | Normal peer | Boots from Peer 0                     | ssh -i ~/Downloads/sng40_1.pem ubuntu@44.210.149.116   | 100.99.173.11   | sudo tailscale up --hostname sng40-1  |
| Peer 3 | Cloud VM #2 | Normal peer | Boots from Peer 0                     | ssh -i ~/Downloads/sng40_2.pem ubuntu@44.192.80.237    | 100.102.6.95    | sudo tailscale up --hostname sng40-2  |
| Peer 4 | Cloud VM #3 | Normal peer | Boots from Peer 0                     | ssh -i ~/Downloads/sng40_3.pem ubuntu@3.229.124.147    | 100.126.19.118  | sudo tailscale up --hostname sng40-3  |

2. networking layer
use tailscale to create private overlay network
    1. install on each machine with
    ```
    curl -fsSL https://tailscale.com/install.sh | sh
    sudo tailscale up --authkey <temp-key> --hostname peerX
    ```
    2. each peer gets something in the 100.x.y.z range (probably. doesn't matter WHAT it is, just that we have it)
    3. set the bootstrap addr to 100.x.y.z:4001 (or whichever port)

3. deployment and launch script
    1. get a binary which can be sent out to each of the nodes (this requires some planning)
    2. run the bootstrap (laptop) node and note multiaddr info
    3. start other peers (this will also take some planning -- how to run a single peer properly, and how to feed it a bootstrap addr?)
    
4. measurement strategy
    1. have each node log timestamps whenever a new peer is discovered
    2. collect logs and compute delta from first to last discovery event

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


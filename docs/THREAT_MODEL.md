# Mutable-storage threat model

Tarsus assumes SHA-256 collision resistance, Ed25519 signature security,
X25519 key-exchange security, HKDF-SHA256, and XChaCha20-Poly1305. Namespace
owner private keys and authorized reader endpoints are trusted. Peers, storage
providers, networks, and secondary indexes may crash, omit messages, replay
old records, equivocate, return altered bytes, or partition.

The protocol detects malformed or altered signed records, manifests, chunks,
envelopes, provider claims, capabilities, and leases. Expected-generation CAS
prevents two writes from the same predecessor from both committing at one
fenced exact-name authority. Hash verification detects corrupt provider data;
replication improves availability but seven copies alone provide no Byzantine
agreement. Search indexes are hints and can omit results, so responses expose
incomplete fanout and exact resolution remains authoritative.

The protocol does not guarantee write availability or linearizability across
arbitrary partitions, global FIFO across associative tuple matches, geographic
or administrative diversity from RTT, revocation of plaintext or keys already
obtained, deletion of third-party copies, protection of public searchable-name
metadata, or application-level byte-range concurrency. A malicious current
namespace owner can authorize any update. Denial of service, traffic analysis,
endpoint compromise, and rollback by a client that refuses to query a current
authority are outside the present prototype.

Strict publication is fail-closed: if any required manifest or chunk lacks the
configured verified-provider count, the new mutable head is not exposed.
Repair reads the committed per-object policy and restores missing availability;
it does not turn replica voting into consensus.

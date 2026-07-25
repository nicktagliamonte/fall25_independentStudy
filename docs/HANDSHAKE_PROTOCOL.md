# Handshake Protocol

The connection-admission path exchanged immediately after two peers establish a libp2p connection. Implemented in `internal/net/handshake.go`, with supporting anti-replay (`replay.go`), attack mitigation (`protection.go`), ASN resolution (`asnresolver.go`), permission wiring (`handshake_permission.go`), and post-handshake connection gating (`handshake_gate.go`).

---

## Protocol

**Protocol ID:** `/sng40/handshake/1.0.0`

Both sides exchange a `VersionMsg`, then a `VerAckMsg`, over a single libp2p stream, JSON-encoded:

1. **Initiator → Responder:** `VersionMsg` (nonce, services, agent, optional credential, optional peer-sample request).
2. **Responder → Initiator:** validates the initiator's `VersionMsg` (anti-replay, version/services policy, credential scheme), then sends its own `VersionMsg` — including a **challenge-response signature over the initiator's nonce**, and a peer sample if `WantPeerlist` was set and a `PeerProvider` is configured.
3. **Initiator:** validates the responder's `VersionMsg` the same way, plus verifies the challenge-response signature against the responder's known public key.
4. **Both sides** exchange an empty `VerAckMsg` to confirm acceptance.

On success, both `PerformHandshake`/`PerformHandshakeWithState` (initiator) and `responder` tag the peer in the host's connection manager with `handshakeOkTag`, a hint consumed by downstream gating/policy (see `HandshakeGate` below).

### VersionMsg fields

| Field | Type | Purpose |
|---|---|---|
| `nonce` | uint64 | Per-message random value (from `UnixNano`); recorded by `NonceCache` for anti-replay, and is the value the *other* side signs for challenge-response. |
| `services` | uint64 | Bitmask of services offered; checked against `HandshakePolicy.ServicesAllow`. |
| `agent` | string | e.g. `"sng40/0.1.0"`; checked against `HandshakePolicy.MinAgentVersion`. |
| `start_height` | int64 | Reported chain/log height. |
| `timestamp` | int64 | Unix seconds; checked by `TimestampChecker` if configured. |
| `state_head` / `state_height` | string / int64 | Optional state-root summary. |
| `auth_scheme` / `auth_proof` | string | Optional admission credential (see below). |
| `want_peerlist` | bool | Requests a peer sample from the responder. |
| `listen_addrs` | []string | Sender's own advertised listen multiaddrs. |
| `peers` | []string | Peer sample, as multiaddrs with a trailing `/p2p/<peerID>` component. |
| `challenge_response` | string | `base64(sign(remote's nonce))`, proving possession of the private key behind the sender's peer ID. |

### Admission credential ("token-ed25519-v1")

When `HandshakePolicy.RequireCredential` is set, both sides must present `auth_scheme` matching `policy.AuthScheme` and a non-empty `auth_proof`. The credential format actually implemented (`verifyToken`) is:

```
auth_proof = base64(ed25519.Sign(CA_private_key, []byte(peerID)))
```

verified against any of `policy.CAPubKeys`. This is simpler than a token format with an expiry field — there is no expiry; a token remains valid for as long as the CA key is trusted.

### Challenge-response

Independent of the admission credential, each side signs the *other* side's nonce with its own libp2p host private key (`signChallenge`, `priv.Sign(nonceBytes(nonce))`) and the receiver verifies it against the sender's known public key in the peerstore (`verifyChallengeResponse`). This proves the responder (and, if it echoes a response, the initiator) actually controls the private key behind its advertised peer ID — independent of whether an admission token is also required.

---

## Anti-replay (`replay.go`)

Enabled via `EnableAntiReplay(ctx, policy)`, which populates and starts:

- **`NonceCache`**: rejects a `VersionMsg` whose `nonce` has already been seen from the same peer.
- **`MessageHashCache`**: rejects a `VersionMsg` whose full JSON-encoded payload (SHA-256 hashed) has already been seen from the same peer — catches replays that vary only the nonce, or don't rely on nonce reuse detection.
- **`TimestampChecker`**: rejects a `VersionMsg` whose `timestamp` falls outside an acceptable window (`RejectExpiredUnix`).

All three are checked in `applyAntiReplay`, in order: timestamp, nonce, message hash. Any left `nil` is skipped (opt-in, like `KeyLockManager` — see `docs/LOCKING_API.md`).

## Attack mitigation (`protection.go`)

Enabled via `EnableAttackMitigation(ctx, policy)`, which populates an `AttackMitigation` bundle and starts a periodic misbehavior-score decay loop (`DefaultMisbehaviorDecayPeriod` = 1 minute):

- **`BanList`**: peers that fail the responder's checks accumulate misbehavior score; once the score crosses a threshold, the peer is banned and subsequent handshake attempts from it are rejected outright (checked first, before rate limiting).
- **`PeerRateLimiter`**: rate-limits handshake attempts per peer.
- **`EclipseLimiter`**: uses ASN resolution (below) to bound how many connections are accepted from the same subnet/ASN, mitigating eclipse attacks. Registered for a peer only after a successful responder-side handshake (`am.Eclipse.Register`).
- **`PeerMisbehaviorScorer`**: accumulates a score per peer on handshake failure (`AddMisbehavior(pid, 10)` in the responder's deferred error handler); crossing `ShouldDisconnect`'s threshold triggers a ban. Scores decay over time via the periodic loop.
- **`AddressBucketStore`**: buckets peer addresses (e.g. by subnet) for diversity-aware peer selection.
- **`PeerResourceCap`**: bounds per-peer resource consumption (e.g. concurrent streams).

## ASN resolution (`asnresolver.go`)

`CymruASNResolver` (`NewCymruASNResolver()`) resolves an IP address to its Autonomous System Number via Team Cymru's public DNS-based whois service, with an in-memory cache (`asnCacheEntry`). This is the mechanism `EclipseLimiter` uses to group peers by network origin rather than just by individual IP, so an attacker controlling many IPs within one ASN doesn't get proportionally more accepted connections.

## Handshake gate (`handshake_gate.go`)

`HandshakeGate` implements a libp2p `network.Notifiee` (`handshakeNotifiee`) that spawns the responder-side handshake automatically whenever a new inbound connection is established (`Connected` callback) — i.e., the handshake isn't only triggered by an explicit call to `PerformHandshake`; connections are gated by default once a `HandshakeGate` is installed. A peer that completes the handshake is tagged `handshakeOkTag` in the connection manager, which downstream code can check to distinguish handshake-verified peers from raw libp2p connections.

**Known gap:** each inbound connection spawns an untracked goroutine to run the handshake; there is no cap or `WaitGroup` bounding concurrent in-flight handshakes (only the initial per-peer rate limiter check gates new attempts, not total concurrency).

## Permission wiring (`handshake_permission.go`)

`NewHandshakePermissionChecker(policy)` adapts a `HandshakePolicy` into a `tuplespace.PermissionChecker`, so the same credential/anti-replay/attack-mitigation policy that gates libp2p connections can also gate P2P tuple space operations (`internal/tuplespace/permission.go`) — e.g. requiring the same admission token for administrative tuple space writes as for connecting at all.

---

## See also

- `docs/NET_PROFILES.md` — shell-level network *simulation* (latency/loss/partition via `tc netem`); this doc covers Go-level partition *detection*, a related but separate concept.
- `docs/LOCKING_API.md` — the "opt-in via nil checks" pattern used throughout this protocol (anti-replay, attack mitigation) mirrors how `KeyLockManager` is opt-in for Put/Delete.

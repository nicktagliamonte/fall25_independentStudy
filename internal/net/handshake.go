// Purpose: Simple version/verack handshake over a libp2p stream.

package net

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

// HandshakeProtocolID is the libp2p protocol ID used for the version/verack handshake stream.
const HandshakeProtocolID = "/sng40/handshake/1.0.0"

// VersionMsg is the wire message exchanged first by both sides of the handshake
// (JSON-encoded over the stream). It advertises the sender's identity/capabilities,
// optionally its local chain-state summary, an admission token (when credentials are
// required by policy), and, if the sender set WantPeerlist, may request/return a small
// sample of known peers.
type VersionMsg struct {
	// Nonce is a per-message random value (derived from the current time in
	// nanoseconds); it is generated but not currently validated by the peer.
	Nonce uint64 `json:"nonce"`
	// Services is a bitmask advertising the capabilities/services offered by the sender.
	Services uint64 `json:"services"`
	// Agent identifies the software and version, e.g. "sng40/0.1.0". Compared against
	// HandshakePolicy.MinAgentVersion by validateVersion when version checking is enabled.
	Agent string `json:"agent"`
	// StartHeight is advertised as the sender's chain start height (informational).
	StartHeight int64 `json:"start_height"`
	// Timestamp is the sender's local Unix time at message creation (informational,
	// not validated).
	Timestamp int64 `json:"timestamp"`
	// Optional state summary
	// StateHeadCID is the string form of the sender's current head CID, if any.
	StateHeadCID string `json:"state_head,omitempty"`
	// StateHeight is the chain height corresponding to StateHeadCID.
	StateHeight int64 `json:"state_height,omitempty"`
	// Admission extension
	// AuthScheme names the credential scheme used for AuthProof (e.g. "token-ed25519-v1").
	// Only populated/checked when HandshakePolicy.RequireCredential is true.
	AuthScheme string `json:"auth_scheme,omitempty"`
	AuthProof  string `json:"auth_proof,omitempty"` // carries signed token
	// Optional discovery extensions
	// WantPeerlist, when true, asks the responder to populate Peers with a sample of
	// peers it knows about (see PeerProvider).
	WantPeerlist bool `json:"want_peerlist,omitempty"`
	// ListenAddrs lists the sender's own listen multiaddrs (without the /p2p suffix),
	// advertised so the remote side can learn how to dial the sender directly.
	ListenAddrs []string `json:"listen_addrs,omitempty"`
	// Peers is a list of multiaddrs, each including a trailing /p2p/<peerID> component,
	// describing peers the responder is willing to share. Populated by the responder
	// only when the initiator set WantPeerlist and a PeerProvider was supplied.
	Peers []string `json:"peers,omitempty"` // multiaddrs with /p2p/<peerID>
}

// VerAckMsg is the empty acknowledgement message exchanged as the final step of the
// handshake by both sides. It currently carries no payload; its receipt alone signals
// "handshake complete" for that direction.
type VerAckMsg struct{}

// HandshakeLocal holds the values this node advertises in its own VersionMsg when
// acting as either initiator or responder.
type HandshakeLocal struct {
	// Agent is this node's agent/version string, e.g. "sng40/0.1.0".
	Agent string
	// Services is the bitmask of services/capabilities this node offers.
	Services uint64
	// StartHeight is advertised as this node's chain start height.
	StartHeight int64
	// WantPeerlist, when true (initiator side), requests that the responder include a
	// peer sample in its VersionMsg.
	WantPeerlist bool
	// ListenAddrs are this node's own listen multiaddrs to advertise to the remote peer.
	ListenAddrs []string
	// Optional state summary to advertise
	// StateHeadCID is this node's current head CID (string form), if any.
	StateHeadCID string
	// StateHeight is the height corresponding to StateHeadCID.
	StateHeight int64
}

// HandshakePolicy configures how an initiator or responder validates the remote peer's
// VersionMsg and how long the handshake is allowed to take.
type HandshakePolicy struct {
	MinAgentVersion string // "" to disable version check
	ServicesAllow   uint64 // 0 means allow any, else remoteServices must be subset of this mask
	// Timeout bounds the entire handshake (all four message exchanges). If zero or
	// negative, policyTimeout falls back to a 5-second default.
	Timeout time.Duration
	// Admission controls (token-based)
	// RequireCredential, when true, requires the remote's VersionMsg to carry a valid
	// AuthScheme/AuthProof; both initiator and responder verify this.
	RequireCredential bool
	AuthScheme        string   // "token-ed25519-v1"
	CAPubKeys         [][]byte // one or more ed25519 public keys
	// Token is this node's own signed credential, sent as AuthProof in its VersionMsg
	// when RequireCredential is true. It is not itself validated against CAPubKeys by
	// this node (that only happens for the token received from the remote peer).
	Token string // signed token carried in AuthProof
}

// PeerProvider is used by the responder to include a small peer sample. It is called
// with the maximum number of peers to return (max) and should return up to that many
// peer.AddrInfo values; the responder serializes each address as a multiaddr with an
// appended /p2p/<peerID> component. A nil PeerProvider (or a nil return) means no
// peers are advertised.
type PeerProvider func(max int) []peer.AddrInfo

// RegisterHandshake installs a stream handler on h for HandshakeProtocolID that runs
// the responder side of the handshake for each inbound stream, using local as this
// node's advertised VersionMsg fields and policy to validate the remote's VersionMsg.
// No peer sample is offered to the initiator even if it requests one (use
// RegisterHandshakeWithPeers for that). Handshake errors are logged nowhere and simply
// close the stream; callers cannot observe failures from this registration.
func RegisterHandshake(h host.Host, local HandshakeLocal, policy HandshakePolicy) {
	h.SetStreamHandler(HandshakeProtocolID, func(s network.Stream) {
		defer s.Close()
		_ = responder(s, local, policy, nil)
	})
}

// RegisterHandshakeWithPeers is like RegisterHandshake but additionally passes
// provider to the responder so it can include a peer sample in its VersionMsg when the
// initiator sets WantPeerlist.
func RegisterHandshakeWithPeers(h host.Host, local HandshakeLocal, policy HandshakePolicy, provider PeerProvider) {
	h.SetStreamHandler(HandshakeProtocolID, func(s network.Stream) {
		defer s.Close()
		_ = responder(s, local, policy, provider)
	})
}

// HandshakeResult reports the outcome of an initiator-side handshake: any peers the
// responder shared (Learned) and the responder's advertised chain-state summary
// (RemoteStateHead/RemoteStateHeight, empty/zero if the responder advertised none).
type HandshakeResult struct {
	Learned           []peer.AddrInfo
	RemoteStateHead   string
	RemoteStateHeight int64
}

// PerformHandshakeWithState opens a new stream to peer p using HandshakeProtocolID and
// runs the initiator side of the handshake, validating the responder's VersionMsg
// against policy and advertising local's fields as this node's own VersionMsg.
//
// On success it tags p in the host's ConnManager with handshakeOkTag and returns a
// HandshakeResult containing any peers the responder shared and its state summary.
// On failure (stream open failure, I/O error, or policy validation failure such as a
// version mismatch or bad/missing auth token) it returns a nil *HandshakeResult and a
// non-nil error; no ConnManager tag is applied in that case.
func PerformHandshakeWithState(ctx context.Context, h host.Host, p peer.ID, policy HandshakePolicy, local HandshakeLocal) (*HandshakeResult, error) {
	s, err := h.NewStream(ctx, p, HandshakeProtocolID)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	learned, remote, err := initiatorWithState(s, local, policy)
	if err != nil {
		return nil, err
	}
	h.ConnManager().TagPeer(p, handshakeOkTag, 1)
	return &HandshakeResult{Learned: learned, RemoteStateHead: remote.StateHeadCID, RemoteStateHeight: remote.StateHeight}, nil
}

// PerformHandshake is like PerformHandshakeWithState but discards the responder's
// state summary, returning only the list of learned peer.AddrInfo. On success it tags
// p in the host's ConnManager with handshakeOkTag. On failure it returns a nil slice
// and a non-nil error, and no tag is applied.
func PerformHandshake(ctx context.Context, h host.Host, p peer.ID, policy HandshakePolicy, local HandshakeLocal) ([]peer.AddrInfo, error) {
	s, err := h.NewStream(ctx, p, HandshakeProtocolID)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	learned, _, err := initiatorWithState(s, local, policy)
	if err != nil {
		return nil, err
	}
	// Mark the peer as verified for downstream gating/policy.
	h.ConnManager().TagPeer(p, handshakeOkTag, 1)
	return learned, nil
}

// initiator runs the initiator side of the handshake over an already-open stream s and
// discards the responder's state summary, returning only learned peers. It is
// preserved for callers that don't need remote state; it has no current callers within
// this package but is kept as a thin wrapper around initiatorWithState.
func initiator(s network.Stream, local HandshakeLocal, policy HandshakePolicy) ([]peer.AddrInfo, error) {
	learned, _, err := initiatorWithState(s, local, policy)
	return learned, err
}

// initiatorWithState drives the initiator side of the version/verack handshake over an
// already-open stream s:
//  1. sends a VersionMsg built from local (including an auth token if
//     policy.RequireCredential is set);
//  2. receives and validates the responder's VersionMsg via validateVersion, and, if
//     credentials are required, verifies the responder's AuthProof against
//     policy.CAPubKeys;
//  3. sends an empty VerAckMsg;
//  4. waits to receive the responder's VerAckMsg.
//
// A per-handshake deadline is set on the stream via policyTimeout(policy) before any
// I/O. Returns the peer.AddrInfo list parsed from the responder's advertised Peers
// (parsePeerlist), the responder's raw VersionMsg (for callers that want the state
// summary), and a non-nil error if any step fails: stream I/O/decode errors, a failed
// validateVersion check, or (when RequireCredential is set) a missing/mismatched auth
// scheme or a token that fails verifyTokenAny. On error the returned slice/VersionMsg
// are zero values and must not be used.
func initiatorWithState(s network.Stream, local HandshakeLocal, policy HandshakePolicy) ([]peer.AddrInfo, VersionMsg, error) {
	deadline := time.Now().Add(policyTimeout(policy))
	_ = s.SetDeadline(deadline)
	enc := json.NewEncoder(s)
	dec := json.NewDecoder(s)

	// 1) send version
	my := VersionMsg{
		Nonce:        uint64(time.Now().UnixNano()),
		Services:     local.Services,
		Agent:        local.Agent,
		StartHeight:  local.StartHeight,
		Timestamp:    time.Now().Unix(),
		WantPeerlist: local.WantPeerlist,
		ListenAddrs:  local.ListenAddrs,
		StateHeadCID: local.StateHeadCID,
		StateHeight:  local.StateHeight,
	}
	// Admission (token): initiator includes its token in Version.
	if policy.RequireCredential {
		my.AuthScheme = policy.AuthScheme
		my.AuthProof = policy.Token
	}
	if err := enc.Encode(&my); err != nil {
		return nil, VersionMsg{}, err
	}

	// 2) recv remote version and validate
	var remote VersionMsg
	if err := dec.Decode(&remote); err != nil {
		return nil, VersionMsg{}, err
	}
	if err := validateVersion(remote, policy); err != nil {
		return nil, VersionMsg{}, err
	}
	// If credentials required, verify responder's token against CA pubkey.
	if policy.RequireCredential {
		if remote.AuthScheme != policy.AuthScheme || remote.AuthProof == "" {
			return nil, VersionMsg{}, errors.New("missing or wrong auth proof/scheme")
		}
		if ok := verifyTokenAny(policy.CAPubKeys, s.Conn().RemotePeer(), remote.AuthProof); !ok {
			return nil, VersionMsg{}, errors.New("bad auth token from responder")
		}
	}

	learned := parsePeerlist(remote.Peers)

	// 3) send verack (no payload needed for token model)
	if err := enc.Encode(&VerAckMsg{}); err != nil {
		return nil, VersionMsg{}, err
	}

	// 4) require verack from remote (no payload expected)
	var ack VerAckMsg
	if err := dec.Decode(&ack); err != nil {
		return nil, VersionMsg{}, err
	}
	// Responder's final ack carries no check; our token was in our initial Version.

	return learned, remote, nil
}

// responder drives the responder side of the version/verack handshake over an
// already-open stream s:
//  1. receives and validates the initiator's VersionMsg via validateVersion (and, if
//     credentials are required, checks that the auth scheme matches, though the
//     initiator's token itself is not cryptographically verified here — see note
//     below);
//  2. sends its own VersionMsg built from local, including its own auth token if
//     required, its ListenAddrs, and — if the initiator set WantPeerlist and provider
//     is non-nil — up to 16 peer samples from provider, each address serialized as a
//     multiaddr with an appended /p2p/<peerID> component (addresses that fail to
//     encode the /p2p component are silently skipped);
//  3. receives the initiator's VerAckMsg;
//  4. sends its own empty VerAckMsg.
//
// A per-handshake deadline is set on the stream via policyTimeout(policy) before any
// I/O. Returns a non-nil error if any step fails: stream I/O/decode errors, a failed
// validateVersion check, or an auth-scheme mismatch when RequireCredential is set.
// Note: unlike initiatorWithState, the responder does not call verifyTokenAny against
// the initiator's AuthProof — it only checks that AuthScheme matches; validateVersion
// only checks that AuthScheme/AuthProof are non-empty.
func responder(s network.Stream, local HandshakeLocal, policy HandshakePolicy, provider PeerProvider) error {
	deadline := time.Now().Add(policyTimeout(policy))
	_ = s.SetDeadline(deadline)
	enc := json.NewEncoder(s)
	dec := json.NewDecoder(s)

	// 1) recv version, validate
	var remote VersionMsg
	if err := dec.Decode(&remote); err != nil {
		return err
	}
	if err := validateVersion(remote, policy); err != nil {
		return err
	}
	// If credential required: ensure scheme present; verify token now.
	if policy.RequireCredential && remote.AuthScheme != policy.AuthScheme {
		return errors.New("unsupported auth scheme")
	}

	// 2) send version
	my := VersionMsg{
		Nonce:        uint64(time.Now().UnixNano()),
		Services:     local.Services,
		Agent:        local.Agent,
		StartHeight:  local.StartHeight,
		Timestamp:    time.Now().Unix(),
		StateHeadCID: local.StateHeadCID,
		StateHeight:  local.StateHeight,
	}
	if policy.RequireCredential {
		my.AuthScheme = policy.AuthScheme
		my.AuthProof = policy.Token
	}
	// include listen addrs and peers if requested
	my.ListenAddrs = append(my.ListenAddrs, local.ListenAddrs...)
	if remote.WantPeerlist && provider != nil {
		const maxPeers = 16
		infos := provider(maxPeers)
		for _, info := range infos {
			// serialize as multiaddr with /p2p
			for _, a := range info.Addrs {
				// append peer id component
				pidComp, err := ma.NewComponent("p2p", info.ID.String())
				if err != nil {
					continue
				}
				full := a.Encapsulate(pidComp)
				my.Peers = append(my.Peers, full.String())
			}
		}
	}
	if err := enc.Encode(&my); err != nil {
		return err
	}

	// 3) recv verack (no payload expected)
	var ack VerAckMsg
	if err := dec.Decode(&ack); err != nil {
		return err
	}
	// responder already verified initiator's token from Version

	// 4) send verack (empty)
	if err := enc.Encode(&VerAckMsg{}); err != nil {
		return err
	}
	return nil
}

// validateVersion checks a remote VersionMsg v against policy:
//   - if policy.ServicesAllow is non-zero, v.Services must be a subset of that mask;
//   - if policy.MinAgentVersion is non-empty, v.Agent's semver tail must be >= it (see
//     agentOK);
//   - if policy.RequireCredential is set, v.AuthScheme must equal policy.AuthScheme and
//     v.AuthProof must be non-empty (the proof's signature is NOT verified here — that
//     happens separately, and only on the initiator side, via verifyTokenAny).
//
// Returns nil if all applicable checks pass, otherwise a descriptive error identifying
// which check failed.
func validateVersion(v VersionMsg, policy HandshakePolicy) error {
	if policy.ServicesAllow != 0 {
		if v.Services&^policy.ServicesAllow != 0 {
			return errors.New("services not allowed")
		}
	}
	if policy.MinAgentVersion != "" {
		if !agentOK(v.Agent, policy.MinAgentVersion) {
			return fmt.Errorf("agent too old: %s < %s", v.Agent, policy.MinAgentVersion)
		}
	}
	if policy.RequireCredential {
		if v.AuthScheme == "" || v.AuthScheme != policy.AuthScheme {
			return errors.New("auth scheme missing or unsupported")
		}
		if v.AuthProof == "" {
			return errors.New("auth token missing")
		}
	}
	return nil
}

// policyTimeout returns p.Timeout if it is positive, otherwise a default of 5 seconds.
// Used to bound the deadline set on handshake streams (both initiator and responder).
func policyTimeout(p HandshakePolicy) time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return 5 * time.Second
}

// agentOK reports whether agent's version (the part after the last '/', e.g.
// "sng40/0.1.0" -> "0.1.0") is semantically >= min's version tail, per semverGTE.
// agent and min are expected in the form "<name>/<major>.<minor>.<patch>".
func agentOK(agent string, min string) bool {
	have := tailSemver(agent)
	want := tailSemver(min)
	return semverGTE(have, want)
}

// tailSemver returns the substring of s after the last '/', or s unchanged if there is
// no '/' (or the '/' is the last character). Used to strip an agent-name prefix (e.g.
// "sng40/") from a version string like "sng40/0.1.0".
func tailSemver(s string) string {
	i := strings.LastIndexByte(s, '/')
	if i >= 0 && i+1 < len(s) {
		return s[i+1:]
	}
	return s
}

// semverGTE reports whether version string a is greater than or equal to version
// string b, comparing up to three dot-separated numeric components (major, minor,
// patch) via parse3. Missing or non-numeric components are treated as 0.
func semverGTE(a, b string) bool {
	ap := parse3(a)
	bp := parse3(b)
	if ap[0] != bp[0] {
		return ap[0] > bp[0]
	}
	if ap[1] != bp[1] {
		return ap[1] > bp[1]
	}
	return ap[2] >= bp[2]
}

// parse3 splits s on '.' into up to three integer components (e.g. "1.2.3" ->
// [1,2,3]). Missing trailing components remain 0; components beyond the third are
// ignored; a non-numeric component parses as 0 (strconv.Atoi error is discarded).
func parse3(s string) [3]int {
	var out [3]int
	parts := strings.SplitN(s, ".", 3)
	for i := 0; i < len(parts) && i < 3; i++ {
		n, _ := strconv.Atoi(parts[i])
		out[i] = n
	}
	return out
}

// parsePeerlist converts a list of multiaddr strings, each expected to include a
// trailing /p2p/<peerID> component, into peer.AddrInfo values. Entries that fail to
// parse as a multiaddr, or that don't yield a valid AddrInfo (e.g. missing the /p2p
// component), are silently skipped rather than causing an error. Returns nil if in is
// empty or none of the entries parse.
func parsePeerlist(in []string) []peer.AddrInfo {
	var out []peer.AddrInfo
	for _, s := range in {
		m, err := ma.NewMultiaddr(s)
		if err != nil {
			continue
		}
		if info, err := peer.AddrInfoFromP2pAddr(m); err == nil {
			out = append(out, *info)
		}
	}
	return out
}

// verifyToken checks proof (the AuthProof field of a remote VersionMsg) against a
// single CA public key caPub for peer pid.
//
// NOTE on the comment this replaces / actual vs. designed behavior: the original
// design notes above this function (retained in git history) described a richer token
// format carrying an expiry and a CBOR/JSON envelope ({pid, exp, sig}), verified via an
// HMAC-based "computeHMACProof" helper. None of that is implemented here — there is no
// computeHMACProof function in this package, no expiry field, and no envelope. The
// actual, simpler scheme implemented is: proof must be the base64-standard encoding of
// an ed25519 signature over the raw bytes of the peer ID (msg = []byte(pid)), i.e.
// proof = base64( ed25519.Sign(caPriv, []byte(pid)) ). Tokens do not expire and are not
// bound to anything but the peer ID, so a captured/leaked proof for a given peer ID
// remains valid indefinitely and is not scoped to a particular handshake session,
// nonce, or connection.
//
// caPub is interpreted directly as an ed25519.PublicKey (must be the correct length).
// Returns false if proof is not valid base64, or if ed25519 signature verification
// fails; returns true only if the signature over []byte(pid) verifies against caPub.
func verifyToken(caPub []byte, pid peer.ID, proof string) bool {
	pub := ed25519.PublicKey(caPub)
	sig, err := base64.StdEncoding.DecodeString(proof)
	if err != nil {
		return false
	}
	msg := []byte(pid)
	return ed25519.Verify(pub, msg, sig)
}

// verifyTokenAny reports whether proof verifies (via verifyToken) against pid for at
// least one of the candidate CA public keys in caPubs, allowing multiple trusted CAs.
// Returns false if caPubs is empty or none of the keys verify the proof.
func verifyTokenAny(caPubs [][]byte, pid peer.ID, proof string) bool {
	for _, k := range caPubs {
		if verifyToken(k, pid, proof) {
			return true
		}
	}
	return false
}

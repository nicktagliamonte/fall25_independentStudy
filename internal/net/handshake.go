// Purpose: Simple version/verack handshake over a libp2p stream.

package net

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
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

// HandshakeProtocolID is the libp2p protocol ID for the version/verack handshake
// exchanged immediately after a connection is established.
const HandshakeProtocolID = "/sng40/handshake/1.0.0"

// VersionMsg is the first message exchanged by both sides of the handshake. It
// advertises the sender's identity/capabilities and optionally carries admission
// credentials, a state summary, a peer-discovery request/response, and a
// challenge-response signature.
type VersionMsg struct {
	// Nonce is a per-message random value (derived from UnixNano) used for
	// anti-replay (NonceCache) and as the challenge value signed by the responder.
	Nonce uint64 `json:"nonce"`
	// Services is a bitmask of services the sender offers.
	Services uint64 `json:"services"`
	// Agent identifies the sender's software/version, e.g. "sng40/0.1.0".
	Agent string `json:"agent"`
	// StartHeight is the sender's reported chain/log height at connection time.
	StartHeight int64 `json:"start_height"`
	// Timestamp is the sender's Unix time (seconds) when the message was created;
	// checked by TimestampChecker if configured.
	Timestamp int64 `json:"timestamp"`
	// Optional state summary
	// StateHeadCID is the sender's advertised state root/head identifier, if any.
	StateHeadCID string `json:"state_head,omitempty"`
	// StateHeight is the height associated with StateHeadCID.
	StateHeight int64 `json:"state_height,omitempty"`
	// Admission extension
	// AuthScheme names the credential scheme in use (e.g. "token-ed25519-v1").
	AuthScheme string `json:"auth_scheme,omitempty"`
	// AuthProof carries the signed token proving admission, format defined by AuthScheme.
	AuthProof string `json:"auth_proof,omitempty"` // carries signed token
	// Optional discovery extensions
	// WantPeerlist requests that the responder include a peer sample in its own VersionMsg.
	WantPeerlist bool `json:"want_peerlist,omitempty"`
	// ListenAddrs are the sender's own advertised listen multiaddrs.
	ListenAddrs []string `json:"listen_addrs,omitempty"`
	// Peers is a sample of other known peers, encoded as multiaddrs with a
	// trailing /p2p/<peerID> component.
	Peers []string `json:"peers,omitempty"` // multiaddrs with /p2p/<peerID>
	// Challenge-response: base64(sign(remote_nonce)) proving possession of private key
	// ChallengeResponse is base64(sign(remote's Nonce)), proving the sender controls
	// the private key behind its libp2p peer ID.
	ChallengeResponse string `json:"challenge_response,omitempty"`
}

// VerAckMsg is the final acknowledgement message of the handshake. It carries no
// payload; its receipt simply confirms the sender accepted the peer's VersionMsg.
type VerAckMsg struct{}

// HandshakeLocal holds the local node's parameters advertised during the handshake.
type HandshakeLocal struct {
	// Agent identifies this node's software/version, e.g. "sng40/0.1.0".
	Agent string
	// Services is the bitmask of services this node offers.
	Services uint64
	// StartHeight is this node's reported chain/log height.
	StartHeight int64
	// WantPeerlist requests a peer sample from the remote responder.
	WantPeerlist bool
	// ListenAddrs are this node's own advertised listen multiaddrs.
	ListenAddrs []string
	// Optional state summary to advertise
	// StateHeadCID is this node's advertised state root/head identifier, if any.
	StateHeadCID string
	// StateHeight is the height associated with StateHeadCID.
	StateHeight int64
}

// HandshakePolicy configures validation, credential requirements, anti-replay, and
// attack-mitigation checks applied during the handshake.
type HandshakePolicy struct {
	// MinAgentVersion is the minimum accepted Agent version tail (e.g. "0.1.0");
	// "" disables the version check.
	MinAgentVersion string // "" to disable version check
	// ServicesAllow is a bitmask; 0 means allow any services, otherwise the
	// remote's advertised Services must be a subset of this mask.
	ServicesAllow uint64 // 0 means allow any, else remoteServices must be subset of this mask
	// Timeout bounds each side of the handshake exchange; policyTimeout defaults
	// this to 5s when Timeout is zero or negative.
	Timeout time.Duration
	// Admission controls (token-based)
	// RequireCredential, when true, requires both sides to present a valid
	// AuthScheme/AuthProof matching this policy's configuration.
	RequireCredential bool
	// AuthScheme names the required credential scheme, e.g. "token-ed25519-v1".
	AuthScheme string // "token-ed25519-v1"
	// CAPubKeys are one or more Ed25519 public keys accepted as token issuers;
	// a token is valid if it verifies against any key in this list.
	CAPubKeys [][]byte // one or more ed25519 public keys
	// Token is this node's own signed admission token, sent as AuthProof.
	Token string // signed token carried in AuthProof
	// Anti-replay (optional; nil = skip)
	// NonceCache, if set, rejects VersionMsg nonces already seen from the same peer.
	NonceCache *NonceCache
	// MessageHashCache, if set, rejects VersionMsg payloads already seen from the same peer.
	MessageHashCache *MessageHashCache
	// TimestampChecker, if set, rejects VersionMsg timestamps outside the acceptable window.
	TimestampChecker *TimestampChecker
	// Attack mitigation (optional; nil = skip)
	// AttackMitigation, if set, applies ban-list, rate-limit, eclipse, misbehavior,
	// and resource-cap protections around the handshake and its resulting connection.
	AttackMitigation *AttackMitigation
}

// PeerProvider is used by the responder to include a small peer sample.
type PeerProvider func(max int) []peer.AddrInfo

// EnableAntiReplay adds NonceCache, MessageHashCache, and TimestampChecker to the policy
// and starts their expunge loops. Returns a cleanup func to call on shutdown.
//
// Parameters:
//   - ctx (context.Context): controls the lifetime of the NonceCache and MessageHashCache expunge goroutines.
//   - policy (*HandshakePolicy): policy to populate with newly created anti-replay components.
//
// Returns:
//   - cleanup (func()): stops the NonceCache and MessageHashCache expunge loops; call on shutdown.
func EnableAntiReplay(ctx context.Context, policy *HandshakePolicy) (cleanup func()) {
	nc := NewNonceCache()
	mhc := NewMessageHashCache()
	tc := NewTimestampChecker()
	go nc.Start(ctx)
	go mhc.Start(ctx)
	policy.NonceCache = nc
	policy.MessageHashCache = mhc
	policy.TimestampChecker = tc
	return func() {
		nc.Stop()
		mhc.Stop()
	}
}

// EnableAttackMitigation adds BanList, EclipseLimiter, PeerRateLimiter, and PeerMisbehaviorScorer
// to the policy. Starts a decay loop for misbehavior scores. Returns a cleanup func (no-op; decay
// exits when ctx is cancelled).
//
// Parameters:
//   - ctx (context.Context): controls the lifetime of the misbehavior-score decay goroutine.
//   - policy (*HandshakePolicy): policy to populate with a newly created AttackMitigation bundle.
//
// Returns:
//   - cleanup (func()): a no-op; the decay loop instead exits when ctx is cancelled.
func EnableAttackMitigation(ctx context.Context, policy *HandshakePolicy) (cleanup func()) {
	am := &AttackMitigation{
		BanList:            NewBanList(),
		Eclipse:            NewEclipseLimiter(ASNResolverOption(NewCymruASNResolver())),
		RateLimiter:        NewPeerRateLimiter(),
		Misbehavior:        NewPeerMisbehaviorScorer(),
		AddressBucketStore: NewAddressBucketStore(),
		ResourceCap:        NewPeerResourceCap(),
	}
	ticker := time.NewTicker(DefaultMisbehaviorDecayPeriod)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				am.Misbehavior.Decay()
			}
		}
	}()
	policy.AttackMitigation = am
	return func() {}
}

// RegisterHandshake installs a responder handler on the host.
//
// Parameters:
//   - h (host.Host): the host to register the handshake stream handler on.
//   - local (HandshakeLocal): this node's parameters advertised to initiators.
//   - policy (HandshakePolicy): validation, anti-replay, and attack-mitigation policy applied to incoming handshakes.
func RegisterHandshake(h host.Host, local HandshakeLocal, policy HandshakePolicy) {
	h.SetStreamHandler(HandshakeProtocolID, func(s network.Stream) {
		defer s.Close()
		_ = responder(s, h, local, policy, nil)
	})
}

// RegisterHandshakeWithPeers installs a responder that can include a peer sample.
//
// Parameters:
//   - h (host.Host): the host to register the handshake stream handler on.
//   - local (HandshakeLocal): this node's parameters advertised to initiators.
//   - policy (HandshakePolicy): validation, anti-replay, and attack-mitigation policy applied to incoming handshakes.
//   - provider (PeerProvider): supplies a peer sample when the initiator sets WantPeerlist.
func RegisterHandshakeWithPeers(h host.Host, local HandshakeLocal, policy HandshakePolicy, provider PeerProvider) {
	RegisterHandshakeWithPeersAndCallback(h, local, policy, provider, nil)
}

// RegisterHandshakeWithPeersAndCallback installs a responder that can include
// a peer sample and reports a successfully validated initiator. The callback
// runs only after the complete version/verack exchange succeeds, so a
// HandshakeGate can use it to admit application streams without requiring a
// redundant reverse-direction handshake.
func RegisterHandshakeWithPeersAndCallback(
	h host.Host,
	local HandshakeLocal,
	policy HandshakePolicy,
	provider PeerProvider,
	onAccepted func(peer.ID),
) {
	h.SetStreamHandler(HandshakeProtocolID, func(s network.Stream) {
		defer s.Close()
		pid := s.Conn().RemotePeer()
		if err := responder(s, h, local, policy, provider); err == nil && onAccepted != nil {
			onAccepted(pid)
		}
	})
}

// HandshakeResult reports the responder's advertised state and any peers learned.
type HandshakeResult struct {
	// Learned is the set of peer addresses parsed from the responder's Peers list.
	Learned []peer.AddrInfo
	// RemoteStateHead is the responder's advertised state root/head identifier, if any.
	RemoteStateHead string
	// RemoteStateHeight is the height associated with RemoteStateHead.
	RemoteStateHeight int64
}

// PerformHandshakeWithState dials the peer and returns learned peers and remote
// state summary. Admission state is owned by HandshakeGate; callers using a
// gate must report successful completion through HandshakeGate.MarkVerified.
//
// Parameters:
//   - ctx (context.Context): controls the lifetime of the handshake stream.
//   - h (host.Host): the local host used to dial and to sign/verify challenge responses.
//   - p (peer.ID): the peer to handshake with.
//   - policy (HandshakePolicy): validation, credential, anti-replay, and attack-mitigation policy.
//   - local (HandshakeLocal): this node's parameters advertised to the responder.
//
// Returns:
//   - *HandshakeResult: learned peers and the responder's advertised state summary.
//   - error: non-nil if the stream cannot be opened or the handshake exchange/validation fails.
func PerformHandshakeWithState(ctx context.Context, h host.Host, p peer.ID, policy HandshakePolicy, local HandshakeLocal) (*HandshakeResult, error) {
	s, err := h.NewStream(ctx, p, HandshakeProtocolID)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	learned, remote, err := initiatorWithState(s, h, local, policy)
	if err != nil {
		return nil, err
	}
	return &HandshakeResult{Learned: learned, RemoteStateHead: remote.StateHeadCID, RemoteStateHeight: remote.StateHeight}, nil
}

// PerformHandshake dials the peer and runs the initiator side. Returns any peers learned.
// Admission state is deliberately not changed here: HandshakeGate performs
// the connectedness check and owns the corresponding connection-manager tag.
//
// Parameters:
//   - ctx (context.Context): controls the lifetime of the handshake stream.
//   - h (host.Host): the local host used to dial and to sign/verify challenge responses.
//   - p (peer.ID): the peer to handshake with.
//   - policy (HandshakePolicy): validation, credential, anti-replay, and attack-mitigation policy.
//   - local (HandshakeLocal): this node's parameters advertised to the responder.
//
// Returns:
//   - []peer.AddrInfo: peer addresses learned from the responder's Peers list.
//   - error: non-nil if the stream cannot be opened or the handshake exchange/validation fails.
func PerformHandshake(ctx context.Context, h host.Host, p peer.ID, policy HandshakePolicy, local HandshakeLocal) ([]peer.AddrInfo, error) {
	s, err := h.NewStream(ctx, p, HandshakeProtocolID)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	learned, _, err := initiatorWithState(s, h, local, policy)
	if err != nil {
		return nil, err
	}
	return learned, nil
}

// initiatorWithState runs the initiator side of the handshake protocol over an
// already-opened stream: it sends a VersionMsg (including credentials if
// RequireCredential), receives and validates the responder's VersionMsg (anti-replay,
// version/services, credential verification, and challenge-response verification),
// then exchanges VerAckMsg in both directions. Returns learned peers and the
// responder's VersionMsg for state summary.
//
// Parameters:
//   - s (network.Stream): the open handshake stream to the responder.
//   - h (host.Host): the local host, used to sign the challenge and look up peerstore keys.
//   - local (HandshakeLocal): this node's parameters to advertise.
//   - policy (HandshakePolicy): validation, credential, anti-replay policy.
//
// Returns:
//   - []peer.AddrInfo: peer addresses learned from the responder's Peers list.
//   - VersionMsg: the responder's full VersionMsg (used by callers for state summary).
//   - error: non-nil if encoding/decoding fails or any validation step rejects the responder.
func initiatorWithState(s network.Stream, h host.Host, local HandshakeLocal, policy HandshakePolicy) ([]peer.AddrInfo, VersionMsg, error) {
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
	if err := applyAntiReplay(policy, s.Conn().RemotePeer(), remote); err != nil {
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
	// Challenge-response: verify responder signed our nonce
	if remote.ChallengeResponse != "" {
		if err := verifyChallengeResponse(h, s.Conn().RemotePeer(), my.Nonce, remote.ChallengeResponse); err != nil {
			return nil, VersionMsg{}, err
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

// responder runs the responder side of the handshake protocol over an incoming
// stream: applies ban-list/rate-limit checks (recording misbehavior and possibly
// banning on failure, via a deferred func), receives and validates the initiator's
// VersionMsg (anti-replay, version/services, credential scheme), sends its own
// VersionMsg (including a signed challenge response and, if requested, a peer
// sample from provider), then exchanges VerAckMsg in both directions. On success,
// registers the peer with the eclipse limiter if AttackMitigation is configured.
//
// Parameters:
//   - s (network.Stream): the incoming handshake stream from the initiator.
//   - h (host.Host): the local host, used to sign the challenge and look up peerstore addresses.
//   - local (HandshakeLocal): this node's parameters to advertise.
//   - policy (HandshakePolicy): validation, credential, anti-replay, and attack-mitigation policy.
//   - provider (PeerProvider): supplies a peer sample when the initiator sets WantPeerlist; may be nil.
//
// Returns:
//   - err (error): non-nil if any protocol step fails or the initiator is rejected; on non-nil error with AttackMitigation configured, the peer's misbehavior score is incremented.
func responder(s network.Stream, h host.Host, local HandshakeLocal, policy HandshakePolicy, provider PeerProvider) (err error) {
	pid := s.Conn().RemotePeer()
	if am := policy.AttackMitigation; am != nil {
		if am.BanList.IsBanned(pid) {
			return errors.New("peer banned")
		}
		if !am.RateLimiter.Allow(pid) {
			return errors.New("rate limited")
		}
		defer func() {
			if err != nil {
				am.Misbehavior.AddMisbehavior(pid, 10)
				if am.Misbehavior.ShouldDisconnect(pid) {
					am.BanList.Ban(pid)
				}
			}
		}()
	}

	deadline := time.Now().Add(policyTimeout(policy))
	_ = s.SetDeadline(deadline)
	enc := json.NewEncoder(s)
	dec := json.NewDecoder(s)

	// 1) recv version, validate
	var remote VersionMsg
	if err = dec.Decode(&remote); err != nil {
		return err
	}
	if err = applyAntiReplay(policy, pid, remote); err != nil {
		return err
	}
	if err = validateVersion(remote, policy); err != nil {
		return err
	}
	// If credential required: ensure scheme present; verify token now.
	if policy.RequireCredential && remote.AuthScheme != policy.AuthScheme {
		err = errors.New("unsupported auth scheme")
		return err
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
	// Challenge-response: sign initiator's nonce
	sig, signErr := signChallenge(h, remote.Nonce)
	if signErr != nil {
		err = signErr
		return err
	}
	my.ChallengeResponse = base64.StdEncoding.EncodeToString(sig)
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
	if err = enc.Encode(&my); err != nil {
		return err
	}

	// 3) recv verack (no payload expected)
	var ack VerAckMsg
	if err = dec.Decode(&ack); err != nil {
		return err
	}
	// responder already verified initiator's token from Version

	// 4) send verack (empty)
	if err = enc.Encode(&VerAckMsg{}); err != nil {
		return err
	}
	if am := policy.AttackMitigation; am != nil {
		_ = am.Eclipse.Register(context.Background(), pid, h.Peerstore().Addrs(pid))
	}
	return nil
}

// applyAntiReplay runs the configured anti-replay checks against an incoming
// VersionMsg, in order: timestamp window, nonce reuse, and duplicate message hash
// (computed via SHA-256 over the JSON-marshaled message). Any configured checker
// left nil is skipped.
//
// Parameters:
//   - policy (HandshakePolicy): supplies the optional TimestampChecker, NonceCache, and MessageHashCache.
//   - pid (peer.ID): the peer the message was received from.
//   - remote (VersionMsg): the message to check.
//
// Returns:
//   - error: ErrExpiredTimestamp, ErrReusedNonce, ErrDuplicateMessageHash, a JSON marshaling error, or nil if all configured checks pass.
func applyAntiReplay(policy HandshakePolicy, pid peer.ID, remote VersionMsg) error {
	if policy.TimestampChecker != nil {
		if err := policy.TimestampChecker.RejectExpiredUnix(remote.Timestamp); err != nil {
			return err
		}
	}
	if policy.NonceCache != nil {
		if err := policy.NonceCache.RecordNonce(pid, remote.Nonce); err != nil {
			return err
		}
	}
	if policy.MessageHashCache != nil {
		// Hash the message for duplicate detection
		b, err := json.Marshal(remote)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		if err := policy.MessageHashCache.RecordHash(pid, sum[:]); err != nil {
			return err
		}
	}
	return nil
}

// validateVersion checks a received VersionMsg against policy: services must be a
// subset of ServicesAllow (if non-zero), Agent must meet MinAgentVersion (if set),
// and if RequireCredential is true, AuthScheme must match policy.AuthScheme and
// AuthProof must be non-empty. It does not verify the credential's cryptographic
// validity; that is done separately (see verifyTokenAny).
//
// Parameters:
//   - v (VersionMsg): the message to validate.
//   - policy (HandshakePolicy): the policy to validate against.
//
// Returns:
//   - error: describes the first validation failure encountered, nil if v passes all checks.
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

// policyTimeout returns the effective handshake timeout for a policy, defaulting
// to 5 seconds when Timeout is unset or non-positive.
//
// Parameters:
//   - p (HandshakePolicy): the policy whose Timeout field is read.
//
// Returns:
//   - time.Duration: p.Timeout if positive, otherwise 5 seconds.
func policyTimeout(p HandshakePolicy) time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return 5 * time.Second
}

// agentOK expects agent like "sng40/0.1.0"; compares the numeric tail against min.
//
// Parameters:
//   - agent (string): the remote's advertised agent string, e.g. "sng40/0.1.0".
//   - min (string): the minimum acceptable agent string in the same format.
//
// Returns:
//   - bool: true if agent's version tail is >= min's version tail.
func agentOK(agent string, min string) bool {
	have := tailSemver(agent)
	want := tailSemver(min)
	return semverGTE(have, want)
}

// tailSemver extracts the version suffix after the last '/' in an agent string
// (e.g. "sng40/0.1.0" -> "0.1.0"); returns s unchanged if it contains no '/'.
//
// Parameters:
//   - s (string): the agent string to extract from.
//
// Returns:
//   - string: the version tail, or s if there is no '/' separator.
func tailSemver(s string) string {
	i := strings.LastIndexByte(s, '/')
	if i >= 0 && i+1 < len(s) {
		return s[i+1:]
	}
	return s
}

// semverGTE compares two 3-component version strings (as parsed by parse3) and
// reports whether a >= b, comparing major, then minor, then patch components in order.
//
// Parameters:
//   - a (string): the version string to test.
//   - b (string): the version string to compare against.
//
// Returns:
//   - bool: true if a's version is greater than or equal to b's.
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

// parse3 parses up to the first three dot-separated numeric components of s into
// a [3]int (e.g. "1.2.3" -> [1,2,3]). Missing components default to 0; components
// that fail to parse as integers also default to 0 (strconv.Atoi errors are ignored).
//
// Parameters:
//   - s (string): a dot-separated version string.
//
// Returns:
//   - [3]int: the parsed major, minor, and patch components.
func parse3(s string) [3]int {
	var out [3]int
	parts := strings.SplitN(s, ".", 3)
	for i := 0; i < len(parts) && i < 3; i++ {
		n, _ := strconv.Atoi(parts[i])
		out[i] = n
	}
	return out
}

// parsePeerlist converts string multiaddrs with /p2p into AddrInfos. Entries that
// fail to parse as multiaddrs or lack a valid /p2p/<peerID> component are silently
// skipped.
//
// Parameters:
//   - in ([]string): multiaddr strings, each expected to include a /p2p/<peerID> component.
//
// Returns:
//   - []peer.AddrInfo: successfully parsed peer address infos.
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

// verifyToken checks that proof is a base64-encoded Ed25519 signature over the raw
// peer ID bytes, valid under caPub. Note: despite the doc comments elsewhere
// describing a richer token format (CBOR/JSON with {pid, exp, sig} and HMAC-based
// proofs), this implementation only verifies the simplified format actually used:
// base64(ed25519.Sign(CA, []byte(peerID))), with no expiry field.
//
// Parameters:
//   - caPub ([]byte): the Ed25519 public key bytes of the certificate authority to verify against.
//   - pid (peer.ID): the peer ID the token is expected to cover.
//   - proof (string): base64-encoded signature to verify.
//
// Returns:
//   - bool: true if proof is a valid signature over pid's bytes under caPub.
func verifyToken(caPub []byte, pid peer.ID, proof string) bool {
	pub := ed25519.PublicKey(caPub)
	sig, err := base64.StdEncoding.DecodeString(proof)
	if err != nil {
		return false
	}
	msg := []byte(pid)
	return ed25519.Verify(pub, msg, sig)
}

// verifyTokenAny reports whether proof verifies against any of the given CA public
// keys, via verifyToken.
//
// Parameters:
//   - caPubs ([][]byte): candidate Ed25519 public keys to check against.
//   - pid (peer.ID): the peer ID the token is expected to cover.
//   - proof (string): base64-encoded signature to verify.
//
// Returns:
//   - bool: true if proof verifies under at least one key in caPubs.
func verifyTokenAny(caPubs [][]byte, pid peer.ID, proof string) bool {
	for _, k := range caPubs {
		if verifyToken(k, pid, proof) {
			return true
		}
	}
	return false
}

// nonceBytes encodes a uint64 nonce as 8 big-endian bytes, the canonical form
// signed/verified for challenge-response.
//
// Parameters:
//   - n (uint64): the nonce value to encode.
//
// Returns:
//   - []byte: the 8-byte big-endian encoding of n.
func nonceBytes(n uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, n)
	return b
}

// signChallenge signs the given nonce (as produced by nonceBytes) using the local
// host's own private key from its peerstore, proving possession of the private key
// behind this node's peer ID.
//
// Parameters:
//   - h (host.Host): the local host whose peerstore holds its own private key.
//   - nonce (uint64): the nonce to sign (typically the remote peer's advertised Nonce).
//
// Returns:
//   - []byte: the signature bytes.
//   - error: non-nil if no private key is found in the peerstore or signing fails.
func signChallenge(h host.Host, nonce uint64) ([]byte, error) {
	priv := h.Peerstore().PrivKey(h.ID())
	if priv == nil {
		return nil, errors.New("no private key in peerstore")
	}
	return priv.Sign(nonceBytes(nonce))
}

// verifyChallengeResponse verifies that proofB64 is a valid base64-encoded
// signature by remote over nonce, using remote's public key as recorded in the
// local host's peerstore.
//
// Parameters:
//   - h (host.Host): the local host whose peerstore holds remote's public key.
//   - remote (peer.ID): the peer that allegedly produced the signature.
//   - nonce (uint64): the nonce that was signed (typically this node's own Nonce sent to remote).
//   - proofB64 (string): base64-encoded signature to verify.
//
// Returns:
//   - error: non-nil if remote's public key is unknown, proofB64 fails to decode, or the signature does not verify.
func verifyChallengeResponse(h host.Host, remote peer.ID, nonce uint64, proofB64 string) error {
	pub := h.Peerstore().PubKey(remote)
	if pub == nil {
		return errors.New("no public key for remote in peerstore")
	}
	sig, err := base64.StdEncoding.DecodeString(proofB64)
	if err != nil {
		return fmt.Errorf("invalid challenge response encoding: %w", err)
	}
	ok, err := pub.Verify(nonceBytes(nonce), sig)
	if err != nil || !ok {
		return errors.New("challenge-response verification failed")
	}
	return nil
}

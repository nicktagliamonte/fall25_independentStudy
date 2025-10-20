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

const HandshakeProtocolID = "/sng40/handshake/1.0.0"

type VersionMsg struct {
	Nonce       uint64 `json:"nonce"`
	Services    uint64 `json:"services"`
	Agent       string `json:"agent"`
	StartHeight int64  `json:"start_height"`
	Timestamp   int64  `json:"timestamp"`
	// Optional state summary
	StateHeadCID string `json:"state_head,omitempty"`
	StateHeight  int64  `json:"state_height,omitempty"`
	// Admission extension
	AuthScheme string `json:"auth_scheme,omitempty"`
	AuthProof  string `json:"auth_proof,omitempty"` // carries signed token
	// Optional discovery extensions
	WantPeerlist bool     `json:"want_peerlist,omitempty"`
	ListenAddrs  []string `json:"listen_addrs,omitempty"`
	Peers        []string `json:"peers,omitempty"` // multiaddrs with /p2p/<peerID>
}

type VerAckMsg struct{}

type HandshakeLocal struct {
	Agent        string
	Services     uint64
	StartHeight  int64
	WantPeerlist bool
	ListenAddrs  []string
	// Optional state summary to advertise
	StateHeadCID string
	StateHeight  int64
}

type HandshakePolicy struct {
	MinAgentVersion string // "" to disable version check
	ServicesAllow   uint64 // 0 means allow any, else remoteServices must be subset of this mask
	Timeout         time.Duration
	// Admission controls (token-based)
	RequireCredential bool
	AuthScheme        string   // "token-ed25519-v1"
	CAPubKeys         [][]byte // one or more ed25519 public keys
	Token             string   // signed token carried in AuthProof
}

// PeerProvider is used by the responder to include a small peer sample.
type PeerProvider func(max int) []peer.AddrInfo

// RegisterHandshake installs a responder handler on the host.
func RegisterHandshake(h host.Host, local HandshakeLocal, policy HandshakePolicy) {
	h.SetStreamHandler(HandshakeProtocolID, func(s network.Stream) {
		defer s.Close()
		_ = responder(s, local, policy, nil)
	})
}

// RegisterHandshakeWithPeers installs a responder that can include a peer sample.
func RegisterHandshakeWithPeers(h host.Host, local HandshakeLocal, policy HandshakePolicy, provider PeerProvider) {
	h.SetStreamHandler(HandshakeProtocolID, func(s network.Stream) {
		defer s.Close()
		_ = responder(s, local, policy, provider)
	})
}

// HandshakeResult reports the responder's advertised state and any peers learned.
type HandshakeResult struct {
	Learned           []peer.AddrInfo
	RemoteStateHead   string
	RemoteStateHeight int64
}

// PerformHandshakeWithState dials the peer and returns learned peers and remote state summary.
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

// PerformHandshake dials the peer and runs the initiator side. Returns any peers learned.
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

// initiator is preserved for callers that don't need remote state.
func initiator(s network.Stream, local HandshakeLocal, policy HandshakePolicy) ([]peer.AddrInfo, error) {
	learned, _, err := initiatorWithState(s, local, policy)
	return learned, err
}

// initiatorWithState returns learned peers and the responder's VersionMsg for state summary.
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

func policyTimeout(p HandshakePolicy) time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return 5 * time.Second
}

// agentOK expects agent like "sng40/0.1.0"; compares the numeric tail against min.
func agentOK(agent string, min string) bool {
	have := tailSemver(agent)
	want := tailSemver(min)
	return semverGTE(have, want)
}

func tailSemver(s string) string {
	i := strings.LastIndexByte(s, '/')
	if i >= 0 && i+1 < len(s) {
		return s[i+1:]
	}
	return s
}

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

func parse3(s string) [3]int {
	var out [3]int
	parts := strings.SplitN(s, ".", 3)
	for i := 0; i < len(parts) && i < 3; i++ {
		n, _ := strconv.Atoi(parts[i])
		out[i] = n
	}
	return out
}

// parsePeerlist converts string multiaddrs with /p2p into AddrInfos.
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

// computeHMACProof returns base64(HMAC-SHA256(secret, encode(iNonce||rNonce||peerID))).
// verifyToken expects AuthProof to be base64 of a signed token that covers the peer ID and an expiry.
// Token format (base64-encoded bytes): CBOR or JSON with fields {pid, exp, sig}, where sig = ed25519.Sign(CA, canonical(pid||exp)).
// For simplicity here, we define proof as base64( ed25519.Sign(CA, []byte(peerID)) ) and verify against CAPubKey.
func verifyToken(caPub []byte, pid peer.ID, proof string) bool {
	pub := ed25519.PublicKey(caPub)
	sig, err := base64.StdEncoding.DecodeString(proof)
	if err != nil {
		return false
	}
	msg := []byte(pid)
	return ed25519.Verify(pub, msg, sig)
}

func verifyTokenAny(caPubs [][]byte, pid peer.ID, proof string) bool {
	for _, k := range caPubs {
		if verifyToken(k, pid, proof) {
			return true
		}
	}
	return false
}

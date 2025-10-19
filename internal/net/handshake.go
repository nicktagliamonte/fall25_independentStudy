// Purpose: Simple version/verack handshake over a libp2p stream.

package net

import (
	"context"
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
}

type HandshakePolicy struct {
	MinAgentVersion string // "" to disable version check
	ServicesAllow   uint64 // 0 means allow any, else remoteServices must be subset of this mask
	Timeout         time.Duration
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

// PerformHandshake dials the peer and runs the initiator side. Returns any peers learned.
func PerformHandshake(ctx context.Context, h host.Host, p peer.ID, policy HandshakePolicy, local HandshakeLocal) ([]peer.AddrInfo, error) {
	s, err := h.NewStream(ctx, p, HandshakeProtocolID)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	learned, err := initiator(s, local, policy)
	if err != nil {
		return nil, err
	}
	// Mark the peer as verified for downstream gating/policy.
	h.ConnManager().TagPeer(p, handshakeOkTag, 1)
	return learned, nil
}

func initiator(s network.Stream, local HandshakeLocal, policy HandshakePolicy) ([]peer.AddrInfo, error) {
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
	}
	if err := enc.Encode(&my); err != nil {
		return nil, err
	}

	// 2) recv remote version and validate
	var remote VersionMsg
	if err := dec.Decode(&remote); err != nil {
		return nil, err
	}
	if err := validateVersion(remote, policy); err != nil {
		return nil, err
	}

	learned := parsePeerlist(remote.Peers)

	// 3) send verack
	if err := enc.Encode(&VerAckMsg{}); err != nil {
		return nil, err
	}

	// 4) require verack from remote
	var ack VerAckMsg
	if err := dec.Decode(&ack); err != nil {
		return nil, err
	}

	return learned, nil
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

	// 2) send version
	my := VersionMsg{
		Nonce:       uint64(time.Now().UnixNano()),
		Services:    local.Services,
		Agent:       local.Agent,
		StartHeight: local.StartHeight,
		Timestamp:   time.Now().Unix(),
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

	// 3) recv verack
	var ack VerAckMsg
	if err := dec.Decode(&ack); err != nil {
		return err
	}

	// 4) send verack
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

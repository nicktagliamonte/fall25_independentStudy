// Purpose: PermissionChecker integration with handshake/auth for P2P tuple space.
// Per planTwo Phase 6.1: P2P tuple space requires permissioned users; uses HandshakePolicy auth state.
//
// Wiring: when creating P2PTupleSpace for use with Router, set the checker:
//
//	p2pTS := tuplespace.NewP2PTupleSpace(tshAddr, hostIP, appId)
//	p2pTS.SetPermissionChecker(net.NewHandshakePermissionChecker(policy))

package net

import (
	"github.com/nicktagliamonte/fall25_independentStudy/internal/tuplespace"
)

type handshakePermissionChecker struct {
	policy HandshakePolicy
}

// NewHandshakePermissionChecker returns a PermissionChecker that allows P2P tuple space
// access when the node has valid handshake auth config. When RequireCredential is true,
// AuthScheme must be set and (Token or CAPubKeys) present; otherwise returns ErrPermissionDenied.
func NewHandshakePermissionChecker(policy HandshakePolicy) tuplespace.PermissionChecker {
	return &handshakePermissionChecker{policy: policy}
}

func (h *handshakePermissionChecker) CheckPermission(operation string) error {
	if !h.policy.RequireCredential {
		return nil
	}
	if h.policy.AuthScheme == "" {
		return tuplespace.ErrPermissionDenied
	}
	if h.policy.Token == "" && len(h.policy.CAPubKeys) == 0 {
		return tuplespace.ErrPermissionDenied
	}
	return nil
}

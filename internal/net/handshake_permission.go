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

// handshakePermissionChecker adapts a HandshakePolicy into a tuplespace.PermissionChecker,
// gating P2P tuple space operations on whether the node is configured with valid
// handshake/auth credentials.
type handshakePermissionChecker struct {
	policy HandshakePolicy
}

// NewHandshakePermissionChecker returns a PermissionChecker that allows P2P tuple space
// access when the node has valid handshake auth config. When RequireCredential is true,
// AuthScheme must be set and (Token or CAPubKeys) present; otherwise returns ErrPermissionDenied.
//
// Parameters:
//   - policy (HandshakePolicy): the handshake policy whose credential requirements are checked.
//
// Returns:
//   - tuplespace.PermissionChecker: a checker that validates the policy on each CheckPermission call.
func NewHandshakePermissionChecker(policy HandshakePolicy) tuplespace.PermissionChecker {
	return &handshakePermissionChecker{policy: policy}
}

// CheckPermission implements tuplespace.PermissionChecker. It ignores the specific
// operation name and instead validates that the underlying HandshakePolicy has
// sufficient credential configuration: if RequireCredential is false, any operation
// is allowed; otherwise AuthScheme must be set and at least one of Token or
// CAPubKeys must be present.
//
// Parameters:
//   - operation (string): the name of the operation being checked; unused, kept to satisfy the PermissionChecker interface.
//
// Returns:
//   - error: tuplespace.ErrPermissionDenied if credentials are required but missing/incomplete, nil otherwise.
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

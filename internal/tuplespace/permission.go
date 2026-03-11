// Purpose: Permission enforcement for P2P tuple space (application management).
// Per planTwo Phase 6.1: top-level tuple space is for permissioned users only.

package tuplespace

import "errors"

// ErrPermissionDenied is returned when a caller lacks permission for the operation.
var ErrPermissionDenied = errors.New("permission denied")

// Operation names for permission checks.
const (
	OpTsPut  = "TsPut"
	OpTsGet  = "TsGet"
	OpTsRead = "TsRead"
)

// PermissionChecker checks if the caller has permission for a tuple space operation.
// Implementations may use KYC token, handshake/auth system, or other identity.
// When nil or not set, no permission check is performed (backward compatible).
type PermissionChecker interface {
	CheckPermission(operation string) error
}

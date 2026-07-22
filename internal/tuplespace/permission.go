// Purpose: Permission enforcement for P2P tuple space (application management).
// Per planTwo Phase 6.1: top-level tuple space is for permissioned users only.

package tuplespace

import "errors"

// ErrPermissionDenied is returned when a caller lacks permission for the operation.
// Implementations of PermissionChecker are not required to return exactly this
// error value, but callers can use it as a sentinel for the common case.
var ErrPermissionDenied = errors.New("permission denied")

// Operation names for permission checks. These are the operation identifiers
// passed to PermissionChecker.CheckPermission by P2PTupleSpace, one per
// TupleSpace method (TsPut, TsGet, TsRead).
const (
	OpTsPut  = "TsPut"
	OpTsGet  = "TsGet"
	OpTsRead = "TsRead"
)

// PermissionChecker checks if the caller has permission for a tuple space operation.
// Implementations may use KYC token, handshake/auth system, or other identity.
// When nil or not set, no permission check is performed (backward compatible).
type PermissionChecker interface {
	// CheckPermission verifies the caller is authorized to perform the named
	// tuple space operation.
	//
	// Parameters:
	//   - operation (string): one of OpTsPut, OpTsGet, OpTsRead identifying
	//     the operation being attempted.
	//
	// Returns:
	//   - error: nil if the operation is permitted; a non-nil error (e.g.
	//     ErrPermissionDenied) if it is not.
	CheckPermission(operation string) error
}

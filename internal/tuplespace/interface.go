// Package tuplespace provides Tarsus tuple-space implementations.
package tuplespace

// TupleSpace defines the interface for tuple space operations.
// Implementations provide Put (non-consuming), Read (non-consuming), and Get (consuming) semantics.
// Concrete implementations in this package include DHTTupleSpace (exact-match,
// unpermissioned storage layer), P2PTupleSpace (regex/wildcard, permissioned
// admin layer), Router (dispatches to DHT/P2P/PHT by pattern shape), and
// TokenFallbackTupleSpace (checks /tokens/ before delegating).
type TupleSpace interface {
	// TsPut stores a tuple in the tuple space.
	//
	// Parameters:
	//   - tpname (string): the tuple name/key to store under.
	//   - tpvalue ([]byte): the tuple payload to store.
	//
	// Returns:
	//   - int: status/error code (0 on success, one of the *_ER constants on failure).
	//   - error: non-nil if the underlying store operation failed.
	TsPut(tpname string, tpvalue []byte) (int, error)

	// TsGet retrieves and removes (consumes) a tuple matching the given name/expression.
	//
	// Parameters:
	//   - tpname (string): the tuple name or pattern/expression to match.
	//
	// Returns:
	//   - []byte: the retrieved tuple data.
	//   - error: non-nil if no matching tuple was found or the operation failed.
	TsGet(tpname string) ([]byte, error)

	// TsRead retrieves a tuple matching the given name/expression without removing it (non-consuming).
	//
	// Parameters:
	//   - tpname (string): the tuple name or pattern/expression to match.
	//
	// Returns:
	//   - []byte: the retrieved tuple data.
	//   - error: non-nil if no matching tuple was found or the operation failed.
	TsRead(tpname string) ([]byte, error)
}

// NamedTupleReplacer is an optional extension for application records that
// have one current value per exact tuple name. It does not change Linda-style
// TsPut multiset semantics: callers must opt into replacement explicitly.
type NamedTupleReplacer interface {
	TsReplace(tpname string, tpvalue []byte) (int, error)
}

// Error codes matching tslib.go (synergy.h). These are returned as the int
// status/error code from TsPut/TsGet/TsRead implementations to signal the
// specific operation that failed, mirroring the legacy C tuple space library's
// error codes so callers/ports of that code observe consistent values.
const (
	TSPUT_ER  = -106
	TSGET_ER  = -107
	TSREAD_ER = -108
)

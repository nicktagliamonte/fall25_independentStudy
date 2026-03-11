// Package tuplespace provides tuple space implementations for vn-IPFS.
package tuplespace

// TupleSpace defines the interface for tuple space operations.
// Implementations provide Put (non-consuming), Read (non-consuming), and Get (consuming) semantics.
type TupleSpace interface {
	// TsPut stores a tuple in the tuple space.
	// Returns status/error code.
	TsPut(tpname string, tpvalue []byte) (int, error)

	// TsGet retrieves and removes (consumes) a tuple matching the given name/expression.
	// Returns the tuple data.
	TsGet(tpname string) ([]byte, error)

	// TsRead retrieves a tuple matching the given name/expression without removing it (non-consuming).
	// Returns the tuple data.
	TsRead(tpname string) ([]byte, error)
}

// Error codes matching tslib.go (synergy.h)
const (
	TSPUT_ER  = -106
	TSGET_ER  = -107
	TSREAD_ER = -108
)

// Purpose: Routing decision logic for tuple space operations.
// Routes exact match → DHT, prefix/substring → PHT, complex regex → P2P.
// Per planTwo 6.2: storage queries → DHT (no permission check); admin queries → P2P (with permission).

package tuplespace

import (
	"context"
	"errors"
	"strings"

	"github.com/nicktagliamonte/fall25_independentStudy/internal/pht"
)

// Router dispatches tuple space operations to the appropriate implementation
// based on query pattern type.
// Storage (exact match, simple wildcard via PHT) → DHT, no permission check.
// Admin/coordination (complex regex) → P2P, permission checked by P2P.
type Router struct {
	// dhtTS is the exact-match, unpermissioned storage-layer tuple space.
	dhtTS *DHTTupleSpace
	// p2pTS is the permissioned tuple space used for complex regex patterns
	// and as the fallback when PHT resolution is unavailable.
	p2pTS *P2PTupleSpace
	// phtStore is the DHT-backed ValueStore used to resolve simple wildcard
	// (prefix/substring) patterns to concrete tuple names via the PHT.
	phtStore pht.ValueStore // DHT-backed ValueStore for PHT queries
}

// Ensure Router implements TupleSpace interface
var _ TupleSpace = (*Router)(nil)

// NewRouter creates a router with DHT and P2P tuple space implementations.
//
// Parameters:
//   - dhtTS (*DHTTupleSpace): backend for exact-match operations.
//   - p2pTS (*P2PTupleSpace): backend for complex regex and PHT-unavailable fallback.
//   - phtStore (pht.ValueStore): DHT-backed store used to resolve simple wildcard patterns via the PHT.
//
// Returns:
//   - *Router: the constructed router.
func NewRouter(dhtTS *DHTTupleSpace, p2pTS *P2PTupleSpace, phtStore pht.ValueStore) *Router {
	return &Router{
		dhtTS:    dhtTS,
		p2pTS:    p2pTS,
		phtStore: phtStore,
	}
}

// isExactMatch checks if the pattern contains only simple wildcards (*) or is exact.
// Returns true if pattern is exact (no wildcards) or simple wildcard (prefix/substring).
//
// Parameters:
//   - pattern (string): the tuple name/pattern to classify.
//
// Returns:
//   - bool: true if pattern has no wildcard/regex characters at all (i.e. is
//     an exact tuple name); false if it contains "*" or any regex metacharacter.
func isExactMatch(pattern string) bool {
	// Check for complex regex patterns (beyond simple *)
	// Simple wildcards: *, *pattern, pattern*, *pattern*
	// Complex regex: anything with regex metacharacters beyond *
	hasComplexRegex := strings.ContainsAny(pattern, ".+?^$[]{}|()\\")
	if hasComplexRegex {
		return false
	}
	// If only contains * (simple wildcard), it's handled by PHT
	// If no wildcards, it's exact match
	return !strings.Contains(pattern, "*")
}

// isSimpleWildcard checks if pattern uses only simple wildcards (prefix or substring).
//
// Parameters:
//   - pattern (string): the tuple name/pattern to classify.
//
// Returns:
//   - bool: true if pattern contains "*" and no regex metacharacters beyond
//     "*" (i.e. it is a prefix/substring pattern resolvable via the PHT);
//     false otherwise (no wildcard at all, or a complex regex pattern).
func isSimpleWildcard(pattern string) bool {
	if !strings.Contains(pattern, "*") {
		return false
	}
	// Check for complex regex
	if strings.ContainsAny(pattern, ".+?^$[]{}|()\\") {
		return false
	}
	// Simple wildcard patterns: *pattern, pattern*, *pattern*
	return true
}

// TsPut routes Put operations.
// Exact tuple names → DHT tuple space.
// Wildcard/regex patterns → P2P tuple space (for coordination/admin tasks).
//
// Parameters:
//   - tpname (string): the tuple name or pattern to store under.
//   - tpvalue ([]byte): the tuple payload.
//
// Returns:
//   - int: status/error code from the chosen backend (DHTTupleSpace or P2PTupleSpace).
//   - error: non-nil if the chosen backend's TsPut failed.
func (r *Router) TsPut(tpname string, tpvalue []byte) (int, error) {
	if isExactMatch(tpname) {
		// Exact match: use DHT tuple space
		return r.dhtTS.TsPut(tpname, tpvalue)
	}
	// Wildcard/regex: use P2P tuple space
	return r.p2pTS.TsPut(tpname, tpvalue)
}

// tupleSpaceOp performs a single named tuple space operation (either
// DHTTupleSpace.TsGet/TsRead or P2PTupleSpace.TsGet/TsRead) against name.
type tupleSpaceOp func(name string) ([]byte, error)

// resolveAndCall implements the routing logic shared by TsGet and TsRead:
// exact match → dhtOp(tpname) directly; simple wildcard (prefix/substring)
// with a configured PHT store → resolve tpname to candidate tuple names via
// the PHT, then try dhtOp on each candidate in turn until one succeeds;
// complex regex, or a wildcard with no PHT store configured → p2pOp(tpname).
//
// Parameters:
//   - tpname (string): the tuple name or pattern to resolve.
//   - dhtOp (tupleSpaceOp): the DHT tuple space operation to apply (TsGet or TsRead).
//   - p2pOp (tupleSpaceOp): the P2P tuple space operation to apply as fallback
//     (TsGet or TsRead; must match dhtOp's operation kind).
//
// Returns:
//   - []byte: the tuple data (first matching name whose dhtOp call succeeds,
//     when resolved via PHT; otherwise straight from the chosen backend).
//   - error: non-nil if PHT resolution fails, no matching tuple names are
//     found, all resolved names fail dhtOp, or the underlying backend's
//     operation fails.
func (r *Router) resolveAndCall(tpname string, dhtOp, p2pOp tupleSpaceOp) ([]byte, error) {
	if isExactMatch(tpname) {
		// Exact match: use DHT tuple space
		return dhtOp(tpname)
	}

	if isSimpleWildcard(tpname) && r.phtStore != nil {
		// Simple wildcard: use PHT to find matching tuple names
		parsed := pht.ParseQuery(tpname)
		ctx := context.Background()

		var matchingNames []string
		var err error

		switch parsed.Kind {
		case pht.QueryPrefix:
			// Prefix query: use PHT tree descent
			matchingNames, err = pht.ExecutePrefixQuery(ctx, r.phtStore, parsed.Prefix)
		case pht.QuerySubstring:
			// Substring query: use PHT with Bloom filter pruning
			matchingNames, err = pht.ExecuteSubstringQuery(ctx, r.phtStore, parsed.Substring, 0)
		default:
			// Fall through to P2P
			return p2pOp(tpname)
		}

		if err != nil {
			return nil, err
		}

		if len(matchingNames) == 0 {
			return nil, errors.New("no matching tuples found")
		}

		// Try the first matching tuple name that dhtOp succeeds on. In tuple
		// space semantics, Get consumes (and Read returns) the first available match.
		var lastErr error
		for _, name := range matchingNames {
			data, err := dhtOp(name)
			if err == nil {
				return data, nil
			}
			lastErr = err
			// Continue to next match if this one fails
		}

		// No matches found in DHT (all were consumed or missing)
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, errors.New("no matching tuples found")
	}

	// Complex regex or PHT unavailable: use P2P tuple space
	return p2pOp(tpname)
}

// TsGet routes Get operations (consuming).
// Exact match → DHT tuple space.
// Prefix wildcard (pattern*) → PHT to find matches, then DHT to retrieve.
// Substring wildcard (*pattern*) → PHT with Bloom filters, then DHT to retrieve.
// Complex regex → P2P tuple space.
//
// Parameters:
//   - tpname (string): the tuple name or pattern to consume.
//
// Returns:
//   - []byte: the consumed tuple data (first matching name whose DHT TsGet
//     succeeds, when resolved via PHT; otherwise straight from the chosen backend).
//   - error: non-nil if PHT resolution fails, no matching tuple names are
//     found, all resolved names fail to consume from the DHT, or the
//     underlying backend's TsGet fails.
func (r *Router) TsGet(tpname string) ([]byte, error) {
	return r.resolveAndCall(tpname, r.dhtTS.TsGet, r.p2pTS.TsGet)
}

// TsRead routes Read operations (non-consuming).
// Exact match → DHT tuple space.
// Prefix wildcard (pattern*) → PHT to find matches, then DHT to read.
// Substring wildcard (*pattern*) → PHT with Bloom filters, then DHT to read.
// Complex regex → P2P tuple space.
//
// Parameters:
//   - tpname (string): the tuple name or pattern to read.
//
// Returns:
//   - []byte: the tuple data (first matching name whose DHT TsRead
//     succeeds, when resolved via PHT; otherwise straight from the chosen backend).
//   - error: non-nil if PHT resolution fails, no matching tuple names are
//     found, all resolved names fail to read from the DHT, or the
//     underlying backend's TsRead fails.
func (r *Router) TsRead(tpname string) ([]byte, error) {
	return r.resolveAndCall(tpname, r.dhtTS.TsRead, r.p2pTS.TsRead)
}

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
	dhtTS    *DHTTupleSpace
	p2pTS    *P2PTupleSpace
	phtStore pht.ValueStore // DHT-backed ValueStore for PHT queries
}

// Ensure Router implements TupleSpace interface
var _ TupleSpace = (*Router)(nil)

// NewRouter creates a router with DHT and P2P tuple space implementations.
func NewRouter(dhtTS *DHTTupleSpace, p2pTS *P2PTupleSpace, phtStore pht.ValueStore) *Router {
	return &Router{
		dhtTS:    dhtTS,
		p2pTS:    p2pTS,
		phtStore: phtStore,
	}
}

// isExactMatch checks if the pattern contains only simple wildcards (*) or is exact.
// Returns true if pattern is exact (no wildcards) or simple wildcard (prefix/substring).
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
func (r *Router) TsPut(tpname string, tpvalue []byte) (int, error) {
	if isExactMatch(tpname) {
		// Exact match: use DHT tuple space
		return r.dhtTS.TsPut(tpname, tpvalue)
	}
	// Wildcard/regex: use P2P tuple space
	return r.p2pTS.TsPut(tpname, tpvalue)
}

// TsGet routes Get operations (consuming).
// Exact match → DHT tuple space.
// Prefix wildcard (pattern*) → PHT to find matches, then DHT to retrieve.
// Substring wildcard (*pattern*) → PHT with Bloom filters, then DHT to retrieve.
// Complex regex → P2P tuple space.
func (r *Router) TsGet(tpname string) ([]byte, error) {
	if isExactMatch(tpname) {
		// Exact match: use DHT tuple space
		return r.dhtTS.TsGet(tpname)
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
			return r.p2pTS.TsGet(tpname)
		}

		if err != nil {
			return nil, err
		}

		if len(matchingNames) == 0 {
			return nil, errors.New("no matching tuples found")
		}

		// Try to get the first matching tuple from DHT
		// In tuple space semantics, Get consumes the first available match
		var lastErr error
		for _, name := range matchingNames {
			data, err := r.dhtTS.TsGet(name)
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
	return r.p2pTS.TsGet(tpname)
}

// TsRead routes Read operations (non-consuming).
// Exact match → DHT tuple space.
// Prefix wildcard (pattern*) → PHT to find matches, then DHT to read.
// Substring wildcard (*pattern*) → PHT with Bloom filters, then DHT to read.
// Complex regex → P2P tuple space.
func (r *Router) TsRead(tpname string) ([]byte, error) {
	if isExactMatch(tpname) {
		// Exact match: use DHT tuple space
		return r.dhtTS.TsRead(tpname)
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
			return r.p2pTS.TsRead(tpname)
		}

		if err != nil {
			return nil, err
		}

		if len(matchingNames) == 0 {
			return nil, errors.New("no matching tuples found")
		}

		// Try to read the first matching tuple from DHT
		// In tuple space semantics, Read returns the first available match
		var lastErr error
		for _, name := range matchingNames {
			data, err := r.dhtTS.TsRead(name)
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
	return r.p2pTS.TsRead(tpname)
}

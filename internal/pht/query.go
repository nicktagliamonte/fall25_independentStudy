// Purpose: Query parsing and classification for PHT query routing (Phase 3.3).

package pht

import (
	"context"
	"sort"
	"strings"
)

// RankedMatch holds a result key and its relevance score in [0, 1], higher = more relevant.
type RankedMatch struct {
	// Key is the matched entry (e.g. a tuple name or filename).
	Key string
	// Score is the relevance score in [0, 1]; higher means a closer match.
	Score float64
}

// QueryKind identifies the query type for routing decisions.
type QueryKind int

const (
	QueryExact     QueryKind = iota // No wildcards, direct DHT lookup
	QueryPrefix                     // Trailing *, e.g. image_*
	QuerySubstring                  // *pattern*, e.g. *forest*
)

// ParsedQuery holds the result of parsing a query pattern.
type ParsedQuery struct {
	Kind      QueryKind // QueryExact, QueryPrefix, or QuerySubstring
	Prefix    string    // For QueryPrefix: the prefix before *
	Substring string    // For QuerySubstring: the substring between *s
	Pattern   string    // Original pattern
}

// ParseQuery classifies the query pattern and extracts prefix or substring.
// Supported patterns:
//   - Exact: "image_001.jpg" (no asterisk)
//   - Prefix: "image_*" (trailing asterisk only)
//   - Substring: "*forest*" or "*forest" or "forest*" (one or both asterisks)
//
// Parameters:
//   - pattern (string): the raw query pattern, e.g. "image_*" or "*forest*".
//
// Returns:
//   - ParsedQuery: the classified query, with Prefix/Substring populated
//     according to Kind, and Pattern set to the original input.
func ParseQuery(pattern string) ParsedQuery {
	q := ParsedQuery{Pattern: pattern}
	if pattern == "" {
		return q
	}
	leading := pattern[0] == '*'
	trailing := pattern[len(pattern)-1] == '*'
	if leading && trailing && len(pattern) >= 2 {
		q.Kind = QuerySubstring
		q.Substring = pattern[1 : len(pattern)-1]
		return q
	}
	if trailing && !leading && len(pattern) >= 2 {
		q.Kind = QueryPrefix
		q.Prefix = pattern[:len(pattern)-1]
		return q
	}
	if leading && len(pattern) >= 2 {
		q.Kind = QuerySubstring
		q.Substring = pattern[1:]
		return q
	}
	q.Kind = QueryExact
	return q
}

// RankPrefixMatches scores keys by prefix match. Shorter keys (closer to prefix)
// rank higher. Score = len(prefix)/len(key) for keys with that prefix.
//
// Parameters:
//   - keys ([]string): candidate keys to score (only those with the given prefix are kept when prefix is non-empty).
//   - prefix (string): the prefix that was queried.
//
// Returns:
//   - []RankedMatch: matches sorted by descending score. If prefix is empty,
//     all keys are returned with score 1.0.
func RankPrefixMatches(keys []string, prefix string) []RankedMatch {
	var out []RankedMatch
	pl := float64(len(prefix))
	if pl == 0 {
		for _, k := range keys {
			out = append(out, RankedMatch{Key: k, Score: 1.0})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
		return out
	}
	for _, k := range keys {
		if strings.HasPrefix(k, prefix) {
			kl := float64(len(k))
			if kl < pl {
				kl = pl
			}
			out = append(out, RankedMatch{Key: k, Score: pl / kl})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// RankSubstringMatches scores keys by substring containment. Exact match (key ==
// substring) gets 1.0; earlier first occurrence ranks higher.
//
// Parameters:
//   - keys ([]string): candidate keys to score (keys not containing substring are dropped).
//   - substring (string): the substring that was queried.
//
// Returns:
//   - []RankedMatch: matches sorted by descending score, computed as
//     1 - index(substring)/(len(key)+1), clamped to 0, with exact matches scoring 1.0.
func RankSubstringMatches(keys []string, substring string) []RankedMatch {
	var out []RankedMatch
	for _, k := range keys {
		if k == substring {
			out = append(out, RankedMatch{Key: k, Score: 1.0})
			continue
		}
		idx := strings.Index(k, substring)
		if idx < 0 {
			continue
		}
		kl := float64(len(k))
		score := 1.0 - float64(idx)/(kl+1)
		if score < 0 {
			score = 0
		}
		out = append(out, RankedMatch{Key: k, Score: score})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// CombineResults merges result slices from candidate nodes, deduplicating by key
// (first occurrence wins). Used when aggregating from multiple subtree traversals.
//
// Parameters:
//   - rows (...[]string): one or more slices of keys to merge, in order.
//
// Returns:
//   - []string: the deduplicated union of all rows, preserving first-seen order.
func CombineResults(rows ...[]string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, row := range rows {
		for _, k := range row {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	return out
}

// ExecutePrefixQuery runs a prefix query via PHT tree descent. Fetches the node at
// prefix from the DHT and collects all keys in its subtree. Returns nil, nil on
// missing prefix.
//
// Parameters:
//   - ctx (context.Context): cancels/deadlines the underlying DHT fetches.
//   - store (ValueStore): DHT-backed store holding the PHT nodes.
//   - prefix (string): the prefix to descend to.
//
// Returns:
//   - []string: all keys found in the subtree rooted at prefix.
//   - error: non-nil if a DHT fetch failed; nil, nil if the prefix node is missing.
func ExecutePrefixQuery(ctx context.Context, store ValueStore, prefix string) ([]string, error) {
	return PrefixQueryDHT(ctx, store, prefix)
}

// ExecuteSubstringQuery runs a substring query with Bloom filter pre-check. Fetches
// root from the DHT, traverses the tree while pruning branches whose Bloom filter
// does not contain all n-grams of the substring, then filters results to keys that
// actually contain the substring (Bloom can have false positives). nGram 0 uses
// DefaultNGramSize.
//
// Parameters:
//   - ctx (context.Context): cancels/deadlines the underlying DHT fetches.
//   - store (ValueStore): DHT-backed store holding the PHT nodes.
//   - substring (string): the substring to search for.
//   - nGram (int): n-gram length for Bloom filter pruning; if <= 0, DefaultNGramSize is used.
//
// Returns:
//   - []string: keys that actually contain substring; nil, nil if substring is empty.
//   - error: non-nil if fetching the root or traversing the DHT failed.
func ExecuteSubstringQuery(ctx context.Context, store ValueStore, substring string, nGram int) ([]string, error) {
	rows, _, err := ExecuteSubstringQueryWithStats(ctx, store, substring, nGram)
	return rows, err
}

// ExecuteSubstringQueryWithStats runs a Bloom-pruned substring query and
// reports direct PHT traversal work.
func ExecuteSubstringQueryWithStats(ctx context.Context, store ValueStore, substring string, nGram int) ([]string, QueryStats, error) {
	return ExecuteSubstringQueryWithStatsAndPruning(ctx, store, substring, nGram, true)
}

// ExecuteSubstringQueryWithStatsAndPruning exposes a controlled Bloom-filter
// ablation. When pruning is false it traverses the same PHT and applies the
// same authoritative substring filter, but supplies no n-grams to traversal.
func ExecuteSubstringQueryWithStatsAndPruning(ctx context.Context, store ValueStore, substring string, nGram int, pruning bool) ([]string, QueryStats, error) {
	if substring == "" {
		return nil, QueryStats{}, nil
	}
	if nGram <= 0 {
		nGram = DefaultNGramSize
	}
	ngrams := ExtractNGrams(substring, nGram)
	if !pruning {
		ngrams = nil
	}
	counters := &queryCounters{}
	counted := countingValueStore{ValueStore: store, counters: counters}
	root, err := NavigateDHT(ctx, counted, "")
	if err != nil || root == nil {
		return nil, counters.snapshot(), err
	}
	candidates, err := collectUnderDHTInternal(ctx, counted, root, ngrams, counters)
	if err != nil {
		return nil, counters.snapshot(), err
	}
	var out []string
	for _, k := range candidates {
		if strings.Contains(k, substring) {
			out = append(out, k)
		}
	}
	stats := counters.snapshot()
	stats.Matches = len(out)
	return out, stats, nil
}

// Execute runs the query appropriate for q.Kind. For QueryPrefix, performs PHT tree
// descent via ExecutePrefixQuery. For QuerySubstring, uses Bloom filter pre-check
// via ExecuteSubstringQuery. For QueryExact, returns nil (call ExecuteExactQuery when implemented).
//
// Parameters:
//   - ctx (context.Context): cancels/deadlines the underlying DHT fetches.
//   - store (ValueStore): DHT-backed store holding the PHT nodes.
//   - q (ParsedQuery): the classified query to execute.
//
// Returns:
//   - []string: matching keys, unranked (in the order ExecuteRanked's underlying ranking produced them).
//   - error: non-nil if the underlying query execution failed.
func Execute(ctx context.Context, store ValueStore, q ParsedQuery) ([]string, error) {
	ranked, err := ExecuteRanked(ctx, store, q)
	if err != nil {
		return nil, err
	}
	keys := make([]string, len(ranked))
	for i, r := range ranked {
		keys[i] = r.Key
	}
	return keys, nil
}

// ExecuteRanked runs the query and returns matches sorted by relevance (highest first).
//
// Parameters:
//   - ctx (context.Context): cancels/deadlines the underlying DHT fetches.
//   - store (ValueStore): DHT-backed store holding the PHT nodes.
//   - q (ParsedQuery): the classified query to execute.
//
// Returns:
//   - []RankedMatch: matches sorted by descending relevance score; nil, nil for QueryExact.
//   - error: non-nil if the underlying prefix/substring query execution failed.
func ExecuteRanked(ctx context.Context, store ValueStore, q ParsedQuery) ([]RankedMatch, error) {
	switch q.Kind {
	case QueryPrefix:
		keys, err := ExecutePrefixQuery(ctx, store, q.Prefix)
		if err != nil {
			return nil, err
		}
		return RankPrefixMatches(keys, q.Prefix), nil
	case QuerySubstring:
		keys, err := ExecuteSubstringQuery(ctx, store, q.Substring, 0)
		if err != nil {
			return nil, err
		}
		return RankSubstringMatches(keys, q.Substring), nil
	default:
		return nil, nil
	}
}

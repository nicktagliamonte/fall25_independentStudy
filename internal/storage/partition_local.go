// Purpose: Track partition-local operations for later reconciliation (Phase 5.2).
// When partitioned, Puts and local stores are recorded. Phase 5.3 will consume
// these for conflict resolution. No Phase 2 dependencies.

package storage

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ipfs/go-cid"
	ds "github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/query"
)

const partitionLocalNS = "/partition/local/"

// PartitionLocalOp records one operation performed while partitioned.
type PartitionLocalOp struct {
	// Op is the operation name (e.g. "put").
	Op string
	// CID is the content identifier the operation applied to.
	CID cid.Cid
	// TsNano is the operation's timestamp in Unix nanoseconds.
	TsNano int64
}

// RecordPartitionLocalOp appends an operation to the log. Call when partitioned.
// The log entry's datastore key embeds the current timestamp and CID so entries
// sort chronologically by key; the value is a tab-separated "op\tcid\tts" string.
//
// Parameters:
//   - ctx (context.Context): controls cancellation/timeout of the datastore write.
//   - d (ds.Batching): the backing datastore; if nil, this is a silent no-op.
//   - op (string): the operation name; if empty, this is a silent no-op.
//   - c (cid.Cid): the CID the operation applies to; if undefined, this is a silent no-op.
//
// Returns:
//   - error: non-nil if the underlying datastore Put fails.
func RecordPartitionLocalOp(ctx context.Context, d ds.Batching, op string, c cid.Cid) error {
	if d == nil || !c.Defined() || op == "" {
		return nil
	}
	ts := time.Now().UnixNano()
	key := ds.NewKey(partitionLocalNS + strconv.FormatInt(ts, 10) + "_" + c.String())
	val := fmt.Sprintf("%s\t%s\t%d", op, c.String(), ts)
	return d.Put(ctx, key, []byte(val))
}

// partitionLocalOpTimestamp extracts the UnixNano timestamp embedded in a
// partition-local-op datastore key (partitionLocalNS + "<ts>_<cid>"), so keys
// can be sorted numerically by timestamp rather than lexically by string. This
// avoids relying on all timestamps having the same decimal digit count (which
// lexical sort of the raw key would require). Keys that don't match the
// expected prefix/format sort as timestamp 0 (i.e. first).
//
// Parameters:
//   - key (string): a full datastore key as returned by the partitionLocalNS query.
//
// Returns:
//   - int64: the parsed UnixNano timestamp, or 0 if key is malformed.
func partitionLocalOpTimestamp(key string) int64 {
	if len(key) <= len(partitionLocalNS) {
		return 0
	}
	rest := key[len(partitionLocalNS):]
	idx := strings.IndexByte(rest, '_')
	if idx < 0 {
		return 0
	}
	ts, err := strconv.ParseInt(rest[:idx], 10, 64)
	if err != nil {
		return 0
	}
	return ts
}

// ListPartitionLocalOps returns operations from the log for reconciliation.
// Keys under partitionLocalNS are queried, sorted numerically by their embedded
// UnixNano timestamp (via partitionLocalOpTimestamp, not lexically by the raw
// key string, so ordering is correct regardless of timestamp digit count),
// decoded, and returned in ascending timestamp order. This sorts the on-disk
// key strings themselves; it does not change the stored key format. Entries
// that fail to read, fail to parse, or have an undecodable CID are silently
// skipped rather than causing the whole call to fail.
//
// Parameters:
//   - ctx (context.Context): controls cancellation/timeout of the datastore query.
//   - d (ds.Datastore): the backing datastore; if nil, returns (nil, nil).
//   - limit (int): maximum number of operations to return; 0 means no limit.
//
// Returns:
//   - []PartitionLocalOp: the decoded operations in ascending timestamp order,
//     truncated to limit if positive.
//   - error: non-nil if the initial datastore Query call fails.
func ListPartitionLocalOps(ctx context.Context, d ds.Datastore, limit int) ([]PartitionLocalOp, error) {
	if d == nil {
		return nil, nil
	}
	q := query.Query{Prefix: partitionLocalNS}
	res, err := d.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer res.Close()
	var keys []string
	for r := range res.Next() {
		if r.Error != nil {
			continue
		}
		keys = append(keys, r.Key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return partitionLocalOpTimestamp(keys[i]) < partitionLocalOpTimestamp(keys[j])
	})
	var out []PartitionLocalOp
	for _, k := range keys {
		if limit > 0 && len(out) >= limit {
			break
		}
		val, err := d.Get(ctx, ds.NewKey(k))
		if err != nil {
			continue
		}
		var op, cidStr string
		var ts int64
		if _, err := fmt.Sscanf(string(val), "%s\t%s\t%d", &op, &cidStr, &ts); err != nil {
			continue
		}
		c, err := cid.Decode(cidStr)
		if err != nil {
			continue
		}
		out = append(out, PartitionLocalOp{Op: op, CID: c, TsNano: ts})
	}
	return out, nil
}

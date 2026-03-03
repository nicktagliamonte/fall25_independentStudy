// Purpose: Track partition-local operations for later reconciliation (Phase 5.2).
// When partitioned, Puts and local stores are recorded. Phase 5.3 will consume
// these for conflict resolution. No Phase 2 dependencies.

package storage

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/ipfs/go-cid"
	ds "github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/query"
)

const partitionLocalNS = "/partition/local/"

// PartitionLocalOp records one operation performed while partitioned.
type PartitionLocalOp struct {
	Op    string
	CID   cid.Cid
	TsNano int64
}

// RecordPartitionLocalOp appends an operation to the log. Call when partitioned.
func RecordPartitionLocalOp(ctx context.Context, d ds.Batching, op string, c cid.Cid) error {
	if d == nil || !c.Defined() || op == "" {
		return nil
	}
	ts := time.Now().UnixNano()
	key := ds.NewKey(partitionLocalNS + strconv.FormatInt(ts, 10) + "_" + c.String())
	val := fmt.Sprintf("%s\t%s\t%d", op, c.String(), ts)
	return d.Put(ctx, key, []byte(val))
}

// ListPartitionLocalOps returns operations from the log for reconciliation.
// limit 0 means no limit. Results ordered by timestamp ascending.
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
	sort.Strings(keys)
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

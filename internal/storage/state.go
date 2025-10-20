// Purpose: Append-only event log (G-set) with verifiable head (DAG-CBOR blocks).

package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	bserv "github.com/ipfs/boxo/blockservice"
	blocks "github.com/ipfs/go-block-format"
	"github.com/ipfs/go-cid"
	ds "github.com/ipfs/go-datastore"
	"github.com/ipld/go-ipld-prime/codec/dagcbor"
	datamodel "github.com/ipld/go-ipld-prime/datamodel"
	basicnode "github.com/ipld/go-ipld-prime/node/basicnode"
	mh "github.com/multiformats/go-multihash"
)

const (
	stateHeadKey   = "/gset/head"
	stateHeightKey = "/gset/height"
)

// AppendPeerAdded appends a peer_added event, updating head and height.
func AppendPeerAdded(ctx context.Context, d ds.Batching, bsvc *bserv.BlockService, peerID string) (cid.Cid, int64, error) {
	if bsvc == nil {
		return cid.Cid{}, 0, errors.New("nil blockservice")
	}
	prev, height, _ := GetHead(ctx, d)

	// Build DAG-CBOR map for the event {type, ts, peer, prev}
	nb := basicnode.Prototype__Map{}.NewBuilder()
	ma, _ := nb.BeginMap(4)
	ma.AssembleKey().AssignString("type")
	ma.AssembleValue().AssignString("peer_added")
	ma.AssembleKey().AssignString("ts")
	ma.AssembleValue().AssignInt(int64(time.Now().Unix()))
	ma.AssembleKey().AssignString("peer")
	ma.AssembleValue().AssignString(peerID)
	if prev.Defined() {
		ma.AssembleKey().AssignString("prev")
		ma.AssembleValue().AssignString(prev.String())
	}
	ma.Finish()
	n := nb.Build()

	var buf bytes.Buffer
	if err := dagcbor.Encode(n, &buf); err != nil {
		return cid.Cid{}, 0, err
	}
	data := buf.Bytes()

	// Compute CID: dag-cbor + sha2-256
	prefix := cid.Prefix{Version: 1, Codec: cid.DagCBOR, MhType: mh.SHA2_256, MhLength: -1}
	c, err := prefix.Sum(data)
	if err != nil {
		return cid.Cid{}, 0, err
	}
	blk, err := blocks.NewBlockWithCid(data, c)
	if err != nil {
		return cid.Cid{}, 0, err
	}
	if err := (*bsvc).AddBlock(ctx, blk); err != nil {
		return cid.Cid{}, 0, err
	}

	// Persist new head and height
	if err := d.Put(ctx, ds.NewKey(stateHeadKey), []byte(c.String())); err != nil {
		return cid.Cid{}, 0, err
	}
	newHeight := height + 1
	if err := d.Put(ctx, ds.NewKey(stateHeightKey), []byte(fmtInt64(newHeight))); err != nil {
		return cid.Cid{}, 0, err
	}
	return c, newHeight, nil
}

// GetHead returns the current head CID and height, or zero values if none.
func GetHead(ctx context.Context, d ds.Batching) (cid.Cid, int64, error) {
	if d == nil {
		return cid.Cid{}, 0, errors.New("nil datastore")
	}
	b, err := d.Get(ctx, ds.NewKey(stateHeadKey))
	if err != nil && err != ds.ErrNotFound {
		return cid.Cid{}, 0, err
	}
	var head cid.Cid
	if len(b) > 0 {
		if c, err := cid.Decode(string(b)); err == nil {
			head = c
		}
	}
	b2, err := d.Get(ctx, ds.NewKey(stateHeightKey))
	if err != nil && err != ds.ErrNotFound {
		return head, 0, err
	}
	height := parseInt64(string(b2))
	return head, height, nil
}

// ApplyEventsFrom walks backward from head up to limit, verifying prev links.
// Returns the number of events verified/applied.
func ApplyEventsFrom(ctx context.Context, bsvc *bserv.BlockService, start cid.Cid, limit int) (int, error) {
	if !start.Defined() || limit <= 0 {
		return 0, nil
	}
	cur := start
	count := 0
	for cur.Defined() && count < limit {
		blk, err := (*bsvc).GetBlock(ctx, cur)
		if err != nil {
			return count, err
		}
		// decode, read prev
		nb := basicnode.Prototype__Any{}.NewBuilder()
		if err := dagcbor.Decode(nb, bytes.NewReader(blk.RawData())); err != nil {
			return count, err
		}
		n := nb.Build()
		prevStr := getMapString(n, "prev")
		if prevStr == "" {
			break
		}
		pc, err := cid.Decode(prevStr)
		if err != nil {
			return count, err
		}
		cur = pc
		count++
	}
	return count, nil
}

// Helpers: tiny int64 encode/decode and map field extraction.
func fmtInt64(n int64) string { return fmt.Sprintf("%d", n) }

func parseInt64(s string) int64 {
	var out int64
	if s == "" {
		return 0
	}
	_, _ = fmt.Sscanf(s, "%d", &out)
	return out
}

func getMapString(n datamodel.Node, key string) string {
	if n.Kind() != datamodel.Kind_Map {
		return ""
	}
	it := n.MapIterator()
	for !it.Done() {
		k, v, _ := it.Next()
		if ks, _ := k.AsString(); ks == key {
			if vs, err := v.AsString(); err == nil {
				return vs
			}
		}
	}
	return ""
}

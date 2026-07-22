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
)

const (
	// stateHeadKey is the datastore key storing the current chain head CID (as a string).
	stateHeadKey = "/gset/head"
	// stateHeightKey is the datastore key storing the current chain height (as a decimal string).
	stateHeightKey = "/gset/height"
)

// AppendPeerAdded appends a new peer_added event to the local G-set event log: it reads the
// current head/height via GetHead, builds a PeerAddedGo event linking to the previous head (if
// any) with the current Unix timestamp, encodes it to DAG-CBOR (encodePeerAddedToCBOR), stores
// it as a block via bsvc.AddBlock (retrying once on failure), and then persists the new head
// CID and incremented height to the datastore.
//
// Parameters:
//   - ctx (context.Context): cancels the datastore/blockservice operations.
//   - d (ds.Batching): the datastore used to read/write head and height.
//   - bsvc (*bserv.BlockService): the block service used to store the encoded event block.
//   - peerID (string): the identifier of the peer being recorded as added.
//
// Returns:
//   - cid.Cid: the CID of the newly appended event block.
//   - int64: the new chain height after the append.
//   - error: non-nil if bsvc is nil, encoding fails, both block-store attempts fail, or
//     persisting the new head/height fails.
func AppendPeerAdded(ctx context.Context, d ds.Batching, bsvc *bserv.BlockService, peerID string) (cid.Cid, int64, error) {
	if bsvc == nil {
		return cid.Cid{}, 0, errors.New("nil blockservice")
	}
	prev, height, _ := GetHead(ctx, d)

	// Build typed event
	var prevStrPtr *string
	if prev.Defined() {
		s := prev.String()
		prevStrPtr = &s
	}
	pa := &PeerAddedGo{Type: "peer_added", Ts: int64(time.Now().Unix()), Peer: peerID, Prev: prevStrPtr}
	raw, c, err := encodePeerAddedToCBOR(pa)
	if err != nil {
		return cid.Cid{}, 0, err
	}

	blk, err := blocks.NewBlockWithCid(raw, c)
	if err != nil {
		return cid.Cid{}, 0, err
	}
	err = (*bsvc).AddBlock(ctx, blk)
	if err != nil {
		err = (*bsvc).AddBlock(ctx, blk)
	}
	if err != nil {
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

// GetHead reads the current chain head CID and height from the datastore (stateHeadKey and
// stateHeightKey). Missing values (ds.ErrNotFound) are treated as zero values rather than
// errors; a malformed stored head CID is silently ignored and reported as the zero CID.
//
// Parameters:
//   - ctx (context.Context): cancels the datastore reads.
//   - d (ds.Batching): the datastore to read from; nil returns an error.
//
// Returns:
//   - cid.Cid: the current head CID, or the zero CID if none is set or it fails to decode.
//   - int64: the current height, or 0 if none is set.
//   - error: non-nil if d is nil or an unexpected (non-not-found) datastore error occurs.
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

// SetHead stores head and height as the current local chain state (stateHeadKey and
// stateHeightKey). If head is undefined, the stored head key is deleted instead (clearing it),
// tolerating an already-absent key; height is always written regardless.
//
// Parameters:
//   - ctx (context.Context): cancels the datastore writes/delete.
//   - d (ds.Batching): the datastore to write to; nil returns an error.
//   - head (cid.Cid): the head CID to store, or the zero CID to clear it.
//   - height (int64): the chain height to store.
//
// Returns:
//   - error: non-nil if d is nil or any underlying datastore operation fails.
func SetHead(ctx context.Context, d ds.Batching, head cid.Cid, height int64) error {
	if d == nil {
		return errors.New("nil datastore")
	}
	if head.Defined() {
		if err := d.Put(ctx, ds.NewKey(stateHeadKey), []byte(head.String())); err != nil {
			return err
		}
	} else {
		// Clear head
		if err := d.Delete(ctx, ds.NewKey(stateHeadKey)); err != nil && err != ds.ErrNotFound {
			return err
		}
	}
	return d.Put(ctx, ds.NewKey(stateHeightKey), []byte(fmtInt64(height)))
}

// ApplyEventsFrom walks the event chain backward from start, following each event's "prev"
// field (decoded generically via dagcbor/basicnode rather than the typed PeerAddedGo struct),
// for up to limit steps or until an event with no "prev" field is reached. It does not persist
// or otherwise apply state beyond counting; the walk itself serves as validation that the
// prev-link chain is well-formed and fetchable.
//
// Parameters:
//   - ctx (context.Context): cancels each block fetch.
//   - bsvc (*bserv.BlockService): the block service used to fetch each event block.
//   - start (cid.Cid): the CID to start walking backward from; a no-op if undefined.
//   - limit (int): the maximum number of events to walk; a no-op if <= 0.
//
// Returns:
//   - int: the number of events successfully walked/verified.
//   - error: non-nil if fetching or decoding any block in the chain fails.
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

// fmtInt64 formats n as a decimal string, for storing height values as datastore byte values.
//
// Parameters:
//   - n (int64): the value to format.
//
// Returns:
//   - string: the decimal string representation of n.
func fmtInt64(n int64) string { return fmt.Sprintf("%d", n) }

// parseInt64 parses s as a decimal int64, the inverse of fmtInt64. An empty string or a
// malformed value both yield 0 (Sscanf's error is ignored, so malformed input silently
// produces the zero value rather than an error).
//
// Parameters:
//   - s (string): the decimal string to parse.
//
// Returns:
//   - int64: the parsed value, or 0 if s is empty or fails to parse.
func parseInt64(s string) int64 {
	var out int64
	if s == "" {
		return 0
	}
	_, _ = fmt.Sscanf(s, "%d", &out)
	return out
}

// getMapString extracts the string value of key from an IPLD map node n, used to read fields
// (like "prev") out of a generically-decoded DAG-CBOR event without needing the typed
// PeerAddedGo struct. Returns "" if n is not a map, key is absent, or its value is not a string.
//
// Parameters:
//   - n (datamodel.Node): the IPLD node to read from; expected to be a map.
//   - key (string): the map key to look up.
//
// Returns:
//   - string: the string value at key, or "" if not found or not a string.
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

// SyncOptions constrains a SyncSuffix attempt, bounding how far back and how much data it is
// willing to walk/fetch when reconciling a remote chain with the local one.
type SyncOptions struct {
	// MaxDepth is the maximum number of blocks to walk backward from remoteHead; <= 0 defaults to 512.
	MaxDepth int
	// MaxBlockBytes is the maximum size allowed for any single fetched event block; <= 0 means no limit.
	MaxBlockBytes int64
	// Timeout bounds the wall-clock time spent walking the remote chain; <= 0 means no deadline.
	Timeout time.Duration
}

// SyncSuffix reconciles the local event chain with a remote chain by validating and applying
// the suffix of events from remoteHead back down to the local head (the common ancestor). If
// remoteHeight is not greater than the local height, it does nothing and reports the local
// head/height unchanged. Otherwise it walks backward from remoteHead (bounded by
// opts.MaxDepth, opts.MaxBlockBytes per block, and opts.Timeout), decoding each block as a
// typed peer_added event and following its "prev" link, stopping once it reaches the current
// local head (or, if there is no local head yet, accepting the walked chain as a valid new
// history). If no common ancestor is found within the configured limits, it returns an error
// without applying anything. On success, it advances the local head/height for each entry in
// the validated suffix (oldest to newest) via SetHead.
//
// Parameters:
//   - ctx (context.Context): cancels block fetches during the walk.
//   - d (ds.Batching): the datastore used to read the local head and persist the new head/height.
//   - bsvc (*bserv.BlockService): the block service used to fetch remote chain blocks.
//   - remoteHead (cid.Cid): the head CID of the remote chain to sync from; must be defined.
//   - remoteHeight (int64): the remote chain's height, compared against the local height to
//     decide whether syncing is needed.
//   - opts (SyncOptions): limits on walk depth, per-block size, and wall-clock time.
//
// Returns:
//   - int: the number of new entries applied to the local chain.
//   - cid.Cid: the resulting local head CID after applying the suffix (unchanged on no-op or error).
//   - int64: the resulting local height after applying the suffix.
//   - error: non-nil if bsvc/d is nil, remoteHead is undefined, a remote block exceeds
//     MaxBlockBytes, a block fails to fetch/decode or contains an invalid event, or no common
//     ancestor is found within the configured limits.
func SyncSuffix(ctx context.Context, d ds.Batching, bsvc *bserv.BlockService, remoteHead cid.Cid, remoteHeight int64, opts SyncOptions) (int, cid.Cid, int64, error) {
	if bsvc == nil {
		return 0, cid.Cid{}, 0, errors.New("nil blockservice")
	}
	if d == nil {
		return 0, cid.Cid{}, 0, errors.New("nil datastore")
	}
	if !remoteHead.Defined() {
		return 0, cid.Cid{}, 0, errors.New("undefined remote head")
	}
	localHead, localHeight, _ := GetHead(ctx, d)
	if remoteHeight <= localHeight {
		return 0, localHead, localHeight, nil
	}

	// Budgeted walk from remote head backward until local head is found or limits hit.
	deadline := time.Time{}
	if opts.Timeout > 0 {
		deadline = time.Now().Add(opts.Timeout)
	}
	maxDepth := opts.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 512
	}
	cur := remoteHead
	type step struct {
		cid  cid.Cid
		peer string
		prev cid.Cid
		size int
	}
	var chain []step
	foundAncestor := !localHead.Defined() // if no local head, accept any chain head

	for depth := 0; depth < maxDepth && cur.Defined(); depth++ {
		if !deadline.IsZero() && time.Now().After(deadline) {
			break
		}
		blk, err := (*bsvc).GetBlock(ctx, cur)
		if err != nil {
			return 0, localHead, localHeight, err
		}
		raw := blk.RawData()
		if opts.MaxBlockBytes > 0 && int64(len(raw)) > opts.MaxBlockBytes {
			return 0, localHead, localHeight, errors.New("remote block exceeds size limit")
		}
		// Decode typed event
		obj, err := decodePeerAddedFromCBOR(raw)
		if err != nil {
			return 0, localHead, localHeight, err
		}
		if obj == nil || obj.Type != "peer_added" || obj.Peer == "" {
			return 0, localHead, localHeight, errors.New("invalid event in remote chain")
		}
		var prev cid.Cid
		if obj.Prev != nil && *obj.Prev != "" {
			pc, err := cid.Decode(*obj.Prev)
			if err != nil {
				return 0, localHead, localHeight, err
			}
			prev = pc
		}
		// Stop if we reached our local head; do not include it in the suffix to apply.
		if cur.Defined() && localHead.Defined() && cur.Equals(localHead) {
			foundAncestor = true
			break
		}
		chain = append(chain, step{cid: cur, peer: obj.Peer, prev: prev, size: len(raw)})
		if !prev.Defined() {
			break
		}
		cur = prev
	}

	if !foundAncestor {
		return 0, localHead, localHeight, errors.New("no common ancestor within sync limits")
	}

	// Apply suffix from oldest to newest by advancing head/height monotonically.
	applied := 0
	for i := len(chain) - 1; i >= 0; i-- {
		st := chain[i]
		// Advance head and height; events are embodied in the chain; no need to rewrite blocks.
		localHeight++
		if err := SetHead(ctx, d, st.cid, localHeight); err != nil {
			return applied, localHead, localHeight, err
		}
		localHead = st.cid
		applied++
	}
	return applied, localHead, localHeight, nil
}

// AppendPeerAddedIfNew scans the local chain backward from the current head to check whether
// peerID has already been recorded via a peer_added event. If found, it returns the current
// head/height unchanged with appended=false. If not found (including if the scan is cut short
// by a fetch/decode error partway through, which is treated the same as "not found" and simply
// stops the scan), it appends a new peer_added event via AppendPeerAdded and returns
// appended=true.
//
// Parameters:
//   - ctx (context.Context): cancels the scan and the append operation.
//   - d (ds.Batching): the datastore holding head/height state.
//   - bsvc (*bserv.BlockService): the block service used to fetch and store event blocks.
//   - peerID (string): the peer identifier to check for and possibly append.
//
// Returns:
//   - cid.Cid: the resulting head CID (unchanged if already present, new event CID otherwise).
//   - int64: the resulting height.
//   - bool: true if a new event was appended, false if peerID was already present.
//   - error: non-nil if d or bsvc is nil, GetHead fails, or the append fails.
func AppendPeerAddedIfNew(ctx context.Context, d ds.Batching, bsvc *bserv.BlockService, peerID string) (cid.Cid, int64, bool, error) {
	if d == nil {
		return cid.Cid{}, 0, false, errors.New("nil datastore")
	}
	if bsvc == nil {
		return cid.Cid{}, 0, false, errors.New("nil blockservice")
	}
	head, height, err := GetHead(ctx, d)
	if err != nil {
		return cid.Cid{}, 0, false, err
	}
	// Walk backward to check for existing entry for peerID.
	cur := head
	for cur.Defined() {
		blk, err := (*bsvc).GetBlock(ctx, cur)
		if err != nil {
			break
		}
		obj, err := decodePeerAddedFromCBOR(blk.RawData())
		if err != nil {
			break
		}
		if obj != nil && obj.Type == "peer_added" && obj.Peer == peerID {
			return head, height, false, nil
		}
		if obj == nil || obj.Prev == nil || *obj.Prev == "" {
			break
		}
		if pc, err := cid.Decode(*obj.Prev); err == nil {
			cur = pc
		} else {
			break
		}
	}
	c, newHeight, err := AppendPeerAdded(ctx, d, bsvc, peerID)
	if err != nil {
		return cid.Cid{}, 0, false, err
	}
	return c, newHeight, true, nil
}

// PeerAddedEntry pairs a decoded peer_added event with the CID of the block it was decoded from.
type PeerAddedEntry struct {
	// CID is the content identifier of the event block.
	CID cid.Cid
	// Event is the decoded peer_added event payload.
	Event *PeerAddedGo
}

// ListRecentFromHead returns up to limit of the most recent peer_added entries, walking
// backward from the current head via GetHead and following each event's "prev" link. The walk
// stops early (without error) if the head is undefined, an event fails to fetch or decode, an
// event's type is not "peer_added", or a "prev" link is absent/undecodable — in all such cases
// the entries collected so far are returned.
//
// Parameters:
//   - ctx (context.Context): cancels the head lookup and block fetches.
//   - d (ds.Batching): the datastore holding head/height state.
//   - bsvc (*bserv.BlockService): the block service used to fetch event blocks.
//   - limit (int): the maximum number of entries to return; <= 0 returns (nil, nil).
//
// Returns:
//   - []PeerAddedEntry: up to limit entries, most recent first.
//   - error: non-nil if d or bsvc is nil, or GetHead fails.
func ListRecentFromHead(ctx context.Context, d ds.Batching, bsvc *bserv.BlockService, limit int) ([]PeerAddedEntry, error) {
	if limit <= 0 {
		return nil, nil
	}
	if d == nil || bsvc == nil {
		return nil, errors.New("nil datastore or blockservice")
	}
	head, _, err := GetHead(ctx, d)
	if err != nil {
		return nil, err
	}
	if !head.Defined() {
		return nil, nil
	}
	out := make([]PeerAddedEntry, 0, limit)
	cur := head
	for cur.Defined() && len(out) < limit {
		blk, err := (*bsvc).GetBlock(ctx, cur)
		if err != nil {
			break
		}
		obj, err := decodePeerAddedFromCBOR(blk.RawData())
		if err != nil {
			break
		}
		if obj == nil || obj.Type != "peer_added" {
			// stop on unexpected data
			break
		}
		out = append(out, PeerAddedEntry{CID: cur, Event: obj})
		if obj.Prev == nil || *obj.Prev == "" {
			break
		}
		if pc, err := cid.Decode(*obj.Prev); err == nil {
			cur = pc
		} else {
			break
		}
	}
	return out, nil
}

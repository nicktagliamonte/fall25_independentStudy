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

// stateHeadKey and stateHeightKey are the fixed datastore keys under which
// the local node's chain state is persisted: stateHeadKey holds the string
// form of the current head CID, and stateHeightKey holds the decimal string
// of the current chain height (number of events applied). Together they
// form the "current local state" referenced throughout this file — this is
// distinct from the manifest/CID index in store.go, which tracks which
// content blocks are stored locally rather than event-log position.
const (
	stateHeadKey   = "/gset/head"
	stateHeightKey = "/gset/height"
)

// AppendPeerAdded builds a new "peer_added" event referencing the current
// head as its predecessor, writes the event as a DAG-CBOR block, and
// advances the local head/height state to point at it. This grows the
// append-only event log (a grow-only set / G-set of peer_added events
// linked via "prev" pointers into a hash-linked chain, verifiable like a
// simple blockchain).
//
// It is NOT idempotent: calling it twice with the same peerID appends two
// distinct events (each has a different Ts/prev and thus a different CID),
// unlike the plain content-addressed block storage in store.go. Callers
// that want "only add this peer once" semantics should use
// AppendPeerAddedIfNew instead.
//
// Parameters:
//   - ctx: context for the underlying datastore/blockservice operations.
//   - d: the batching key/value datastore used to read/write the head and
//     height keys. Must be non-nil (a nil d will panic in Put, since only
//     bsvc is nil-checked here).
//   - bsvc: pointer to the BlockService used to store the encoded event
//     block. Must be non-nil.
//   - peerID: the identifier of the peer being recorded as added.
//
// Returns:
//   - cid.Cid: the CID of the newly appended event block. Zero value
//     (cid.Cid{}) on error.
//   - int64: the new chain height (previous height + 1). 0 on error.
//   - error: non-nil if bsvc is nil, if encoding the event to DAG-CBOR
//     fails, if adding the block to the blockservice fails (after one
//     internal retry — see below), or if persisting the new head/height to
//     the datastore fails.
//
// Concurrency/reliability note: the call to bsvc.AddBlock is retried
// exactly once on error (line ~50-52) with the same block and no delay or
// backoff, and the error from the retry is what's ultimately checked. This
// is a blind retry — it does not distinguish transient from permanent
// failures — and the same pattern is duplicated in store.go's
// PutRawBlock; consider factoring out a shared retry helper.
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

// GetHead reads the current local chain head CID and height from the
// datastore keys stateHeadKey/stateHeightKey.
//
// Parameters:
//   - ctx: context for the datastore read.
//   - d: the batching datastore to read from.
//
// Returns:
//   - cid.Cid: the decoded head CID, or the zero value (cid.Cid{}, for
//     which Defined() is false) if no head has ever been set, or if the
//     stored bytes fail to decode as a CID (decode errors are silently
//     swallowed here — head simply stays undefined rather than the error
//     being surfaced, which is a possible source of confusion).
//   - int64: the height parsed from stateHeightKey, or 0 if unset or
//     unparsable (parseInt64 returns 0 on any Sscanf failure).
//   - error: non-nil only if d is nil, or if the underlying Get calls
//     return an error other than ds.ErrNotFound (ds.ErrNotFound is treated
//     as "no value yet" and does not produce an error). Note that on a
//     height-read error the already-resolved head is still returned
//     alongside the error rather than being discarded.
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

// SetHead overwrites the current local chain state with the given head CID
// and height. This is a low-level setter used internally by AppendPeerAdded
// and SyncSuffix to advance state; callers driving normal event appends
// should generally prefer AppendPeerAdded/AppendPeerAddedIfNew instead of
// calling this directly, since SetHead does not itself write or validate
// any event block — it only updates the pointer.
//
// Parameters:
//   - ctx: context for the datastore writes.
//   - d: the batching datastore to write to.
//   - head: the CID to record as the new head. If head.Defined() is false
//     (the zero value), the stored head key is deleted instead (clearing
//     the head), rather than writing an empty/invalid CID string.
//   - height: the height value to record alongside head, written
//     unconditionally as a decimal string regardless of whether head was
//     defined or cleared.
//
// Returns:
//   - error: non-nil if d is nil, if deleting the head key fails with
//     anything other than ds.ErrNotFound, or if either Put call fails.
//     Note there is no atomicity between the head write/delete and the
//     height write — if the process crashes or the height Put fails after
//     the head operation succeeds, head and height can become inconsistent
//     (e.g. a defined head with the old height, or a cleared head with a
//     stale nonzero height persisted). d is a ds.Batching, which supports
//     batched writes, but this function does not use a Batch to make the
//     pair atomic.
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

// ApplyEventsFrom walks the event chain backward starting at start,
// following each block's "prev" field, for up to limit hops. It is a
// read-only traversal/verification helper: despite the name "Apply", it
// does not mutate any datastore state (no head/height is written) and does
// not decode into PeerAddedGo — it uses a generic dagcbor decode and only
// pulls out the "prev" string field via getMapString, so it does not
// validate event "type"/"peer" contents the way SyncSuffix and
// AppendPeerAddedIfNew do.
//
// Parameters:
//   - ctx: context for blockservice reads.
//   - bsvc: pointer to the BlockService to fetch blocks from.
//   - start: the CID to begin walking backward from.
//   - limit: maximum number of hops to walk. If limit <= 0, the function
//     returns immediately with (0, nil) and does no work.
//
// Returns:
//   - int: the number of events successfully walked/verified before
//     stopping (either because limit was reached, the chain ended — a
//     block with no "prev" field — or an error occurred). On error this is
//     the count of hops completed strictly before the failing block.
//   - error: non-nil if start is defined but fetching or decoding any
//     block along the walk fails (e.g. missing block, malformed DAG-CBOR,
//     or an unparsable prev CID string). Returns (0, nil), not an error,
//     if start is undefined or limit <= 0.
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

// fmtInt64 formats n as a plain decimal string (no separators/sign
// handling beyond what fmt's "%d" verb does) for storage as a datastore
// value under stateHeightKey. Paired with parseInt64 for the reverse
// conversion.
func fmtInt64(n int64) string { return fmt.Sprintf("%d", n) }

// parseInt64 parses s (expected to be a decimal integer string previously
// produced by fmtInt64) back into an int64. Returns 0 for an empty string
// or if fmt.Sscanf fails to parse s (the Sscanf error is deliberately
// ignored, so a malformed stored value silently reads back as height 0
// rather than surfacing an error to the caller).
func parseInt64(s string) int64 {
	var out int64
	if s == "" {
		return 0
	}
	_, _ = fmt.Sscanf(s, "%d", &out)
	return out
}

// getMapString looks up key in the IPLD map node n and returns its string
// value. Used to pull the "prev" field out of a generically-decoded
// DAG-CBOR event node during the read-only walk in ApplyEventsFrom.
//
// Returns the empty string if n is not a map kind, if key is not present,
// or if the value at key is not string-typed (AsString fails) — all of
// these are treated identically as "no value", so callers cannot
// distinguish "field absent" from "field present but wrong type" from this
// return value alone.
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

// SyncOptions bounds the work SyncSuffix is willing to do when walking a
// remote chain backward looking for a common ancestor with the local head.
// These limits exist because the remote chain is untrusted input (see
// TestSyncSuffix_LyingHead): without them, a malicious or buggy peer could
// force an unbounded backward walk or serve arbitrarily large blocks.
type SyncOptions struct {
	// MaxDepth caps how many blocks are walked backward from remoteHead
	// before giving up. If <= 0, SyncSuffix defaults it to 512.
	MaxDepth int
	// MaxBlockBytes caps the raw size (in bytes) of any single remote
	// block. If a fetched block exceeds this, the sync fails immediately.
	// If <= 0, no size limit is enforced.
	MaxBlockBytes int64
	// Timeout is a soft wall-clock budget for the backward walk: once
	// exceeded, the walk stops accepting further blocks (checked once per
	// loop iteration, not preemptively during an in-flight GetBlock call),
	// which will typically then surface as "no common ancestor within sync
	// limits" if the ancestor had not yet been found. If <= 0, no timeout
	// is applied.
	Timeout time.Duration
}

// SyncSuffix attempts to catch the local chain up to a peer-advertised
// remoteHead by walking the remote chain backward (via each event's "prev"
// link) until it finds the local head (a common ancestor) or exhausts the
// budgets in opts, then replays that suffix onto local state oldest-first.
//
// Fast path: if remoteHeight <= localHeight, the remote is not ahead, so no
// work is done and the current local head/height are returned unchanged.
//
// Otherwise it fetches and DAG-CBOR-decodes each block from remoteHead
// backward, type-checking each as a PeerAddedGo with Type=="peer_added" and
// a non-empty Peer, enforcing opts.MaxBlockBytes and opts.MaxDepth, and
// stopping (without including it in the suffix to apply) as soon as it
// reaches a block equal to the current local head. If the local head is
// itself undefined (fresh node with no state), any reachable chain is
// accepted as rooted (foundAncestor starts true). If the walk exhausts
// MaxDepth/Timeout without finding the local head, it fails closed.
//
// Once a valid suffix is collected, it is applied from oldest to newest:
// for each step, the local height is incremented and SetHead is called to
// advance head/height to that step's CID. Existing event blocks are not
// re-fetched or re-written during this phase — they were already fetched
// during the backward walk and are assumed to already be content-addressed
// and available via bsvc, so only the head/height pointer is advanced.
//
// Parameters:
//   - ctx: context for blockservice/datastore operations.
//   - d: local batching datastore holding head/height state.
//   - bsvc: pointer to the BlockService used to fetch remote blocks (via
//     Bitswap or whatever exchange it's wired to).
//   - remoteHead: the CID the remote peer claims is its current head.
//   - remoteHeight: the height the remote peer claims corresponds to
//     remoteHead.
//   - opts: SyncOptions bounding the walk (see above).
//
// Returns:
//   - int: number of events actually applied (advanced onto local state).
//     0 if the remote wasn't ahead, or on any error.
//   - cid.Cid: the resulting local head after the operation (unchanged
//     original local head if nothing was applied or an error occurred
//     before any SetHead call; may be partially advanced if a SetHead call
//     fails partway through applying the suffix).
//   - int64: the resulting local height, with the same partial-progress
//     caveat as the head return value.
//   - error: non-nil if bsvc or d is nil, remoteHead is undefined, a block
//     fetch/decode fails, a fetched block isn't a well-formed peer_added
//     event, a block exceeds MaxBlockBytes, no common ancestor is found
//     within MaxDepth/Timeout, or a SetHead call fails while applying the
//     suffix.
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

// AppendPeerAddedIfNew provides "add once" semantics on top of the
// otherwise-unconditional AppendPeerAdded: it walks the local chain
// backward from the current head looking for an existing peer_added event
// for peerID, and only appends a new event if none is found. This is what
// gives peer-added membership its set-like (G-set) idempotence — appending
// the same peerID repeatedly does not grow the chain after the first time.
//
// Note the backward scan is O(chain length) in the worst case (peerID not
// present, or present near the root) since it walks the full chain via
// GetBlock + decodePeerAddedFromCBOR until it either finds a match or runs
// out of "prev" links; there is no index by peerID.
//
// Parameters:
//   - ctx: context for datastore/blockservice operations.
//   - d: local batching datastore holding head/height state. Must be
//     non-nil.
//   - bsvc: pointer to the BlockService used to read/write event blocks.
//     Must be non-nil.
//   - peerID: the peer identifier to check for / append.
//
// Returns:
//   - cid.Cid: if peerID was already present, the current head CID
//     (unchanged); if newly appended, the CID of the new event.
//   - int64: the corresponding height (unchanged current height, or the
//     new height after appending).
//   - bool: true if a new event was appended (peerID was not already
//     present), false if peerID was already found in the chain (or if
//     GetHead failed — see error case).
//   - error: non-nil if d or bsvc is nil, if GetHead fails, or if the
//     underlying AppendPeerAdded call fails. Errors encountered while
//     walking the chain to search for peerID (failed GetBlock or decode)
//     are swallowed via `break`, not returned — a malformed/missing
//     intermediate block silently truncates the search rather than
//     failing it, which could cause a duplicate append if the sought
//     event lies beyond the broken link.
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

// PeerAddedEntry pairs a decoded peer_added event with the CID of the block
// it was decoded from, as returned by ListRecentFromHead.
type PeerAddedEntry struct {
	// CID is the content-address of the event block itself.
	CID cid.Cid
	// Event is the decoded event payload (type/timestamp/peer/prev).
	Event *PeerAddedGo
}

// ListRecentFromHead walks the local event chain backward from the current
// head and collects up to limit peer_added entries, most recent first.
//
// Parameters:
//   - ctx: context for datastore/blockservice operations.
//   - d: local batching datastore used to resolve the current head via
//     GetHead.
//   - bsvc: pointer to the BlockService used to fetch event blocks.
//   - limit: maximum number of entries to return. If limit <= 0, returns
//     (nil, nil) immediately without touching d or bsvc.
//
// Returns:
//   - []PeerAddedEntry: up to limit entries in newest-to-oldest order. Nil
//     (not an empty non-nil slice) if limit <= 0, if d or bsvc is nil
//     (see error case), or if there is no head yet. The walk stops early
//     (without error) if it encounters a block that fails to fetch,
//     fails to decode, or does not look like a peer_added event (type
//     mismatch) — such a block is treated as the end of the usable chain
//     rather than a hard failure.
//   - error: non-nil only if d or bsvc is nil, or if GetHead itself
//     returns an error (e.g. underlying datastore Get failure other than
//     not-found).
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

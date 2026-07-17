package storage

// Purpose: Strongly-typed IPLD bindings for storage events using bindnode.

import (
    "bytes"

    "github.com/ipfs/go-cid"
    "github.com/ipld/go-ipld-prime/codec/dagcbor"
    "github.com/ipld/go-ipld-prime/datamodel"
    basicnode "github.com/ipld/go-ipld-prime/node/basicnode"
    mh "github.com/multiformats/go-multihash"
)

// PeerAddedGo is the Go-native mirror of a "peer_added" event, the single
// event type used by the append-only chain in state.go. Despite the
// package comment's mention of "bindnode", this struct is NOT actually fed
// through go-ipld-prime's bindnode package anywhere in this file (no
// bindnode.Prototype/Wrap calls appear) — the ipld struct tags are present
// but unused, and encodePeerAddedToCBOR/decodePeerAddedFromCBOR instead
// hand-roll the DAG-CBOR map encoding/decoding via basicnode directly, as
// the second comment below ("Encoding/decoding uses basicnode...")
// correctly describes. This is a stale/misleading doc comment worth fixing
// or removing the unused struct tags.
//
// Fields:
//   - Type: the event type discriminator; always the literal string
//     "peer_added" for events produced by AppendPeerAdded. Encoded/decoded
//     unconditionally (always present in the CBOR map).
//   - Ts: Unix timestamp (seconds) recorded at event-creation time via
//     time.Now().Unix() in AppendPeerAdded. Always present.
//   - Peer: the identifier of the peer this event records as added.
//     Always present.
//   - Prev *string: the string form of the CID of the previous event in
//     the chain (this event's predecessor/parent), or nil for the first
//     event in a chain (no predecessor). Encoded as "omitempty" — when nil
//     or pointing at an empty string, the "prev" key is omitted from the
//     CBOR map entirely by encodePeerAddedToCBOR, rather than being
//     encoded as null/empty.
type PeerAddedGo struct {
	Type string  `ipld:"type"`
	Ts   int64   `ipld:"ts"`
	Peer string  `ipld:"peer"`
	Prev *string `ipld:"prev,omitempty"`
}

// Encoding/decoding uses basicnode to avoid schema inference at init time.

// computeCBORCID derives the content-address (CID) for an already-encoded
// DAG-CBOR byte string raw, using CIDv1, the dag-cbor codec, and a SHA2-256
// multihash (mh.SHA2_256) with default length. This mirrors how block CIDs
// are computed elsewhere for event blocks so that identical event bytes
// always produce the identical CID (content addressing).
//
// Parameters:
//   - raw: the DAG-CBOR-encoded bytes to hash.
//
// Returns:
//   - cid.Cid: the computed CID.
//   - error: non-nil if the multihash sum computation fails (e.g.
//     unsupported hash function/length — not expected to fail in practice
//     for SHA2_256 with default length).
func computeCBORCID(raw []byte) (cid.Cid, error) {
	prefix := cid.Prefix{Version: 1, Codec: cid.DagCBOR, MhType: mh.SHA2_256, MhLength: -1}
	return prefix.Sum(raw)
}

// encodePeerAddedToCBOR serializes pa into a DAG-CBOR map with keys
// "type", "ts", "peer", and (if pa.Prev is non-nil and non-empty) "prev",
// then computes and returns its content-address via computeCBORCID. Field
// order in the builder (type, ts, peer, prev) determines the map's key
// order in the resulting encoding, which — since it feeds into a
// content-address — must stay consistent for the same logical event to
// always hash to the same CID.
//
// Parameters:
//   - pa: the event to encode. Must be non-nil (a nil pa will panic on
//     field access; there is no nil check).
//
// Returns:
//   - []byte: the raw DAG-CBOR-encoded bytes.
//   - cid.Cid: the CID computed from those bytes (zero value on error).
//   - error: non-nil if constructing the IPLD map (AssembleEntry/
//     AssignString/AssignInt/Finish), the DAG-CBOR encode step, or the CID
//     computation fails.
func encodePeerAddedToCBOR(pa *PeerAddedGo) ([]byte, cid.Cid, error) {
    // Build a DAG-CBOR map with fields: type, ts, peer, prev (optional)
    nb := basicnode.Prototype__Map{}.NewBuilder()
    // 4 is the max entries; prev is optional
    ma, _ := nb.BeginMap(4)
    if ent, err := ma.AssembleEntry("type"); err != nil {
        return nil, cid.Cid{}, err
    } else if err := ent.AssignString(pa.Type); err != nil {
        return nil, cid.Cid{}, err
    }
    if ent, err := ma.AssembleEntry("ts"); err != nil {
        return nil, cid.Cid{}, err
    } else if err := ent.AssignInt(pa.Ts); err != nil {
        return nil, cid.Cid{}, err
    }
    if ent, err := ma.AssembleEntry("peer"); err != nil {
        return nil, cid.Cid{}, err
    } else if err := ent.AssignString(pa.Peer); err != nil {
        return nil, cid.Cid{}, err
    }
    if pa.Prev != nil && *pa.Prev != "" {
        if ent, err := ma.AssembleEntry("prev"); err != nil {
            return nil, cid.Cid{}, err
        } else if err := ent.AssignString(*pa.Prev); err != nil {
            return nil, cid.Cid{}, err
        }
    }
    if err := ma.Finish(); err != nil {
        return nil, cid.Cid{}, err
    }
    n := nb.Build()
    var buf bytes.Buffer
    if err := dagcbor.Encode(n, &buf); err != nil {
        return nil, cid.Cid{}, err
    }
    raw := buf.Bytes()
    c, err := computeCBORCID(raw)
    if err != nil {
        return nil, cid.Cid{}, err
    }
    return raw, c, nil
}

// decodePeerAddedFromCBOR parses raw DAG-CBOR bytes generically (via
// basicnode.Prototype__Any, i.e. without a predefined schema) and extracts
// the "type", "ts", "peer", and "prev" fields into a PeerAddedGo. Unknown
// map keys are ignored; any field whose value has an unexpected IPLD kind
// (e.g. "ts" not representable as AsInt) is simply left at its Go zero
// value rather than causing a decode error.
//
// Parameters:
//   - raw: the DAG-CBOR-encoded bytes to decode (as produced by
//     encodePeerAddedToCBOR, or any DAG-CBOR map with compatible keys).
//
// Returns:
//   - *PeerAddedGo: the decoded event with whatever fields were found. If
//     the decoded node is not a map (n.Kind() != datamodel.Kind_Map), (nil,
//     nil) is returned — a non-map document is treated as "no event", not
//     an error, so callers must check for a nil result even when err is
//     nil.
//   - error: non-nil only if the DAG-CBOR decode step itself
//     (dagcbor.Decode) fails, e.g. malformed/truncated CBOR input.
func decodePeerAddedFromCBOR(raw []byte) (*PeerAddedGo, error) {
    nb := basicnode.Prototype__Any{}.NewBuilder()
    if err := dagcbor.Decode(nb, bytes.NewReader(raw)); err != nil {
        return nil, err
    }
    n := nb.Build()
    if n.Kind() != datamodel.Kind_Map {
        return nil, nil
    }
    // Extract fields from the map
    var out PeerAddedGo
    it := n.MapIterator()
    for !it.Done() {
        k, v, _ := it.Next()
        ks, _ := k.AsString()
        switch ks {
        case "type":
            if s, err := v.AsString(); err == nil {
                out.Type = s
            }
        case "ts":
            if i, err := v.AsInt(); err == nil {
                out.Ts = i
            }
        case "peer":
            if s, err := v.AsString(); err == nil {
                out.Peer = s
            }
        case "prev":
            if s, err := v.AsString(); err == nil {
                if s != "" {
                    out.Prev = &s
                }
            }
        }
    }
    return &out, nil
}

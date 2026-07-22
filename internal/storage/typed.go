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

// PeerAddedGo mirrors the event representation.
type PeerAddedGo struct {
	// Type is the event type discriminator (e.g. "peer_added").
	Type string `ipld:"type"`
	// Ts is the event timestamp (Unix nanoseconds).
	Ts int64 `ipld:"ts"`
	// Peer is the string-encoded peer ID involved in the event.
	Peer string `ipld:"peer"`
	// Prev is the string-encoded CID of the previous event in the log, if any.
	Prev *string `ipld:"prev,omitempty"`
}

// Encoding/decoding uses basicnode to avoid schema inference at init time.

// computeCBORCID computes the CIDv1 dag-cbor/SHA2-256 CID for raw DAG-CBOR bytes.
//
// Parameters:
//   - raw ([]byte): already-encoded DAG-CBOR bytes.
//
// Returns:
//   - cid.Cid: the computed content identifier.
//   - error: non-nil if hashing/prefix summation fails.
func computeCBORCID(raw []byte) (cid.Cid, error) {
	prefix := cid.Prefix{Version: 1, Codec: cid.DagCBOR, MhType: mh.SHA2_256, MhLength: -1}
	return prefix.Sum(raw)
}

// encodePeerAddedToCBOR encodes a PeerAddedGo event as a DAG-CBOR map with
// fields "type", "ts", "peer", and an optional "prev" (included only when
// pa.Prev is non-nil and non-empty), then computes its CID.
//
// Parameters:
//   - pa (*PeerAddedGo): the event to encode.
//
// Returns:
//   - []byte: the raw DAG-CBOR encoded bytes.
//   - cid.Cid: the CIDv1 dag-cbor/SHA2-256 CID of the encoded bytes.
//   - error: non-nil if building the IPLD map or encoding to CBOR fails.
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

// decodePeerAddedFromCBOR decodes raw DAG-CBOR bytes into a PeerAddedGo event.
// If the decoded node is not a map, returns (nil, nil) rather than an error.
// Unrecognized map keys are silently ignored; fields whose values fail their
// expected-type assertion (AsString/AsInt) are left at their zero value.
//
// Parameters:
//   - raw ([]byte): the DAG-CBOR encoded event bytes.
//
// Returns:
//   - *PeerAddedGo: the decoded event, or nil if raw does not decode to a map.
//   - error: non-nil if the DAG-CBOR decode itself fails.
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

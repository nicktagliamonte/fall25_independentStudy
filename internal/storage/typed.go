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
	Type string  `ipld:"type"`
	Ts   int64   `ipld:"ts"`
	Peer string  `ipld:"peer"`
	Prev *string `ipld:"prev,omitempty"`
}

// Encoding/decoding uses basicnode to avoid schema inference at init time.

func computeCBORCID(raw []byte) (cid.Cid, error) {
	prefix := cid.Prefix{Version: 1, Codec: cid.DagCBOR, MhType: mh.SHA2_256, MhLength: -1}
	return prefix.Sum(raw)
}

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

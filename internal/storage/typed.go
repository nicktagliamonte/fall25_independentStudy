package storage

// Purpose: Strongly-typed IPLD bindings for storage events using bindnode.

import (
	"bytes"

	"github.com/ipfs/go-cid"
	"github.com/ipld/go-ipld-prime/codec/dagcbor"
	"github.com/ipld/go-ipld-prime/datamodel"
	"github.com/ipld/go-ipld-prime/node/bindnode"
	mh "github.com/multiformats/go-multihash"
)

// PeerAddedGo mirrors the event representation.
type PeerAddedGo struct {
	Type string  `ipld:"type"`
	Ts   int64   `ipld:"ts"`
	Peer string  `ipld:"peer"`
	Prev *string `ipld:"prev,omitempty"`
}

var (
	peerAddedProto datamodel.NodePrototype = bindnode.Prototype((*PeerAddedGo)(nil), nil)
)

func computeCBORCID(raw []byte) (cid.Cid, error) {
	prefix := cid.Prefix{Version: 1, Codec: cid.DagCBOR, MhType: mh.SHA2_256, MhLength: -1}
	return prefix.Sum(raw)
}

func encodePeerAddedToCBOR(pa *PeerAddedGo) ([]byte, cid.Cid, error) {
	n := bindnode.Wrap(pa, nil)
	var buf bytes.Buffer
	if err := dagcbor.Encode(n.Representation(), &buf); err != nil {
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
	nb := peerAddedProto.NewBuilder()
	if err := dagcbor.Decode(nb, bytes.NewReader(raw)); err != nil {
		return nil, err
	}
	n := nb.Build()
	obj, _ := bindnode.Unwrap(n).(*PeerAddedGo)
	return obj, nil
}

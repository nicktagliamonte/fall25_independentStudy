// Purpose: Tests for CID set reconciliation helpers.

package sync

import (
	"bytes"
	"testing"

	"github.com/ipfs/go-cid"
)

func TestBuildIBLTFromCIDs(t *testing.T) {
	c1, _ := cid.Decode("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")
	c2, _ := cid.Decode("QmYwAPJzv5CZsnA625s3Xf2nemtYgPpHdWEz79ojWnPbdG")
	tbl := BuildIBLTFromCIDs([]cid.Cid{c1, c2}, 64)
	if tbl == nil || tbl.CellCount != 64 {
		t.Fatalf("BuildIBLTFromCIDs: unexpected tbl")
	}
	diff := tbl.Subtract(NewIBLT(64))
	if diff == nil {
		t.Fatal("Subtract failed")
	}
	pr := diff.Peel()
	if len(pr.Positive) != 2 {
		t.Errorf("peel positive want 2 got %d", len(pr.Positive))
	}
}

func TestCIDsForKeyHashes(t *testing.T) {
	c1, _ := cid.Decode("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")
	c2, _ := cid.Decode("QmYwAPJzv5CZsnA625s3Xf2nemtYgPpHdWEz79ojWnPbdG")
	cids := []cid.Cid{c1, c2}
	h1 := KeyHash([]byte(c1.String()))
	h2 := KeyHash([]byte(c2.String()))
	res := CIDsForKeyHashes(cids, []uint64{h1})
	if len(res) != 1 || !res[0].Equals(c1) {
		t.Errorf("CIDsForKeyHashes(h1) want [c1] got %v", res)
	}
	res = CIDsForKeyHashes(cids, []uint64{h1, h2})
	if len(res) != 2 {
		t.Errorf("CIDsForKeyHashes(h1,h2) want 2 got %d", len(res))
	}
	res = CIDsForKeyHashes(cids, []uint64{999})
	if len(res) != 0 {
		t.Errorf("CIDsForKeyHashes(999) want [] got %v", res)
	}
}

func TestFetchProtocolRoundtrip(t *testing.T) {
	hashes := []uint64{1, 2, 3}
	var buf bytes.Buffer
	if err := WriteFetchRequest(&buf, hashes); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFetchRequest(&buf, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Errorf("ReadFetchRequest got %v", got)
	}
}

func TestFetchResponseRoundtrip(t *testing.T) {
	c1, _ := cid.Decode("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")
	cids := []cid.Cid{c1}
	var buf bytes.Buffer
	if err := WriteFetchResponse(&buf, cids); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFetchResponse(&buf, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].Equals(c1) {
		t.Errorf("ReadFetchResponse got %v", got)
	}
}

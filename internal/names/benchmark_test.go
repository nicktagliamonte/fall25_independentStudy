package names

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"
	"golang.org/x/crypto/curve25519"
)

func BenchmarkObjectBuild(b *testing.B) {
	_, signer := testKeys(b)
	readerPrivate := make([]byte, 32)
	_, _ = rand.Read(readerPrivate)
	readerPublic, _ := curve25519.X25519(readerPrivate, curve25519.Basepoint)
	for _, size := range []int{64 << 10, 4 << 20, 64 << 20} {
		b.Run(fmt.Sprintf("bytes_%d", size), func(b *testing.B) {
			data := bytes.Repeat([]byte{0x5a}, size)
			sink := func(_ context.Context, raw []byte) (ContentKey, error) { return sha256.Sum256(raw), nil }
			b.SetBytes(int64(size))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := BuildObject(context.Background(), bytes.NewReader(data), sink, BuildObjectOptions{Encryption: "private", KeyEpoch: uint64(i + 1), ReaderPublicKeys: [][]byte{readerPublic}, Signer: signer}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkNameRecordSignAndValidate(b *testing.B) {
	owner, private := testKeys(b)
	namespace, _ := NewNamespaceID()
	record := testRecord(b, namespace, "/bench/object", owner, private, 0, nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		record.Timestamp++
		record.Nonce[0]++
		if err := record.Sign(private); err != nil {
			b.Fatal(err)
		}
		if err := record.Validate(time.Unix(0, record.Timestamp), nil); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSevenMemberQuorumCertificateValidate(b *testing.B) {
	now := time.Now()
	owner, ownerPrivate := testKeys(b)
	namespace, _ := NewNamespaceID()
	validators := make([][]byte, 7)
	privateKeys := make([]ed25519.PrivateKey, 7)
	for i := range validators {
		validators[i], privateKeys[i] = testKeys(b)
	}
	root := &NamespaceRoot{Version: FormatVersion, Namespace: namespace[:], Validators: validators, FaultThreshold: 2, CommitteeEpoch: 1, Timestamp: now.UnixNano(), Nonce: bytes.Repeat([]byte{7}, 16)}
	if err := root.Sign(ownerPrivate); err != nil {
		b.Fatal(err)
	}
	record := testRecord(b, namespace, "/bench/bft", owner, ownerPrivate, 0, nil)
	raw, _ := record.Marshal()
	hash := sha256.Sum256(raw)
	votes := make([]QuorumVote, 5)
	for i := range votes {
		votes[i] = QuorumVote{Version: FormatVersion, Namespace: namespace[:], NameID: record.NameID, RecordHash: hash[:], CommitteeEpoch: 1, View: 1, Phase: PhaseCommit, Timestamp: now.UnixNano(), Nonce: bytes.Repeat([]byte{byte(i + 1)}, 16)}
		if err := votes[i].Sign(privateKeys[i]); err != nil {
			b.Fatal(err)
		}
	}
	qc, err := AssembleQuorumCertificate(root, votes, now)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := qc.Validate(root, now); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSevenMemberThreePhaseCommit(b *testing.B) {
	now := time.Now()
	owner, ownerPrivate := testKeys(b)
	namespace, _ := NewNamespaceID()
	validators := make([][]byte, 7)
	privateKeys := make([]ed25519.PrivateKey, 7)
	for i := range validators {
		validators[i], privateKeys[i] = testKeys(b)
	}
	root := &NamespaceRoot{Version: FormatVersion, Namespace: namespace[:], Validators: validators, FaultThreshold: 2, CommitteeEpoch: 1, Timestamp: now.UnixNano(), Nonce: bytes.Repeat([]byte{11}, 16)}
	if err := root.Sign(ownerPrivate); err != nil {
		b.Fatal(err)
	}
	journals := make([]*QuorumJournal, 5)
	for i := range journals {
		journals[i] = &QuorumJournal{Datastore: dssync.MutexWrap(ds.NewMapDatastore()), Private: privateKeys[i], Now: func() time.Time { return now }}
	}
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		record := testRecord(b, namespace, fmt.Sprintf("/bench/commit/%09d", iteration), owner, ownerPrivate, 0, nil)
		raw, _ := record.Marshal()
		var justify *QuorumCertificate
		for _, phase := range []QuorumPhase{PhasePrepare, PhasePrecommit, PhaseCommit} {
			votes := make([]QuorumVote, 5)
			for validator := range journals {
				vote, err := journals[validator].Vote(context.Background(), root, raw, phase, 1, justify)
				if err != nil {
					b.Fatal(err)
				}
				votes[validator] = *vote
			}
			qc, err := AssembleQuorumCertificate(root, votes, now)
			if err != nil {
				b.Fatal(err)
			}
			justify = qc
		}
	}
}

// BenchmarkLogicalNameScale is run with -benchtime=1x. Each cell builds a
// fresh current-name index and complete version history so index cardinality
// and total update work are measured independently.
func BenchmarkLogicalNameScale(b *testing.B) {
	for _, count := range []int{10_000, 100_000} {
		for _, versions := range []int{1, 4, 16} {
			b.Run(fmt.Sprintf("names_%d/versions_%d", count, versions), func(b *testing.B) {
				for iteration := 0; iteration < b.N; iteration++ {
					owner, private := testKeys(b)
					namespace, _ := NewNamespaceID()
					index := &memorySearchIndex{entries: make(map[string]struct{})}
					service := testService()
					service.SetSearchIndex(index)
					for nameNumber := 0; nameNumber < count; nameNumber++ {
						path := fmt.Sprintf("/scale/%09d.dat", nameNumber)
						current := testRecord(b, namespace, path, owner, private, 0, nil)
						current.Policy.StrictPublish = false
						_ = current.Sign(private)
						raw, _ := current.Marshal()
						if _, err := service.Create(context.Background(), raw); err != nil {
							b.Fatal(err)
						}
						for generation := 1; generation < versions; generation++ {
							next := testRecord(b, namespace, path, owner, private, uint64(generation), current)
							next.Policy.StrictPublish = false
							_ = next.Sign(private)
							raw, _ = next.Marshal()
							if _, err := service.Update(context.Background(), bytesToNameID(next.NameID), uint64(generation-1), raw); err != nil {
								b.Fatal(err)
							}
							current = next
						}
					}
					index.mu.Lock()
					entries := len(index.entries)
					index.mu.Unlock()
					if entries != count {
						b.Fatalf("index entries=%d, want %d", entries, count)
					}
					b.ReportMetric(float64(entries), "index_entries")
					b.ReportMetric(float64(count*versions), "committed_records")
				}
			})
		}
	}
}

// BenchmarkExactResolutionVersionIndependence measures the current-head path
// after retaining different amounts of history. Exact resolution reads one
// current key and validates one record; it does not scan historical versions.
func BenchmarkExactResolutionVersionIndependence(b *testing.B) {
	const namesCount = 1_000
	for _, versions := range []int{1, 4, 16} {
		b.Run(fmt.Sprintf("names_%d/versions_%d", namesCount, versions), func(b *testing.B) {
			b.StopTimer()
			owner, private := testKeys(b)
			namespace, _ := NewNamespaceID()
			service := testService()
			var target NameID
			for nameNumber := 0; nameNumber < namesCount; nameNumber++ {
				path := fmt.Sprintf("/resolve/%09d.dat", nameNumber)
				current := testRecord(b, namespace, path, owner, private, 0, nil)
				current.Policy.StrictPublish = false
				_ = current.Sign(private)
				raw, _ := current.Marshal()
				if _, err := service.Create(context.Background(), raw); err != nil {
					b.Fatal(err)
				}
				for generation := 1; generation < versions; generation++ {
					next := testRecord(b, namespace, path, owner, private, uint64(generation), current)
					next.Policy.StrictPublish = false
					_ = next.Sign(private)
					raw, _ = next.Marshal()
					if _, err := service.Update(context.Background(), bytesToNameID(next.NameID), uint64(generation-1), raw); err != nil {
						b.Fatal(err)
					}
					current = next
				}
				if nameNumber == namesCount-1 {
					target = bytesToNameID(current.NameID)
				}
			}
			b.ReportMetric(float64(namesCount*versions), "retained_records")
			b.ReportAllocs()
			b.StartTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				if _, _, err := service.Get(context.Background(), target); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

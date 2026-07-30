package storage

import (
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/nicktagliamonte/fall25_independentStudy/internal/tuplespace"
)

type concurrentOfferTupleSpace struct {
	mu          sync.Mutex
	values      map[string][]byte
	inFlight    int
	maxInFlight int
}

func (m *concurrentOfferTupleSpace) TsPut(string, []byte) (int, error) {
	return -1, errors.New("not implemented")
}

func (m *concurrentOfferTupleSpace) TsGet(string) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func (m *concurrentOfferTupleSpace) TsRead(name string) ([]byte, error) {
	m.mu.Lock()
	m.inFlight++
	if m.inFlight > m.maxInFlight {
		m.maxInFlight = m.inFlight
	}
	m.mu.Unlock()

	time.Sleep(20 * time.Millisecond)

	m.mu.Lock()
	m.inFlight--
	value, ok := m.values[name]
	m.mu.Unlock()
	if !ok {
		return nil, tuplespace.ErrTupleNotFound
	}
	return append([]byte(nil), value...), nil
}

func TestStorageCandidateReadsUseBoundedConcurrency(t *testing.T) {
	const peerCount = 8
	peers := make([]peer.ID, 0, peerCount)
	values := make(map[string][]byte, peerCount)
	for index := 0; index < peerCount; index++ {
		pid := tokenTestPeerID(t)
		peers = append(peers, pid)
		offer := StorageAvailableOffer{
			PeerID:               pid.String(),
			StorageAvailability:  1 << 30,
			ReputationScore:      1,
			AvailabilityDuration: 60,
			Timestamp:            time.Now().Unix(),
		}
		encoded, err := json.Marshal(offer)
		if err != nil {
			t.Fatal(err)
		}
		values[StorageAvailableTuplePrefix+pid.String()] = encoded
	}
	ts := &concurrentOfferTupleSpace{values: values}
	protocol := NewStorageAvailableProtocol(ts)
	protocol.CandidateConcurrency = 4
	protocol.PeerIDsToCheck = func() []peer.ID {
		return append(append([]peer.ID(nil), peers...), peers[0])
	}

	excluded := map[peer.ID]bool{peers[1]: true}
	candidates, err := protocol.FindAnyStorageAvailableCandidatesExcluding(
		tokenTestPeerID(t),
		0,
		excluded,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != peerCount-1 {
		t.Fatalf("candidate count = %d, want %d", len(candidates), peerCount-1)
	}
	if ts.maxInFlight < 2 || ts.maxInFlight > protocol.CandidateConcurrency {
		t.Fatalf(
			"maximum concurrent reads = %d, want between 2 and %d",
			ts.maxInFlight,
			protocol.CandidateConcurrency,
		)
	}
	if !sort.SliceIsSorted(candidates, func(i, j int) bool {
		return candidates[i].PeerID.String() < candidates[j].PeerID.String()
	}) {
		t.Fatal("candidate results are not deterministically sorted")
	}
	for _, candidate := range candidates {
		if candidate.PeerID == peers[1] {
			t.Fatal("excluded peer appeared in candidate results")
		}
	}
}

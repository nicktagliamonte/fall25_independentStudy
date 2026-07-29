package tuplespace

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

type fixedOwnerResolver struct {
	owner peer.ID
}

func (r fixedOwnerResolver) ResolveTupleOwner(context.Context, string) (peer.ID, error) {
	return r.owner, nil
}

func connectTupleHosts(t *testing.T, a, b peerHost) {
	t.Helper()
	info := peer.AddrInfo{ID: b.ID(), Addrs: b.Addrs()}
	if err := a.Connect(context.Background(), info); err != nil {
		t.Fatalf("connect hosts: %v", err)
	}
}

type peerHost interface {
	ID() peer.ID
	Addrs() []multiaddr.Multiaddr
	Connect(context.Context, peer.AddrInfo) error
}

func TestDistributedTupleSpaceRoutesExactOperationsToOwner(t *testing.T) {
	ownerHost, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer ownerHost.Close()
	clientHost, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer clientHost.Close()
	connectTupleHosts(t, clientHost, ownerHost)

	resolver := fixedOwnerResolver{owner: ownerHost.ID()}
	ownerTS, err := NewDistributedTupleSpace(ownerHost, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer ownerTS.Close()
	clientTS, err := NewDistributedTupleSpace(clientHost, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer clientTS.Close()

	if _, err := clientTS.TsPut("task:image:001", []byte("payload")); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := clientTS.TsRead("task:image:001")
	if err != nil || string(got) != "payload" {
		t.Fatalf("read = %q, %v", got, err)
	}
	got, err = clientTS.TsGet("task:image:001")
	if err != nil || string(got) != "payload" {
		t.Fatalf("get = %q, %v", got, err)
	}
	if _, err := clientTS.TsRead("task:image:001"); !errors.Is(err, ErrTupleNotFound) {
		t.Fatalf("read after get = %v", err)
	}
}

func TestDistributedTupleSpaceConcurrentClientsConsumeOnce(t *testing.T) {
	ownerHost, _ := libp2p.New()
	defer ownerHost.Close()
	clientAHost, _ := libp2p.New()
	defer clientAHost.Close()
	clientBHost, _ := libp2p.New()
	defer clientBHost.Close()
	connectTupleHosts(t, clientAHost, ownerHost)
	connectTupleHosts(t, clientBHost, ownerHost)

	resolver := fixedOwnerResolver{owner: ownerHost.ID()}
	ownerTS, _ := NewDistributedTupleSpace(ownerHost, resolver)
	defer ownerTS.Close()
	clientA, _ := NewDistributedTupleSpace(clientAHost, resolver)
	defer clientA.Close()
	clientB, _ := NewDistributedTupleSpace(clientBHost, resolver)
	defer clientB.Close()

	if _, err := clientA.TsPut("task:exclusive", []byte("once")); err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for _, client := range []*DistributedTupleSpace{clientA, clientB} {
		wg.Add(1)
		go func(ts *DistributedTupleSpace) {
			defer wg.Done()
			if _, err := ts.TsGet("task:exclusive"); err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrTupleNotFound) {
				t.Errorf("get: %v", err)
			}
		}(client)
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("successful consumers = %d, want 1", successes.Load())
	}
}

package tuplespace

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/multiformats/go-multiaddr"
)

type fixedOwnerResolver struct {
	owner peer.ID
}

func (r fixedOwnerResolver) ResolveTupleOwner(context.Context, string) (peer.ID, error) {
	return r.owner, nil
}

type resolvingOwnerResolver struct {
	fixedOwnerResolver
	info  peer.AddrInfo
	calls atomic.Int32
}

func (r *resolvingOwnerResolver) FindPeer(context.Context, peer.ID) (peer.AddrInfo, error) {
	r.calls.Add(1)
	return r.info, nil
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
	if _, err := clientTS.TsReplace("task:image:001", []byte("replacement")); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, err = clientTS.TsRead("task:image:001")
	if err != nil || string(got) != "replacement" {
		t.Fatalf("read replacement = %q, %v", got, err)
	}
	got, err = clientTS.TsGet("task:image:001")
	if err != nil || string(got) != "replacement" {
		t.Fatalf("get = %q, %v", got, err)
	}
	if _, err := clientTS.TsRead("task:image:001"); !errors.Is(err, ErrTupleNotFound) {
		t.Fatalf("read after get = %v", err)
	}
}

func TestDistributedTupleSpaceDeduplicatesRetriedPutAtOwner(t *testing.T) {
	h, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	space, err := NewDistributedTupleSpace(h, fixedOwnerResolver{owner: h.ID()})
	if err != nil {
		t.Fatal(err)
	}
	defer space.Close()

	request := tupleWireRequest{
		Operation: "put",
		Name:      "task:deduplicated",
		Value:     []byte("payload"),
		RequestID: "retry-id",
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := space.applyLocalOnce(context.Background(), request); err != nil {
			t.Fatalf("attempt %d: %v", attempt+1, err)
		}
	}
	value, err := space.local.TsGet(request.Name)
	if err != nil || string(value) != "payload" {
		t.Fatalf("first get = %q, %v", value, err)
	}
	if _, err := space.local.TsGet(request.Name); !errors.Is(err, ErrTupleNotFound) {
		t.Fatalf("duplicate put created an extra tuple copy: %v", err)
	}
}

func TestDistributedTupleSpaceRoutesOverEstablishedOverlay(t *testing.T) {
	clientHost, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer clientHost.Close()
	relayHost, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer relayHost.Close()
	ownerHost, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer ownerHost.Close()

	resolver := fixedOwnerResolver{owner: ownerHost.ID()}
	clientTS, err := NewDistributedTupleSpace(clientHost, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer clientTS.Close()
	relayTS, err := NewDistributedTupleSpace(relayHost, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer relayTS.Close()
	ownerTS, err := NewDistributedTupleSpace(ownerHost, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer ownerTS.Close()
	connectTupleHosts(t, clientHost, relayHost)
	connectTupleHosts(t, relayHost, ownerHost)
	if clientHost.Network().Connectedness(ownerHost.ID()) == network.Connected {
		t.Fatal("test requires client and owner to have no direct connection")
	}

	if _, err := clientTS.TsPut("task:overlay-route", []byte("payload")); err != nil {
		t.Fatalf("overlay put: %v", err)
	}
	got, err := clientTS.TsRead("task:overlay-route")
	if err != nil || string(got) != "payload" {
		t.Fatalf("overlay read = %q, %v", got, err)
	}
	if clientHost.Network().Connectedness(ownerHost.ID()) == network.Connected {
		t.Fatal("overlay request unexpectedly created a direct client-owner connection")
	}
}

func TestDistributedTupleSpaceRacesAroundBlockedOverlayBranch(t *testing.T) {
	clientHost, _ := libp2p.New()
	defer clientHost.Close()
	firstCandidate, _ := libp2p.New()
	defer firstCandidate.Close()
	secondCandidate, _ := libp2p.New()
	defer secondCandidate.Close()
	ownerHost, _ := libp2p.New()
	defer ownerHost.Close()
	connectTupleHosts(t, clientHost, firstCandidate)
	connectTupleHosts(t, clientHost, secondCandidate)

	candidates := connectedRouteCandidates(clientHost, ownerHost.ID(), nil)
	if len(candidates) != 2 {
		t.Fatalf("route candidates = %v", candidates)
	}
	hosts := map[peer.ID]host.Host{
		firstCandidate.ID():  firstCandidate,
		secondCandidate.ID(): secondCandidate,
	}
	blocked := hosts[candidates[0]]
	relay := hosts[candidates[1]]
	blocked.SetStreamHandler(NativeTupleProtocolID, func(stream network.Stream) {
		defer stream.Close()
		time.Sleep(2 * time.Second)
	})
	resolver := fixedOwnerResolver{owner: ownerHost.ID()}
	client, _ := NewDistributedTupleSpace(clientHost, resolver)
	defer client.Close()
	relaySpace, _ := NewDistributedTupleSpace(relay, resolver)
	defer relaySpace.Close()
	owner, _ := NewDistributedTupleSpace(ownerHost, resolver)
	defer owner.Close()
	connectTupleHosts(t, relay, ownerHost)

	started := time.Now()
	if _, err := client.TsPut("task:parallel-route", []byte("payload")); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("parallel route took %v; blocked branch delayed success", elapsed)
	}
}

func TestDistributedTupleSpaceResolvesUnknownOwnerAddress(t *testing.T) {
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

	if got := clientHost.Peerstore().Addrs(ownerHost.ID()); len(got) != 0 {
		t.Fatalf("client unexpectedly knows owner addresses: %v", got)
	}
	ownerResolver := fixedOwnerResolver{owner: ownerHost.ID()}
	clientResolver := &resolvingOwnerResolver{
		fixedOwnerResolver: ownerResolver,
		info: peer.AddrInfo{
			ID:    ownerHost.ID(),
			Addrs: ownerHost.Addrs(),
		},
	}
	ownerTS, err := NewDistributedTupleSpace(ownerHost, ownerResolver)
	if err != nil {
		t.Fatal(err)
	}
	defer ownerTS.Close()
	clientTS, err := NewDistributedTupleSpace(clientHost, clientResolver)
	if err != nil {
		t.Fatal(err)
	}
	defer clientTS.Close()

	if _, err := clientTS.TsPut("task:address-recovery", []byte("payload")); err != nil {
		t.Fatalf("put via DHT-resolved address: %v", err)
	}
	if clientResolver.calls.Load() == 0 {
		t.Fatal("owner address lookup was not attempted")
	}
	got, err := clientTS.TsRead("task:address-recovery")
	if err != nil || string(got) != "payload" {
		t.Fatalf("read = %q, %v", got, err)
	}
}

func TestDistributedTupleSpaceRefreshesUndialableOwnerAddress(t *testing.T) {
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

	stale, err := multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/1")
	if err != nil {
		t.Fatal(err)
	}
	clientHost.Peerstore().AddAddr(ownerHost.ID(), stale, peerstore.PermanentAddrTTL)
	ownerResolver := fixedOwnerResolver{owner: ownerHost.ID()}
	clientResolver := &resolvingOwnerResolver{
		fixedOwnerResolver: ownerResolver,
		info: peer.AddrInfo{
			ID:    ownerHost.ID(),
			Addrs: ownerHost.Addrs(),
		},
	}
	ownerTS, err := NewDistributedTupleSpace(ownerHost, ownerResolver)
	if err != nil {
		t.Fatal(err)
	}
	defer ownerTS.Close()
	clientTS, err := NewDistributedTupleSpace(clientHost, clientResolver)
	if err != nil {
		t.Fatal(err)
	}
	defer clientTS.Close()

	if _, err := clientTS.TsPut("task:address-refresh", []byte("payload")); err != nil {
		t.Fatalf("put after DHT address refresh: %v", err)
	}
	if clientResolver.calls.Load() == 0 {
		t.Fatal("DHT address refresh was not attempted")
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

func TestDistributedTupleSpaceAssociativeOperationsAcrossPeers(t *testing.T) {
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

	if _, err := clientTS.TsPut("task:image:dataset-a:001", []byte("image-task")); err != nil {
		t.Fatalf("put image task: %v", err)
	}
	if _, err := clientTS.TsPut("task:text:dataset-a:001", []byte("text-task")); err != nil {
		t.Fatalf("put text task: %v", err)
	}

	got, err := clientTS.TsRead("task:image:dataset-a:*")
	if err != nil || string(got) != "image-task" {
		t.Fatalf("wildcard read = %q, %v", got, err)
	}
	got, err = clientTS.TsGet(`task:(image|text):dataset-a:001`)
	if err != nil || string(got) != "image-task" {
		t.Fatalf("regex get = %q, %v", got, err)
	}
	if _, err := clientTS.TsRead("task:image:dataset-a:*"); !errors.Is(err, ErrTupleNotFound) {
		t.Fatalf("image task after consuming regex get = %v", err)
	}
	got, err = clientTS.TsRead("task:text:dataset-a:*")
	if err != nil || string(got) != "text-task" {
		t.Fatalf("unconsumed text task = %q, %v", got, err)
	}
}

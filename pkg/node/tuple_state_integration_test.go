package node

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	kaddht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	myhost "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
	"github.com/nicktagliamonte/fall25_independentStudy/internal/tuplespace"
)

type liveTupleFailoverResolver struct {
	primary   peer.ID
	successor peer.ID
	infos     map[peer.ID]peer.AddrInfo
}

func (r liveTupleFailoverResolver) ResolveTupleOwner(context.Context, string) (peer.ID, error) {
	return r.primary, nil
}

func (r liveTupleFailoverResolver) ResolveTupleOwnerAfter(
	_ context.Context,
	_ string,
	excluded string,
) (peer.ID, error) {
	if excluded != r.primary.String() {
		return "", fmt.Errorf("unexpected excluded tuple owner %q", excluded)
	}
	return r.successor, nil
}

func (r liveTupleFailoverResolver) FindPeer(
	_ context.Context,
	id peer.ID,
) (peer.AddrInfo, error) {
	info, ok := r.infos[id]
	if !ok {
		return peer.AddrInfo{}, errors.New("peer not found")
	}
	return info, nil
}

func TestDurableTupleStateSurvivesOwnerFailureAcrossLiveDHT(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	hosts := make([]host.Host, 3)
	dhts := make([]*kaddht.IpfsDHT, 3)
	for i := range hosts {
		h, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
		if err != nil {
			t.Fatal(err)
		}
		hosts[i] = h
		t.Cleanup(func() { _ = h.Close() })
	}
	for i, h := range hosts {
		dht, err := myhost.NewDHT(ctx, h, myhost.DHTConfig{
			Mode:               myhost.DHTModeServer,
			UseTokenDHT:        true,
			BootstrapPeersFunc: func() []peer.AddrInfo { return nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		dhts[i] = dht
		t.Cleanup(func() { _ = dht.Close() })
	}
	for i := range hosts {
		for j := i + 1; j < len(hosts); j++ {
			connectDurableTupleTestHosts(t, ctx, hosts[i], hosts[j])
		}
	}
	for _, dht := range dhts {
		if err := dht.Bootstrap(ctx); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		ready := true
		for _, dht := range dhts {
			ready = ready && dht.RoutingTable().Size() >= 2
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"DHTs did not converge: sizes=(%d,%d,%d)",
				dhts[0].RoutingTable().Size(),
				dhts[1].RoutingTable().Size(),
				dhts[2].RoutingTable().Size(),
			)
		}
		time.Sleep(25 * time.Millisecond)
	}

	resolver := liveTupleFailoverResolver{
		primary:   hosts[0].ID(),
		successor: hosts[1].ID(),
		infos: map[peer.ID]peer.AddrInfo{
			hosts[0].ID(): {ID: hosts[0].ID(), Addrs: hosts[0].Addrs()},
			hosts[1].ID(): {ID: hosts[1].ID(), Addrs: hosts[1].Addrs()},
			hosts[2].ID(): {ID: hosts[2].ID(), Addrs: hosts[2].Addrs()},
		},
	}
	spaces := make([]*tuplespace.DistributedTupleSpace, 3)
	for i := range hosts {
		space, err := tuplespace.NewDistributedTupleSpace(hosts[i], resolver)
		if err != nil {
			t.Fatal(err)
		}
		if err := space.EnableDurableState(tuplespace.NewDHTValueStoreAdapter(dhts[i])); err != nil {
			t.Fatal(err)
		}
		space.SetDurableStateTiming(50*time.Millisecond, 300*time.Millisecond, 10*time.Millisecond)
		spaces[i] = space
		t.Cleanup(space.Close)
	}

	if _, err := spaces[2].TsPut("task:live-failover", []byte("survives")); err != nil {
		t.Fatalf("put through primary: %v", err)
	}
	if err := dhts[0].Close(); err != nil {
		t.Fatal(err)
	}
	if err := hosts[0].Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(350 * time.Millisecond)

	value, err := spaces[2].TsGet("task:live-failover")
	if err != nil || string(value) != "survives" {
		t.Fatalf("get through successor = %q, %v", value, err)
	}
	if _, err := spaces[2].TsRead("task:live-failover"); !errors.Is(err, tuplespace.ErrTupleNotFound) {
		t.Fatalf("tuple remained after successor get: %v", err)
	}
}

func connectDurableTupleTestHosts(
	t *testing.T,
	ctx context.Context,
	from host.Host,
	to host.Host,
) {
	t.Helper()
	info := peer.AddrInfo{ID: to.ID(), Addrs: to.Addrs()}
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for {
		if err := from.Connect(ctx, info); err == nil {
			return
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			t.Fatalf("connect %s -> %s: %v", from.ID(), to.ID(), lastErr)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("connect %s -> %s: %v", from.ID(), to.ID(), ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

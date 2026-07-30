package node

import (
	"context"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	libpeerstore "github.com/libp2p/go-libp2p/core/peerstore"
	myhost "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

func TestRemovePersistedDefaultBootstrapPeers(t *testing.T) {
	ctx := context.Background()
	h, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	defer h.Close()
	other, err := myhost.NewHost(ctx, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("create non-default host: %v", err)
	}
	defer other.Close()

	_, datastore := mystore.NewEphemeralBlockstore()
	persistentPeers, err := myhost.NewPeerStore(datastore)
	if err != nil {
		t.Fatalf("create peerstore: %v", err)
	}
	defaults := myhost.DefaultBootstrapPeerInfos()
	if len(defaults) == 0 {
		t.Fatal("default bootstrap list is empty")
	}
	for _, info := range defaults {
		if err := persistentPeers.Upsert(info.ID, info.Addrs, 0, "seed"); err != nil {
			t.Fatalf("persist default %s: %v", info.ID, err)
		}
		h.Peerstore().AddAddrs(info.ID, info.Addrs, libpeerstore.PermanentAddrTTL)
	}
	otherInfo := peer.AddrInfo{ID: other.ID(), Addrs: other.Addrs()}
	if err := persistentPeers.Upsert(otherInfo.ID, otherInfo.Addrs, 0, "handshake"); err != nil {
		t.Fatalf("persist non-default: %v", err)
	}
	h.Peerstore().AddAddrs(otherInfo.ID, otherInfo.Addrs, libpeerstore.PermanentAddrTTL)

	if err := removePersistedDefaultBootstrapPeers(h, persistentPeers); err != nil {
		t.Fatalf("remove defaults: %v", err)
	}
	for _, info := range defaults {
		if _, ok := persistentPeers.StablePeerInfo(info.ID); ok {
			t.Errorf("default %s remains in persistent peerstore", info.ID)
		}
		if addrs := h.Peerstore().Addrs(info.ID); len(addrs) != 0 {
			t.Errorf("default %s remains in libp2p peerstore: %v", info.ID, addrs)
		}
	}
	if info, ok := persistentPeers.StablePeerInfo(otherInfo.ID); !ok || len(info.Addrs) == 0 {
		t.Fatal("non-default peer was removed from persistent peerstore")
	}
	if addrs := h.Peerstore().Addrs(otherInfo.ID); len(addrs) == 0 {
		t.Fatal("non-default peer was removed from libp2p peerstore")
	}
}

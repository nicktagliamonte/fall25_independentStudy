package control

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	myhost "github.com/nicktagliamonte/fall25_independentStudy/internal/net"
)

func TestConnectExplicitPeerBypassesDialBackoff(t *testing.T) {
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := reservation.Addr().(*net.TCPAddr).Port
	if err := reservation.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}

	targetKey, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("generate target identity: %v", err)
	}
	targetID, err := peer.IDFromPrivateKey(targetKey)
	if err != nil {
		t.Fatalf("derive target peer ID: %v", err)
	}
	addr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", port))
	if err != nil {
		t.Fatalf("build target address: %v", err)
	}
	info := peer.AddrInfo{ID: targetID, Addrs: []multiaddr.Multiaddr{addr}}

	dialer, err := myhost.NewHost(context.Background(), []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("create dialer: %v", err)
	}
	defer dialer.Close()

	failCtx, failCancel := context.WithTimeout(context.Background(), time.Second)
	err = dialer.Connect(failCtx, info)
	failCancel()
	if err == nil {
		t.Fatal("initial dial unexpectedly succeeded before target started")
	}

	target, err := myhost.NewHostWithPriv(
		context.Background(),
		[]string{fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", port)},
		targetKey,
	)
	if err != nil {
		t.Fatalf("start target on backed-off address: %v", err)
	}
	defer target.Close()

	normalCtx, normalCancel := context.WithTimeout(context.Background(), time.Second)
	err = dialer.Connect(normalCtx, info)
	normalCancel()
	if err == nil || !strings.Contains(err.Error(), "dial backoff") {
		t.Fatalf("ordinary dial error=%v, want dial backoff", err)
	}

	explicitCtx, explicitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer explicitCancel()
	if err := connectExplicitPeer(explicitCtx, dialer, info); err != nil {
		t.Fatalf("explicit dial did not bypass backoff: %v", err)
	}
}

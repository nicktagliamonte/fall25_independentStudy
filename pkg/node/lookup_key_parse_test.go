// Purpose: Unit tests for lookup-key bootstrap multiaddr parsing (comma-separated fallbacks).

package node

import (
	"testing"
)

func TestParseLookupKeyBootstrapPeers_commaSeparated(t *testing.T) {
	const pid = "12D3KooWJuDkPAajCkeNn64vpr8PwK5fbhKLGPCbvhCDUrTPbhCB"
	a := "/dns4/foo/tcp/4001/p2p/" + pid
	b := "/ip4/10.0.0.1/tcp/4001/p2p/" + pid
	infos, err := parseLookupKeyBootstrapPeers(a + "," + b)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("got %d infos, want 2", len(infos))
	}
}

func TestParseLookupKeyBootstrapPeers_single(t *testing.T) {
	const pid = "12D3KooWJuDkPAajCkeNn64vpr8PwK5fbhKLGPCbvhCDUrTPbhCB"
	infos, err := parseLookupKeyBootstrapPeers("/ip4/127.0.0.1/tcp/4001/p2p/" + pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Fatalf("got %d infos, want 1", len(infos))
	}
}

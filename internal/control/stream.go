// Purpose: Libp2p control protocol over a stream for remote control operations.

package control

import (
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

// ProtocolID is the libp2p protocol identifier that was formerly used for a
// peer-to-peer (stream-based) control protocol, as an alternative transport
// to the loopback HTTP control server in server.go. The stream-based
// protocol is not currently wired up (see StartStreamServer below); the
// constant and this file are retained only for future reference / possible
// reintroduction.
const ProtocolID = "/sng40/control/1.0.0"

// StartStreamServer is a placeholder for registering a libp2p stream
// handler (keyed on ProtocolID) that would let peers issue control
// operations directly over a libp2p stream instead of the local HTTP
// control server. It currently does nothing: h and stack are discarded via
// blank assignment, and no handler is actually registered on h via
// h.SetStreamHandler or similar. The `_ = network.Stream(nil)` line exists
// only to reference the network package (avoiding an unused-import error)
// and has no runtime effect. Callers should not rely on this function for
// any control-protocol behavior; use the HTTP server started by Start (in
// server.go) instead.
//
// h: the libp2p host that a real implementation would register a stream
// handler on. Unused.
// stack: the storage stack a real implementation would dispatch control
// operations against. Unused.
func StartStreamServer(h host.Host, stack *mystore.Stack) {
	_ = h
	_ = stack
	_ = network.Stream(nil)
}

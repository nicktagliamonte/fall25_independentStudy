// Purpose: Libp2p control protocol over a stream for remote control operations.

package control

import (
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

// removed from runtime; keeping file for future reference
const ProtocolID = "/sng40/control/1.0.0"

// StartStreamServer registers a stream handler for the control protocol.
func StartStreamServer(h host.Host, stack *mystore.Stack) {
	_ = h
	_ = stack
	_ = network.Stream(nil)
}

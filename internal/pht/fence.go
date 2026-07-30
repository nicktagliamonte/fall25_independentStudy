package pht

import "strings"

// WriteFence identifies the authority epoch and writer that produced a PHT
// record. Epochs fence owners from earlier leases; Writer deterministically
// resolves the rare case where multiple candidates concurrently claim the same
// epoch.
type WriteFence struct {
	Epoch  uint64 `json:"epoch,omitempty"`
	Writer string `json:"writer,omitempty"`
}

// CompareWriteFences returns -1 when a is older than b, 0 when they are equal,
// and 1 when a is newer. Within one epoch the lexicographically greater writer
// wins, matching the DHT record validator.
func CompareWriteFences(a, b WriteFence) int {
	if a.Epoch < b.Epoch {
		return -1
	}
	if a.Epoch > b.Epoch {
		return 1
	}
	return strings.Compare(a.Writer, b.Writer)
}

func stampWriteFence(n *Node, fence WriteFence) {
	if n == nil {
		return
	}
	n.Epoch = fence.Epoch
	n.Writer = fence.Writer
	for _, child := range n.Children {
		stampWriteFence(child, fence)
	}
}

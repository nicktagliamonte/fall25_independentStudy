// Purpose: IBLT-based CID set reconciliation (Phase 4.3). Wires catalog IBLT exchange
// with libp2p host and storage stack.

package node

import (
	"context"
	"time"

	bserv "github.com/ipfs/boxo/blockservice"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
	mysync "github.com/nicktagliamonte/fall25_independentStudy/internal/sync"
)

const (
	// catalogIBLTCellCount is the number of cells in each periodic catalog
	// IBLT snapshot; larger values tolerate bigger set differences before
	// Peel fails to fully recover the difference.
	catalogIBLTCellCount = 256
	// catalogIBLTInterval is how often this node initiates an IBLT exchange
	// with each connected neighbor.
	catalogIBLTInterval = 5 * time.Minute
	// catalogIBLTTimeout bounds a single IBLT exchange (stream open, write,
	// read) with one neighbor.
	catalogIBLTTimeout = 30 * time.Second
)

// catalogNeighborProvider implements mysync.NeighborProvider by returning the
// string-encoded peer IDs of all currently connected peers (excluding self).
type catalogNeighborProvider struct {
	h host.Host // libp2p host used to enumerate connected peers.
}

// Neighbors returns the string-encoded peer IDs of all peers this host is
// currently connected to, excluding its own peer ID. It implements
// mysync.NeighborProvider for the periodic IBLT exchange loop.
//
// Returns:
//   - []string: string-encoded peer IDs of connected neighbors.
func (p *catalogNeighborProvider) Neighbors() []string {
	peers := p.h.Network().Peers()
	out := make([]string, 0, len(peers))
	for _, pid := range peers {
		if pid != p.h.ID() && p.h.Network().Connectedness(pid) == network.Connected {
			out = append(out, pid.String())
		}
	}
	return out
}

// catalogIBLTStreamOpener implements mysync.IBLTStreamOpener by opening a
// libp2p stream on the IBLT exchange protocol to a given peer.
type catalogIBLTStreamOpener struct {
	h host.Host // libp2p host used to dial the IBLT protocol stream.
}

// OpenIBLTStream opens a new libp2p stream to peerID on mysync.IBLTProtocolID,
// decoding peerID from its string form first. It implements
// mysync.IBLTStreamOpener for the periodic IBLT exchange loop.
//
// Parameters:
//   - ctx (context.Context): bounds the stream-open attempt.
//   - peerID (string): string-encoded libp2p peer ID to connect to.
//
// Returns:
//   - mysync.IBLTStream: the opened bidirectional stream.
//   - error: non-nil if peerID cannot be decoded or the stream cannot be opened.
func (o *catalogIBLTStreamOpener) OpenIBLTStream(ctx context.Context, peerID string) (mysync.IBLTStream, error) {
	pid, err := peer.Decode(peerID)
	if err != nil {
		return nil, err
	}
	return o.h.NewStream(ctx, pid, mysync.IBLTProtocolID)
}

// catalogFetchRequester implements mysync.FetchRequester: given key hashes a
// peer has that we're missing (per IBLT reconciliation), it connects to the
// peer, asks it to resolve those key hashes to CIDs over the IBLT fetch
// protocol, and then fetches each returned block via the block service so it
// lands in the local store and provider records.
type catalogFetchRequester struct {
	h     host.Host           // libp2p host used to dial the peer and open the fetch stream.
	bsvc  *bserv.BlockService // block service used to fetch resolved blocks by CID.
	stack *mystore.Stack      // storage stack whose ProviderRecords are updated for fetched CIDs.
}

// RequestFetch resolves keyHashes to CIDs via peerID's IBLT fetch protocol
// responder, then fetches each resolved block through r.bsvc so it is stored
// locally, recording each fetched CID in r.stack.ProviderRecords. All errors
// are handled by returning early; this is a best-effort background repair
// path and has no return value to report failure through. It implements
// mysync.FetchRequester.
//
// Parameters:
//   - ctx (context.Context): parent context; a 60s timeout is derived from it for the whole operation.
//   - peerID (string): string-encoded peer ID of the neighbor that reported these key hashes as Negative (peer has, we don't).
//   - keyHashes ([]uint64): IBLT key hashes to resolve to CIDs and fetch.
func (r *catalogFetchRequester) RequestFetch(ctx context.Context, peerID string, keyHashes []uint64) {
	if len(keyHashes) == 0 || r.bsvc == nil || r.stack == nil {
		return
	}
	pid, err := peer.Decode(peerID)
	if err != nil {
		return
	}
	ctx2, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := r.h.Connect(ctx2, peer.AddrInfo{ID: pid, Addrs: r.h.Peerstore().Addrs(pid)}); err != nil {
		return
	}
	stream, err := r.h.NewStream(ctx2, pid, mysync.IBLTFetchProtocolID)
	if err != nil {
		return
	}
	defer stream.Close()
	if err := mysync.WriteFetchRequest(stream, keyHashes); err != nil {
		return
	}
	_ = stream.CloseWrite()
	cids, err := mysync.ReadFetchResponse(stream, 4096)
	if err != nil {
		return
	}
	for _, c := range cids {
		if !c.Defined() {
			continue
		}
		ctxGet, cancelGet := context.WithTimeout(ctx2, 15*time.Second)
		_, _ = (*r.bsvc).GetBlock(ctxGet, c)
		cancelGet()
		if r.stack.ProviderRecords != nil {
			r.stack.ProviderRecords.Add(c)
		}
	}
}

// CatalogIBLTOption customizes InstallCatalogIBLT's mysync.ExchangerConfig
// before periodic exchange starts (e.g. for tests that need a shorter
// interval).
type CatalogIBLTOption func(*mysync.ExchangerConfig)

// CatalogIBLTInterval returns a CatalogIBLTOption that overrides the periodic
// exchange interval (default catalogIBLTInterval) in the config passed to
// InstallCatalogIBLT.
//
// Parameters:
//   - d (time.Duration): the interval to use between IBLT exchange rounds.
//
// Returns:
//   - CatalogIBLTOption: an option that sets ExchangerConfig.Interval to d.
func CatalogIBLTInterval(d time.Duration) CatalogIBLTOption {
	return func(c *mysync.ExchangerConfig) { c.Interval = d }
}

// InstallCatalogIBLT wires this node into the IBLT-based catalog
// reconciliation protocol (internal/sync). It registers two libp2p stream
// handlers:
//   - mysync.IBLTProtocolID: on an inbound stream, reads the remote's IBLT,
//     builds a local IBLT snapshot from stack.ProviderRecords, and writes it
//     back (the passive side of a pairwise exchange).
//   - mysync.IBLTFetchProtocolID: on an inbound stream, reads requested key
//     hashes, resolves them against the local provider-record snapshot, and
//     writes back the matching CIDs (the passive side of key-hash-to-CID
//     resolution).
//
// It then starts mysync.StartPeriodicExchange, which actively initiates IBLT
// exchanges with connected neighbors (catalogNeighborProvider) on
// catalogIBLTInterval, using catalogIBLTStreamOpener to dial and
// catalogFetchRequester to fetch blocks the exchange reveals this node is
// missing. If stack or stack.ProviderRecords is nil, this is a no-op and
// returns a no-op stop function.
//
// Parameters:
//   - ctx (context.Context): governs the periodic exchange loop; canceling it also stops the loop.
//   - h (host.Host): libp2p host on which stream handlers are registered and exchanges are dialed.
//   - stack (*mystore.Stack): storage stack supplying the provider-record catalog and block service.
//   - opts (...CatalogIBLTOption): optional overrides applied to the exchange config (e.g. CatalogIBLTInterval).
//
// Returns:
//   - func(): a stop function that cancels the periodic exchange loop and waits for it to exit.
func InstallCatalogIBLT(ctx context.Context, h host.Host, stack *mystore.Stack, opts ...CatalogIBLTOption) func() {
	if stack == nil || stack.ProviderRecords == nil {
		return func() {}
	}
	records := stack.ProviderRecords
	bsvc := stack.BlockSvc

	h.SetStreamHandler(mysync.IBLTProtocolID, func(s network.Stream) {
		defer s.Close()
		if _, err := mysync.ReadIBLT(s); err != nil {
			return
		}
		local := mysync.BuildIBLTFromCIDs(records.Snapshot(), catalogIBLTCellCount)
		_ = mysync.WriteIBLT(s, local)
	})

	h.SetStreamHandler(mysync.IBLTFetchProtocolID, func(s network.Stream) {
		defer s.Close()
		keyHashes, err := mysync.ReadFetchRequest(s, 4096)
		if err != nil {
			return
		}
		cids := mysync.CIDsForKeyHashes(records.Snapshot(), keyHashes)
		_ = mysync.WriteFetchResponse(s, cids)
	})

	neighbors := &catalogNeighborProvider{h: h}
	opener := &catalogIBLTStreamOpener{h: h}
	fetcher := &catalogFetchRequester{h: h, bsvc: bsvc, stack: stack}
	buildLocal := func() *mysync.IBLT {
		return mysync.BuildIBLTFromCIDs(records.Snapshot(), catalogIBLTCellCount)
	}
	cfg := mysync.ExchangerConfig{
		Interval:       catalogIBLTInterval,
		CellCount:      catalogIBLTCellCount,
		Timeout:        catalogIBLTTimeout,
		FetchRequester: fetcher,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return mysync.StartPeriodicExchange(ctx, cfg, buildLocal, neighbors, opener, nil)
}

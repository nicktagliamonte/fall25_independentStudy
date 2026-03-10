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
	catalogIBLTCellCount  = 256
	catalogIBLTInterval   = 5 * time.Minute
	catalogIBLTTimeout    = 30 * time.Second
)

// catalogNeighborProvider returns connected peer IDs for IBLT exchange.
type catalogNeighborProvider struct {
	h host.Host
}

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

// catalogIBLTStreamOpener opens a stream for IBLT exchange.
type catalogIBLTStreamOpener struct {
	h host.Host
}

func (o *catalogIBLTStreamOpener) OpenIBLTStream(ctx context.Context, peerID string) (mysync.IBLTStream, error) {
	pid, err := peer.Decode(peerID)
	if err != nil {
		return nil, err
	}
	return o.h.NewStream(ctx, pid, mysync.IBLTProtocolID)
}

// catalogFetchRequester opens a fetch stream, requests CIDs for keyHashes, fetches blocks.
type catalogFetchRequester struct {
	h     host.Host
	bsvc  *bserv.BlockService
	stack *mystore.Stack
}

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

// CatalogIBLTOption customizes InstallCatalogIBLT behavior (e.g. for tests).
type CatalogIBLTOption func(*mysync.ExchangerConfig)

// CatalogIBLTInterval overrides the exchange interval.
func CatalogIBLTInterval(d time.Duration) CatalogIBLTOption {
	return func(c *mysync.ExchangerConfig) { c.Interval = d }
}

// InstallCatalogIBLT installs IBLT and fetch stream handlers and starts periodic exchange.
// Stops when ctx is done. Returns a stop function to cancel the exchange loop.
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

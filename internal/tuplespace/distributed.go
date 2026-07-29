// Purpose: Repository-native distributed tuple-space transport and ownership.
package tuplespace

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

const (
	// NativeTupleProtocolID is the libp2p protocol used by Tarsus tuple owners.
	NativeTupleProtocolID protocol.ID = "/tarsus/tuplespace/1.0.0"
	defaultTupleTimeout               = 10 * time.Second
	maxTupleRequestBytes              = 1 << 20
	handshakeVerifiedTag              = "handshake_ok"
)

// TupleOwnerResolver deterministically chooses the peer that serializes exact
// operations for a tuple name. Production resolvers should derive ownership
// from the shared DHT keyspace.
type TupleOwnerResolver interface {
	ResolveTupleOwner(ctx context.Context, tupleName string) (peer.ID, error)
}

type tupleWireRequest struct {
	Operation string `json:"operation"`
	Name      string `json:"name"`
	Value     []byte `json:"value,omitempty"`
}

type tupleWireResponse struct {
	Value []byte `json:"value,omitempty"`
	Error string `json:"error,omitempty"`
}

// DistributedTupleSpace routes exact operations to a deterministic owner. Each
// owner executes requests through NativeTupleSpace, making its mutex the
// per-owner serialization boundary. Associative operations currently query all
// reachable peers; the distributed index can supply a narrower candidate set
// without changing the ownership or consume protocol.
type DistributedTupleSpace struct {
	host                 host.Host
	resolver             TupleOwnerResolver
	local                *NativeTupleSpace
	timeout              time.Duration
	requireVerifiedPeers bool
}

// NewDistributedTupleSpace installs the native tuple protocol on h.
func NewDistributedTupleSpace(h host.Host, resolver TupleOwnerResolver) (*DistributedTupleSpace, error) {
	if h == nil {
		return nil, errors.New("host required")
	}
	if resolver == nil {
		return nil, errors.New("tuple owner resolver required")
	}
	d := &DistributedTupleSpace{
		host:     h,
		resolver: resolver,
		local:    NewNativeTupleSpace(),
		timeout:  defaultTupleTimeout,
	}
	h.SetStreamHandler(NativeTupleProtocolID, d.handleStream)
	return d, nil
}

// SetRequireVerifiedPeers makes tuple streams wait for the host's handshake
// gate to verify a newly dialed peer. Production nodes enable this after
// installing the gate; direct libp2p tests may leave it disabled.
func (d *DistributedTupleSpace) SetRequireVerifiedPeers(required bool) {
	d.requireVerifiedPeers = required
}

// Close removes the protocol handler. It does not close the shared libp2p host.
func (d *DistributedTupleSpace) Close() {
	if d != nil && d.host != nil {
		d.host.RemoveStreamHandler(NativeTupleProtocolID)
	}
}

func (d *DistributedTupleSpace) TsPut(name string, value []byte) (int, error) {
	if name == "" {
		return TSPUT_ER, errors.New("tuple name required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
	defer cancel()
	_, err := d.exact(ctx, tupleWireRequest{Operation: "put", Name: name, Value: value})
	if err != nil {
		return TSPUT_ER, err
	}
	return 0, nil
}

// TsReplace routes an exact-name singleton update to the same deterministic
// tuple owner used by Put, Read, and Get.
func (d *DistributedTupleSpace) TsReplace(name string, value []byte) (int, error) {
	if name == "" {
		return TSPUT_ER, errors.New("tuple name required")
	}
	if isTuplePattern(name) {
		return TSPUT_ER, errors.New("tuple replacement requires an exact name")
	}
	ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
	defer cancel()
	_, err := d.exact(ctx, tupleWireRequest{Operation: "replace", Name: name, Value: value})
	if err != nil {
		return TSPUT_ER, err
	}
	return 0, nil
}

func (d *DistributedTupleSpace) TsRead(expr string) ([]byte, error) {
	return d.TsReadContext(context.Background(), expr)
}

// TsReadContext is the context-aware read path used by indexed candidate
// verification so one query has one end-to-end deadline rather than a fresh
// timeout for every stale candidate.
func (d *DistributedTupleSpace) TsReadContext(parent context.Context, expr string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, d.timeout)
	defer cancel()
	if !isTuplePattern(expr) {
		return d.exact(ctx, tupleWireRequest{Operation: "read", Name: expr})
	}
	return d.associative(ctx, "read", expr)
}

func (d *DistributedTupleSpace) TsGet(expr string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
	defer cancel()
	if !isTuplePattern(expr) {
		return d.exact(ctx, tupleWireRequest{Operation: "get", Name: expr})
	}
	return d.associative(ctx, "get", expr)
}

func (d *DistributedTupleSpace) exact(ctx context.Context, req tupleWireRequest) ([]byte, error) {
	owner, err := d.resolver.ResolveTupleOwner(ctx, req.Name)
	if err != nil {
		return nil, fmt.Errorf("resolve tuple owner: %w", err)
	}
	if owner == "" {
		return nil, errors.New("tuple owner resolver returned an empty peer ID")
	}
	return d.requestPeer(ctx, owner, req)
}

// associative queries peers in stable peer-ID order. A consuming operation
// stops after the first owner atomically removes a match. Failures from one
// unreachable owner do not prevent trying the remaining reachable owners.
func (d *DistributedTupleSpace) associative(ctx context.Context, operation, expr string) ([]byte, error) {
	if _, err := compileTupleMatcher(expr); err != nil {
		return nil, err
	}
	peers := append([]peer.ID(nil), d.host.Network().Peers()...)
	peers = append(peers, d.host.ID())
	sort.Slice(peers, func(i, j int) bool { return peers[i].String() < peers[j].String() })

	var lastErr error
	for _, owner := range peers {
		value, err := d.requestPeer(ctx, owner, tupleWireRequest{
			Operation: operation,
			Name:      expr,
		})
		if err == nil {
			return value, nil
		}
		lastErr = err
	}
	if lastErr != nil && !errors.Is(lastErr, ErrTupleNotFound) {
		return nil, lastErr
	}
	return nil, ErrTupleNotFound
}

func (d *DistributedTupleSpace) requestPeer(ctx context.Context, owner peer.ID, req tupleWireRequest) ([]byte, error) {
	if owner == d.host.ID() {
		return d.applyLocal(req)
	}
	stream, err := openTuplePeerStream(ctx, d.host, owner, NativeTupleProtocolID, d.requireVerifiedPeers)
	if err != nil {
		return nil, fmt.Errorf("open tuple-owner stream: %w", err)
	}
	defer stream.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	}
	if err := json.NewEncoder(stream).Encode(req); err != nil {
		_ = stream.Reset()
		return nil, fmt.Errorf("write tuple request: %w", err)
	}
	var response tupleWireResponse
	if err := json.NewDecoder(io.LimitReader(stream, maxTupleRequestBytes)).Decode(&response); err != nil {
		return nil, fmt.Errorf("read tuple response: %w", err)
	}
	if response.Error != "" {
		if response.Error == ErrTupleNotFound.Error() {
			return nil, ErrTupleNotFound
		}
		return nil, errors.New(response.Error)
	}
	return response.Value, nil
}

func openTuplePeerStream(ctx context.Context, h host.Host, owner peer.ID, protocolID protocol.ID, requireVerified bool) (network.Stream, error) {
	info := peer.AddrInfo{ID: owner, Addrs: h.Peerstore().Addrs(owner)}
	if err := h.Connect(ctx, info); err != nil {
		return nil, fmt.Errorf("connect to peer: %w", err)
	}
	if requireVerified {
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			tagInfo := h.ConnManager().GetTagInfo(owner)
			if tagInfo != nil && tagInfo.Tags[handshakeVerifiedTag] > 0 {
				break
			}
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("wait for peer verification: %w", ctx.Err())
			case <-ticker.C:
			}
		}
	}
	return h.NewStream(ctx, owner, protocolID)
}

func (d *DistributedTupleSpace) handleStream(stream network.Stream) {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(d.timeout))
	var req tupleWireRequest
	decoder := json.NewDecoder(bufio.NewReader(io.LimitReader(stream, maxTupleRequestBytes)))
	if err := decoder.Decode(&req); err != nil {
		_ = json.NewEncoder(stream).Encode(tupleWireResponse{Error: "decode tuple request: " + err.Error()})
		return
	}
	value, err := d.applyLocal(req)
	response := tupleWireResponse{Value: value}
	if err != nil {
		response.Error = err.Error()
	}
	_ = json.NewEncoder(stream).Encode(response)
}

func (d *DistributedTupleSpace) applyLocal(req tupleWireRequest) ([]byte, error) {
	switch req.Operation {
	case "put":
		_, err := d.local.TsPut(req.Name, req.Value)
		return nil, err
	case "replace":
		_, err := d.local.TsReplace(req.Name, req.Value)
		return nil, err
	case "read":
		return d.local.TsRead(req.Name)
	case "get":
		return d.local.TsGet(req.Name)
	default:
		return nil, fmt.Errorf("unsupported tuple operation %q", req.Operation)
	}
}

func isTuplePattern(expr string) bool {
	const regexMeta = `*.+?^$[]{}|()\`
	for _, char := range regexMeta {
		for _, exprChar := range expr {
			if exprChar == char {
				return true
			}
		}
	}
	return false
}

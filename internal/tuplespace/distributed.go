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
	host     host.Host
	resolver TupleOwnerResolver
	local    *NativeTupleSpace
	timeout  time.Duration
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

func (d *DistributedTupleSpace) TsRead(expr string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
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
	stream, err := d.host.NewStream(ctx, owner, NativeTupleProtocolID)
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

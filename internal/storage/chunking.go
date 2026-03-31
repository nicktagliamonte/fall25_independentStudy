// Purpose: Chunk-indexed payload storage and reassembly for key-based retrieval.

package storage

import (
	"context"
	"encoding/json"
	"fmt"

	bserv "github.com/ipfs/boxo/blockservice"
	ds "github.com/ipfs/go-datastore"
)

const chunkIndexNS = "/chunk/index/"

// DefaultContentChunkSize is the fixed payload chunk size used for chunked storage.
const DefaultContentChunkSize = 4 * 1024

// ChunkIndex stores payload chunk metadata keyed by the logical payload key.
type ChunkIndex struct {
	Version    int      `json:"version"`
	ChunkSize  int      `json:"chunk_size"`
	TotalBytes int      `json:"total_bytes"`
	ChunkKeys  []string `json:"chunk_keys"`
}

// SplitPayloadChunks splits payload bytes into fixed-size chunks.
func SplitPayloadChunks(data []byte, chunkSize int) [][]byte {
	if chunkSize <= 0 {
		chunkSize = DefaultContentChunkSize
	}
	if len(data) == 0 {
		return [][]byte{}
	}
	out := make([][]byte, 0, (len(data)+chunkSize-1)/chunkSize)
	for start := 0; start < len(data); start += chunkSize {
		end := start + chunkSize
		if end > len(data) {
			end = len(data)
		}
		chunk := make([]byte, end-start)
		copy(chunk, data[start:end])
		out = append(out, chunk)
	}
	return out
}

// StoreChunkIndex stores a chunk index for the logical payload key.
func StoreChunkIndex(ctx context.Context, d ds.Batching, key Key, idx ChunkIndex) error {
	if d == nil || key.IsZero() {
		return nil
	}
	raw, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	return d.Put(ctx, ds.NewKey(chunkIndexNS+key.String()), raw)
}

// GetChunkIndex retrieves a chunk index for a key. Returns (nil, nil) when absent.
func GetChunkIndex(ctx context.Context, d ds.Batching, key Key) (*ChunkIndex, error) {
	if d == nil || key.IsZero() {
		return nil, nil
	}
	raw, err := d.Get(ctx, ds.NewKey(chunkIndexNS+key.String()))
	if err != nil {
		return nil, nil
	}
	var idx ChunkIndex
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, err
	}
	if idx.Version <= 0 || idx.TotalBytes < 0 || len(idx.ChunkKeys) == 0 {
		return nil, fmt.Errorf("invalid chunk index for key %s", key.String())
	}
	return &idx, nil
}

// ResolvePayloadByKeyLocal resolves a logical key from local storage.
// It first tries direct single-block key mapping, then chunk-index reassembly.
func ResolvePayloadByKeyLocal(ctx context.Context, d ds.Batching, bsvc *bserv.BlockService, key Key) ([]byte, error) {
	blockData, err := GetBlockByKey(ctx, d, bsvc, key)
	if err == nil && blockData != nil {
		return blockData, nil
	}
	idx, err := GetChunkIndex(ctx, d, key)
	if err != nil {
		return nil, err
	}
	if idx == nil {
		return nil, nil
	}

	out := make([]byte, 0, idx.TotalBytes)
	for i := range idx.ChunkKeys {
		chunkKey, perr := ParseKey(idx.ChunkKeys[i])
		if perr != nil {
			return nil, fmt.Errorf("parse chunk key[%d]: %w", i, perr)
		}
		chunkData, gerr := GetBlockByKey(ctx, d, bsvc, chunkKey)
		if gerr != nil {
			return nil, fmt.Errorf("load chunk[%d]: %w", i, gerr)
		}
		if chunkData == nil {
			return nil, fmt.Errorf("missing chunk[%d]", i)
		}
		out = append(out, chunkData...)
	}
	if len(out) != idx.TotalBytes {
		return nil, fmt.Errorf("reassembled payload size mismatch: got=%d want=%d", len(out), idx.TotalBytes)
	}
	if !KeyFromData(out).Equal(key) {
		return nil, fmt.Errorf("reassembled payload key mismatch")
	}
	return out, nil
}

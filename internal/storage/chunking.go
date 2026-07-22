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
	// Version is the chunk index format version; must be > 0 to be considered valid.
	Version int `json:"version"`
	// ChunkSize is the fixed chunk size (in bytes) used when splitting the payload.
	ChunkSize int `json:"chunk_size"`
	// TotalBytes is the total length of the original, unchunked payload.
	TotalBytes int `json:"total_bytes"`
	// ChunkKeys is the ordered list of hex-encoded Keys for each chunk, in the
	// order they must be concatenated to reassemble the original payload.
	ChunkKeys []string `json:"chunk_keys"`
}

// SplitPayloadChunks splits payload bytes into fixed-size chunks.
//
// Parameters:
//   - data ([]byte): the payload to split.
//   - chunkSize (int): the maximum size of each chunk; values <= 0 fall back to
//     DefaultContentChunkSize.
//
// Returns:
//   - [][]byte: the ordered chunks (each a fresh copy of the underlying bytes);
//     an empty (non-nil) slice if data is empty. The final chunk may be smaller
//     than chunkSize.
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
//
// Parameters:
//   - ctx (context.Context): controls cancellation/timeout of the datastore write.
//   - d (ds.Batching): the backing datastore; if nil, this is a silent no-op.
//   - key (Key): the logical payload key; if zero, this is a silent no-op.
//   - idx (ChunkIndex): the chunk index metadata to persist.
//
// Returns:
//   - error: non-nil if JSON marshaling or the datastore Put fails.
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
//
// Parameters:
//   - ctx (context.Context): controls cancellation/timeout of the datastore read.
//   - d (ds.Batching): the backing datastore; if nil, returns (nil, nil).
//   - key (Key): the logical payload key; if zero, returns (nil, nil).
//
// Returns:
//   - *ChunkIndex: the stored chunk index, or nil if not present, not readable,
//     or if d/key are invalid.
//   - error: non-nil only if the stored record exists but fails to unmarshal
//     or fails basic sanity checks (Version > 0, TotalBytes >= 0, non-empty ChunkKeys).
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
// It first tries direct single-block key mapping (GetBlockByKey), and if that
// misses, falls back to chunk-index reassembly: loading the ChunkIndex for key,
// fetching each listed chunk in order, concatenating them, and verifying the
// reassembled payload's length and SHA256 hash match the index/key.
//
// Parameters:
//   - ctx (context.Context): controls cancellation/timeout of the underlying reads.
//   - d (ds.Batching): the backing datastore, used for chunk index lookup.
//   - bsvc (*bserv.BlockService): the block service used to fetch block bytes by key.
//   - key (Key): the logical payload key to resolve.
//
// Returns:
//   - []byte: the resolved payload bytes, or nil if neither a direct block nor a
//     chunk index exists for key.
//   - error: non-nil if a chunk key fails to parse, a chunk is missing/unreadable,
//     the reassembled size doesn't match the index's TotalBytes, or the
//     reassembled payload's hash doesn't match key.
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

// Purpose: HTTP handlers for first-class directory namespace (mkdir, links, ls, resolve).

package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/host"

	"github.com/nicktagliamonte/fall25_independentStudy/internal/directory"
	mystore "github.com/nicktagliamonte/fall25_independentStudy/internal/storage"
)

// registerNamespaceHandlers registers the /namespace/* HTTP routes (mkdir,
// link, unlink, rename, ls, resolve) on mux. It is called once from
// control.Start alongside the other endpoint registrations. All mutating
// routes (mkdir/link/unlink/rename) follow the same copy-on-write pattern:
// load or create a Directory, produce a modified clone, Encode it, store it
// via putNamespaceBlock, and return the new directory key.
//
// Parameters:
//   - mux (*http.ServeMux): the server mux to register routes on.
//   - stack (*mystore.Stack): storage stack used to load/store directory blocks.
//   - h (host.Host): libp2p host used to update the routing table after each put (may be nil).
//   - repairProtocol (*mystore.RepairProtocol): optional protocol used to replicate newly stored directory blocks to peers (nil disables replication).
//
// Returns: (none — routes are registered on mux as a side effect)
func registerNamespaceHandlers(mux *http.ServeMux, stack *mystore.Stack, h host.Host, repairProtocol *mystore.RepairProtocol) {
	// POST /namespace/mkdir creates a new, empty directory block and returns
	// its key. Request body is ignored (may be empty or "{}").
	//
	// Parameters:
	//   - w (http.ResponseWriter): writes {"dir_key", "cid"} JSON on success.
	//   - r (*http.Request): must be a POST; body is unused.
	//
	// Returns: (none — writes directly to w)
	mux.HandleFunc("/namespace/mkdir", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		d := directory.New()
		raw, err := d.Encode()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		key, c, err := putNamespaceBlock(r.Context(), stack, h, repairProtocol, raw)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeNamespaceJSON(w, http.StatusOK, map[string]string{"dir_key": key.String(), "cid": c.String()})
	})

	// POST /namespace/link adds or replaces the entry named req.Name under
	// the directory req.DirKey, pointing it at req.ChildKey, and returns the
	// new (copy-on-write) directory key. The original directory block at
	// req.DirKey is left unchanged.
	//
	// Parameters:
	//   - w (http.ResponseWriter): writes {"dir_key", "cid"} JSON on success.
	//   - r (*http.Request): must be a POST with JSON body {"dir_key", "name", "child_key"}.
	//
	// Returns: (none — writes directly to w)
	mux.HandleFunc("/namespace/link", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			DirKey   string `json:"dir_key"`
			Name     string `json:"name"`
			ChildKey string `json:"child_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		d, err := loadDirectory(r.Context(), stack, req.DirKey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		d2 := d.Clone()
		if err := d2.AddLink(req.Name, req.ChildKey); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		raw, err := d2.Encode()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		key, c, err := putNamespaceBlock(r.Context(), stack, h, repairProtocol, raw)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeNamespaceJSON(w, http.StatusOK, map[string]string{"dir_key": key.String(), "cid": c.String()})
	})

	// POST /namespace/unlink removes the entry named req.Name from the
	// directory req.DirKey and returns the new (copy-on-write) directory key.
	// Returns 404 if the directory itself, or the named entry within it, does
	// not exist.
	//
	// Parameters:
	//   - w (http.ResponseWriter): writes {"dir_key", "cid"} JSON on success.
	//   - r (*http.Request): must be a POST with JSON body {"dir_key", "name"}.
	//
	// Returns: (none — writes directly to w)
	mux.HandleFunc("/namespace/unlink", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			DirKey string `json:"dir_key"`
			Name   string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		d, err := loadDirectory(r.Context(), stack, req.DirKey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if _, ok := d.Entries[req.Name]; !ok {
			http.Error(w, "name not found", http.StatusNotFound)
			return
		}
		d2 := d.Clone()
		if err := d2.RemoveLink(req.Name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		raw, err := d2.Encode()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		key, c, err := putNamespaceBlock(r.Context(), stack, h, repairProtocol, raw)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeNamespaceJSON(w, http.StatusOK, map[string]string{"dir_key": key.String(), "cid": c.String()})
	})

	// POST /namespace/rename moves the entry req.OldName to req.NewName
	// within the directory req.DirKey (removing the old name and re-adding
	// the same child key under the new name), returning the new
	// (copy-on-write) directory key. Returns 404 if the directory or
	// req.OldName does not exist.
	//
	// Parameters:
	//   - w (http.ResponseWriter): writes {"dir_key", "cid"} JSON on success.
	//   - r (*http.Request): must be a POST with JSON body {"dir_key", "old_name", "new_name"}.
	//
	// Returns: (none — writes directly to w)
	mux.HandleFunc("/namespace/rename", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			DirKey  string `json:"dir_key"`
			OldName string `json:"old_name"`
			NewName string `json:"new_name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		d, err := loadDirectory(r.Context(), stack, req.DirKey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		child, ok := d.Entries[req.OldName]
		if !ok {
			http.Error(w, "old_name not found", http.StatusNotFound)
			return
		}
		d2 := d.Clone()
		if err := d2.RemoveLink(req.OldName); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := d2.AddLink(req.NewName, child); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		raw, err := d2.Encode()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		key, c, err := putNamespaceBlock(r.Context(), stack, h, repairProtocol, raw)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeNamespaceJSON(w, http.StatusOK, map[string]string{"dir_key": key.String(), "cid": c.String()})
	})

	// POST /namespace/ls loads the directory req.DirKey and returns its
	// sorted entry names plus the full name→child-key map. It is read-only
	// and does not create a new directory block.
	//
	// Parameters:
	//   - w (http.ResponseWriter): writes {"dir_key", "names", "entries"} JSON on success.
	//   - r (*http.Request): must be a POST with JSON body {"dir_key"}.
	//
	// Returns: (none — writes directly to w)
	mux.HandleFunc("/namespace/ls", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			DirKey string `json:"dir_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		d, err := loadDirectory(r.Context(), stack, req.DirKey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		out := map[string]any{
			"dir_key": req.DirKey,
			"names":   d.List(),
			"entries": d.Entries,
		}
		writeNamespaceJSON(w, http.StatusOK, out)
	})

	// POST /namespace/resolve walks req.Path (a "/"-separated relative path)
	// starting from req.RootKey via directory.ResolvePath, using
	// stack.GetBlock to fetch each intermediate directory block, and returns
	// the key of the final resolved object (file or directory). An empty
	// path returns req.RootKey unchanged.
	//
	// Parameters:
	//   - w (http.ResponseWriter): writes {"key"} JSON on success.
	//   - r (*http.Request): must be a POST with JSON body {"root_key", "path"}.
	//
	// Returns: (none — writes directly to w)
	mux.HandleFunc("/namespace/resolve", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			RootKey string `json:"root_key"`
			Path    string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		root, err := mystore.ParseKey(req.RootKey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		key, err := directory.ResolvePath(root, req.Path, func(k mystore.Key) ([]byte, error) {
			b, _, err := stack.GetBlock(r.Context(), k)
			return b, err
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeNamespaceJSON(w, http.StatusOK, map[string]string{"key": key.String()})
	})
}

// putNamespaceBlock stores an encoded directory block through the normal
// block-storage pipeline: PutBlock into the local store, local routing
// metadata, and (if repairProtocol and h are non-nil) asynchronous replication
// to ReplicationFactorR-1 other peers with aggregate provider publication.
// Including the local copy, this mirrors the /put handler's total target.
//
// Parameters:
//   - ctx (context.Context): context for the synchronous PutBlock call.
//   - stack (*mystore.Stack): storage stack to put the block into.
//   - h (host.Host): libp2p host used for the routing-table update and as the local peer ID for replication (nil skips both).
//   - repairProtocol (*mystore.RepairProtocol): optional; when non-nil (and h non-nil and data non-empty) replication is scheduled asynchronously.
//   - data ([]byte): the encoded directory block bytes (from Directory.Encode).
//
// Returns:
//   - (mystore.Key): the content key of the stored block.
//   - (cid.Cid): the IPFS-compatible CID of the stored block.
//   - (error): non-nil if the underlying PutBlock call fails.
func putNamespaceBlock(ctx context.Context, stack *mystore.Stack, h host.Host, repairProtocol *mystore.RepairProtocol, data []byte) (mystore.Key, cid.Cid, error) {
	key, c, err := stack.PutBlock(ctx, data)
	if err != nil {
		return mystore.Key{}, cid.Cid{}, err
	}
	if h != nil {
		if repairProtocol != nil {
			stack.RecordLocalPut(key, h.ID(), nil, c)
		} else {
			_ = stack.UpdateRoutingTableOnPutAsync(key, h.ID(), nil, c)
		}
	}
	if repairProtocol != nil && h != nil && len(data) > 0 {
		go func() {
			ctxRepair, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			_ = repairProtocol.ReplicateToNPeers(ctxRepair, key, c, data, ReplicationFactorR-1)
		}()
	}
	return key, c, nil
}

// loadDirectory parses dirKeyHex as a content key, fetches the corresponding
// block from local storage, and decodes it as a directory.Directory.
//
// Parameters:
//   - ctx (context.Context): context for the block fetch.
//   - stack (*mystore.Stack): storage stack to read the block from.
//   - dirKeyHex (string): the 64-hex-char content key of the directory block.
//
// Returns:
//   - (*directory.Directory): the decoded directory, or nil on error.
//   - (error): non-nil if dirKeyHex is not a valid key, the block is missing/empty, or decoding fails (wrong kind or invalid JSON).
func loadDirectory(ctx context.Context, stack *mystore.Stack, dirKeyHex string) (*directory.Directory, error) {
	k, err := mystore.ParseKey(dirKeyHex)
	if err != nil {
		return nil, err
	}
	data, err := mystore.GetBlockByKey(ctx, stack.Datastore, stack.BlockSvc, k)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("directory not found")
	}
	return directory.Decode(data)
}

// writeNamespaceJSON writes a JSON-encoded response with the given status
// code and Content-Type: application/json, used by all /namespace/* handlers
// to shape their success responses.
//
// Parameters:
//   - w (http.ResponseWriter): the response writer.
//   - status (int): the HTTP status code to write.
//   - v (any): the value to JSON-encode as the response body.
//
// Returns: (none — writes directly to w)
func writeNamespaceJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

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

func registerNamespaceHandlers(mux *http.ServeMux, stack *mystore.Stack, h host.Host, repairProtocol *mystore.RepairProtocol) {
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

func putNamespaceBlock(ctx context.Context, stack *mystore.Stack, h host.Host, repairProtocol *mystore.RepairProtocol, data []byte) (mystore.Key, cid.Cid, error) {
	key, c, err := stack.PutBlock(ctx, data)
	if err != nil {
		return mystore.Key{}, cid.Cid{}, err
	}
	if h != nil {
		stack.UpdateRoutingTableOnPutAsync(key, h.ID(), nil, c)
	}
	if repairProtocol != nil && h != nil && len(data) > 0 {
		go func() {
			ctxRepair, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			_ = repairProtocol.ReplicateToNPeers(ctxRepair, key, c, data, ReplicationFactorR)
		}()
	}
	return key, c, nil
}

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

func writeNamespaceJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

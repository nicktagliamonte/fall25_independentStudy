// Purpose: Local routing table storing [Key, Providers, RepVector] per planTwo.
// Key is the primary identifier (hash of data). CID is kept for IPFS blockstore compatibility.
// Supports multiple providers per key.

package storage

import (
	"sync"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/peer"
)

// ProviderInfo holds provider metadata for a key, including distance category.
type ProviderInfo struct {
	ProviderID       peer.ID
	DistanceCategory DistanceCategory
	AddedAt          time.Time
}

// RoutingTableEntry represents a single entry in the local routing table.
// Per planTwo: [Key, Providers, RepVector]. Key is primary identifier.
type RoutingTableEntry struct {
	// Key is the primary identifier (hash of data).
	Key Key
	// CID is kept for IPFS blockstore compatibility (derived from data).
	CID cid.Cid
	// Providers is the list of providers for this key (multiple providers per key).
	Providers []ProviderInfo
	// RepVector is the replication vector specifying N/M/F distribution for this key.
	RepVector ReplicationVector
}

// RoutingTable stores local routing table entries mapping Keys to provider information
// and replication vectors. Thread-safe.
type RoutingTable struct {
	mu      sync.RWMutex
	entries map[string]*RoutingTableEntry
}

// NewRoutingTable creates an empty routing table.
func NewRoutingTable() *RoutingTable {
	return &RoutingTable{
		entries: make(map[string]*RoutingTableEntry),
	}
}

// Set stores or updates a routing table entry for the given Key.
// If the Key already exists, merges the provider into Providers (if not already present) and updates RepVector.
// Key is the primary identifier; CID is optional (for IPFS compatibility).
func (rt *RoutingTable) Set(k Key, providerID peer.ID, repVector ReplicationVector, c cid.Cid) {
	if k.IsZero() {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	keyStr := k.String()
	entry, exists := rt.entries[keyStr]
	info := ProviderInfo{ProviderID: providerID, DistanceCategory: DistanceMidrange, AddedAt: time.Now()}
	if exists {
		providerExists := false
		for _, p := range entry.Providers {
			if p.ProviderID == providerID {
				providerExists = true
				break
			}
		}
		if !providerExists {
			entry.Providers = append(entry.Providers, info)
		}
		entry.RepVector = repVector
		if c.Defined() {
			entry.CID = c
		}
	} else {
		rt.entries[keyStr] = &RoutingTableEntry{
			Key:       k,
			CID:       c,
			Providers: []ProviderInfo{info},
			RepVector: repVector,
		}
	}
}

// Get retrieves the routing table entry for the given Key.
// Returns nil if the Key is not found.
func (rt *RoutingTable) Get(k Key) *RoutingTableEntry {
	if k.IsZero() {
		return nil
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return rt.entries[k.String()]
}

// GetByCID retrieves the routing table entry by CID (compatibility; prefer Get by Key).
// This is a compatibility method for IPFS operations. Returns nil if not found.
// Note: This requires iterating through entries, so it's less efficient than Get(Key).
func (rt *RoutingTable) GetByCID(c cid.Cid) *RoutingTableEntry {
	if !c.Defined() {
		return nil
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	for _, entry := range rt.entries {
		if entry.CID.Defined() && entry.CID.Equals(c) {
			return entry
		}
	}
	return nil
}

// Remove deletes the routing table entry for the given Key.
func (rt *RoutingTable) Remove(k Key) {
	if k.IsZero() {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	delete(rt.entries, k.String())
}

// RemoveByCID deletes the routing table entry for the given CID.
// This is a compatibility method for IPFS operations.
// Note: This requires iterating through entries, so it's less efficient than Remove(Key).
func (rt *RoutingTable) RemoveByCID(c cid.Cid) {
	if !c.Defined() {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for keyStr, entry := range rt.entries {
		if entry.CID.Defined() && entry.CID.Equals(c) {
			delete(rt.entries, keyStr)
			return
		}
	}
}

// UpdateRepVector updates only the replication vector for the given Key.
// No-op if the Key is not in the table.
func (rt *RoutingTable) UpdateRepVector(k Key, repVector ReplicationVector) {
	if k.IsZero() {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	entry, ok := rt.entries[k.String()]
	if ok {
		entry.RepVector = repVector
	}
}

// UpdateProviderID replaces the provider list with a single provider. Use AddProvider to append.
// No-op if the Key is not in the table.
func (rt *RoutingTable) UpdateProviderID(k Key, providerID peer.ID) {
	if k.IsZero() {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	entry, ok := rt.entries[k.String()]
	if ok {
		entry.Providers = []ProviderInfo{{ProviderID: providerID, DistanceCategory: DistanceMidrange, AddedAt: time.Now()}}
	}
}

// AddProvider adds a provider to the list of providers for the given Key with the specified distance category.
// If the Key already exists, appends the provider if not already present.
// If the Key does not exist, creates a new entry with the provider and default replication vector.
func (rt *RoutingTable) AddProvider(k Key, providerID peer.ID, category DistanceCategory) {
	if k.IsZero() {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	keyStr := k.String()
	entry, exists := rt.entries[keyStr]
	info := ProviderInfo{ProviderID: providerID, DistanceCategory: category, AddedAt: time.Now()}
	if exists {
		for _, p := range entry.Providers {
			if p.ProviderID == providerID {
				return
			}
		}
		entry.Providers = append(entry.Providers, info)
	} else {
		rt.entries[keyStr] = &RoutingTableEntry{
			Key:       k,
			CID:       cid.Cid{},
			Providers: []ProviderInfo{info},
			RepVector: DefaultReplicationVector(),
		}
	}
}

// RemoveProvider removes a provider from the list of providers for the given Key.
// If the Key does not exist or the provider is not in the list, this is a no-op.
func (rt *RoutingTable) RemoveProvider(k Key, providerID peer.ID) {
	if k.IsZero() {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	keyStr := k.String()
	entry, exists := rt.entries[keyStr]
	if !exists {
		return
	}
	for i, p := range entry.Providers {
		if p.ProviderID == providerID {
			entry.Providers = append(entry.Providers[:i], entry.Providers[i+1:]...)
			return
		}
	}
}

// GetProviders returns the list of providers for the given Key.
// Returns an empty slice if the Key does not exist.
// The returned slice is a copy to prevent external mutation.
func (rt *RoutingTable) GetProviders(k Key) []ProviderInfo {
	if k.IsZero() {
		return nil
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	entry, exists := rt.entries[k.String()]
	if !exists {
		return nil
	}
	providers := make([]ProviderInfo, len(entry.Providers))
	copy(providers, entry.Providers)
	return providers
}

// GetProvidersByCategory returns peer IDs of providers matching the given distance category for the Key.
// Returns an empty slice if the Key does not exist or no providers match.
func (rt *RoutingTable) GetProvidersByCategory(k Key, category DistanceCategory) []peer.ID {
	if k.IsZero() {
		return nil
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	entry, exists := rt.entries[k.String()]
	if !exists {
		return nil
	}
	var out []peer.ID
	for _, p := range entry.Providers {
		if p.DistanceCategory == category {
			out = append(out, p.ProviderID)
		}
	}
	return out
}

// Len returns the number of entries in the routing table.
func (rt *RoutingTable) Len() int {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return len(rt.entries)
}

// Snapshot returns a copy of all routing table entries for iteration.
func (rt *RoutingTable) Snapshot() []*RoutingTableEntry {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	out := make([]*RoutingTableEntry, 0, len(rt.entries))
	for _, entry := range rt.entries {
		out = append(out, entry)
	}
	return out
}

// Has returns true if the routing table contains an entry for the given Key.
func (rt *RoutingTable) Has(k Key) bool {
	if k.IsZero() {
		return false
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	_, ok := rt.entries[k.String()]
	return ok
}

// HasByCID returns true if the routing table contains an entry for the CID (compatibility).
// This is a compatibility method for IPFS operations.
func (rt *RoutingTable) HasByCID(c cid.Cid) bool {
	if !c.Defined() {
		return false
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	for _, entry := range rt.entries {
		if entry.CID.Defined() && entry.CID.Equals(c) {
			return true
		}
	}
	return false
}

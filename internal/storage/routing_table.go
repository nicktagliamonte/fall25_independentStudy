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
	// ProviderID is the peer ID of the provider.
	ProviderID peer.ID
	// DistanceCategory is the classified distance of this provider (Near/Midrange/Far-flung).
	DistanceCategory DistanceCategory
	// AddedAt is when this provider was added to the entry.
	AddedAt time.Time
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
//
// Returns:
//   - *RoutingTable: a new, empty routing table ready for use.
func NewRoutingTable() *RoutingTable {
	return &RoutingTable{
		entries: make(map[string]*RoutingTableEntry),
	}
}

// Set stores or updates a routing table entry for the given Key.
// If the Key already exists, merges providerID into Providers (if not already
// present, tagged with DistanceMidrange) and overwrites RepVector; c only
// overwrites the entry's CID if c.Defined() is true. If the Key does not
// exist, creates a new entry with providerID as its sole provider.
//
// Parameters:
//   - k (Key): the content key; a zero key makes this a no-op.
//   - providerID (peer.ID): the provider to associate with k.
//   - repVector (ReplicationVector): the replication vector to store/update for k.
//   - c (cid.Cid): the CID to associate with k, for IPFS compatibility; ignored
//     if undefined and the entry already exists (a brand-new entry stores it
//     regardless of whether it's defined).
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

// Get retrieves the routing table entry for the given Key. This is an O(1) lookup.
//
// Parameters:
//   - k (Key): the content key to look up.
//
// Returns:
//   - *RoutingTableEntry: the entry for k, or nil if k is zero or not found.
//     The returned pointer aliases the table's internal storage; callers must
//     not mutate it without holding external synchronization.
func (rt *RoutingTable) Get(k Key) *RoutingTableEntry {
	if k.IsZero() {
		return nil
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return rt.entries[k.String()]
}

// GetByCID retrieves the routing table entry by CID (compatibility; prefer Get by Key).
// This is a compatibility method for IPFS operations.
// Note: This requires iterating through entries, so it's less efficient (O(n)) than Get(Key).
//
// Parameters:
//   - c (cid.Cid): the CID to look up.
//
// Returns:
//   - *RoutingTableEntry: the first entry whose CID equals c, or nil if c is
//     undefined or no entry matches.
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
//
// Parameters:
//   - k (Key): the content key to remove; a zero key makes this a no-op.
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
// Note: This requires iterating through entries, so it's less efficient (O(n)) than Remove(Key).
//
// Parameters:
//   - c (cid.Cid): the CID whose entry should be removed; a no-op if undefined
//     or no entry matches.
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
//
// Parameters:
//   - k (Key): the content key to update; a zero key makes this a no-op.
//   - repVector (ReplicationVector): the new replication vector to store.
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

// UpdateProviderID replaces the entire provider list for the given Key with a
// single provider (tagged DistanceMidrange). Use AddProvider to append to the
// existing list instead of replacing it. No-op if the Key is not in the table.
//
// Parameters:
//   - k (Key): the content key to update; a zero key makes this a no-op.
//   - providerID (peer.ID): the sole provider to set for k.
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
// If the Key already exists, appends the provider if not already present (existing
// entries are left untouched, including their DistanceCategory, if the provider is
// already listed). If the Key does not exist, creates a new entry with this
// provider and the default replication vector.
//
// Parameters:
//   - k (Key): the content key; a zero key makes this a no-op.
//   - providerID (peer.ID): the provider to add.
//   - category (DistanceCategory): the distance classification to record for a
//     newly-added provider.
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
//
// Parameters:
//   - k (Key): the content key; a zero key makes this a no-op.
//   - providerID (peer.ID): the provider to remove from k's entry.
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
//
// Parameters:
//   - k (Key): the content key to look up.
//
// Returns:
//   - []ProviderInfo: a copy of the entry's providers, or nil if k is zero or not found.
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
//
// Parameters:
//   - k (Key): the content key to look up.
//   - category (DistanceCategory): the distance category to filter providers by.
//
// Returns:
//   - []peer.ID: peer IDs of providers in the entry whose DistanceCategory
//     equals category; nil if k is zero, not found, or no providers match.
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
//
// Returns:
//   - int: the current number of keys tracked by the table.
func (rt *RoutingTable) Len() int {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return len(rt.entries)
}

// Snapshot returns a copy of all routing table entries for iteration.
// The slice itself is a fresh copy, but its elements are the same
// *RoutingTableEntry pointers stored internally, so mutating a returned entry
// mutates the live table without going through the table's lock.
//
// Returns:
//   - []*RoutingTableEntry: all current entries in unspecified order.
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
//
// Parameters:
//   - k (Key): the content key to check.
//
// Returns:
//   - bool: true if k is non-zero and present in the table.
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
//
// Parameters:
//   - c (cid.Cid): the CID to check.
//
// Returns:
//   - bool: true if c is defined and some entry's CID equals it.
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

// Purpose: ASN resolution via Team Cymru DNS for eclipse attack mitigation.

package net

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	cymruV4Zone  = "origin.asn.cymru.com"
	cymruV6Zone  = "origin6.asn.cymru.com"
	asnCacheTTL  = time.Hour
	asnCacheSize = 1024
)

type asnCacheEntry struct {
	asn uint32
	exp time.Time
}

// CymruASNResolver resolves ASN for public IPs using Team Cymru DNS (origin.asn.cymru.com
// and origin6.asn.cymru.com). Loopback, link-local, and private IPs return (0, false).
// Implements ASNResolver for use with EclipseLimiter.
type CymruASNResolver struct {
	mu    sync.RWMutex
	cache map[string]asnCacheEntry
	now   func() time.Time
}

// NewCymruASNResolver creates a resolver that uses Team Cymru's free DNS-based ASN lookup.
func NewCymruASNResolver() *CymruASNResolver {
	return &CymruASNResolver{
		cache: make(map[string]asnCacheEntry, asnCacheSize),
		now:   time.Now,
	}
}

// ResolveASN implements ASNResolver. Returns (0, false) for private/loopback IPs or lookup failure.
func (r *CymruASNResolver) ResolveASN(ctx context.Context, ip net.IP) (uint32, bool) {
	if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsPrivate() {
		return 0, false
	}
	key := ip.String()

	r.mu.RLock()
	if e, ok := r.cache[key]; ok && r.now().Before(e.exp) {
		r.mu.RUnlock()
		return e.asn, e.asn != 0
	}
	r.mu.RUnlock()

	asn, ok := r.resolveCymru(ctx, ip)
	r.mu.Lock()
	if len(r.cache) >= asnCacheSize {
		r.evictExpiredLocked()
	}
	r.cache[key] = asnCacheEntry{asn: asn, exp: r.now().Add(asnCacheTTL)}
	r.mu.Unlock()
	return asn, ok
}

func (r *CymruASNResolver) evictExpiredLocked() {
	now := r.now()
	for k, e := range r.cache {
		if now.After(e.exp) {
			delete(r.cache, k)
		}
	}
}

func (r *CymruASNResolver) resolveCymru(ctx context.Context, ip net.IP) (uint32, bool) {
	ip = ip.To16()
	if ip == nil {
		return 0, false
	}

	var host string
	if ip4 := ip.To4(); ip4 != nil {
		host = cymruReverseV4(ip4) + "." + cymruV4Zone
	} else {
		host = cymruReverseV6(ip) + "." + cymruV6Zone
	}

	resolver := &net.Resolver{}
	txts, err := resolver.LookupTXT(ctx, host)
	if err != nil || len(txts) == 0 {
		return 0, false
	}

	first := strings.TrimSpace(txts[0])
	parts := strings.SplitN(first, "|", 2)
	if len(parts) < 1 {
		return 0, false
	}
	asnStr := strings.TrimSpace(parts[0])
	asnStr = strings.TrimPrefix(asnStr, "AS")
	asnStr = strings.TrimPrefix(asnStr, "as")
	if idx := strings.Index(asnStr, " "); idx >= 0 {
		asnStr = asnStr[:idx]
	}
	asn, err := strconv.ParseUint(asnStr, 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(asn), true
}

func cymruReverseV4(ip net.IP) string {
	return fmt.Sprintf("%d.%d.%d.%d", ip[3], ip[2], ip[1], ip[0])
}

func cymruReverseV6(ip net.IP) string {
	var sb strings.Builder
	for i := 15; i >= 0; i-- {
		b := ip[i]
		sb.WriteString(fmt.Sprintf("%x.%x.", b>>4, b&0xf))
	}
	return strings.TrimSuffix(sb.String(), ".")
}

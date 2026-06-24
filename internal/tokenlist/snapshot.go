// Package tokenlist holds the LI.FI allowlist as an in-process snapshot and
// refreshes it from LI.FI on a schedule, persisting to Redis and Postgres.
package tokenlist

import (
	"strings"
	"sync/atomic"
	"time"

	"wallet-api/internal/lifi"
)

// Snapshot is an immutable, indexed view of the token list.
type Snapshot struct {
	chain     string
	fetchedAt time.Time
	byAddress map[string]lifi.ListToken
	symbols   map[string]struct{}
}

// NewSnapshot indexes tokens by lowercased address and uppercased symbol.
func NewSnapshot(chain string, tokens []lifi.ListToken, fetchedAt time.Time) *Snapshot {
	byAddr := make(map[string]lifi.ListToken, len(tokens))
	syms := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		if t.Address != "" {
			byAddr[strings.ToLower(t.Address)] = t
		}
		if t.Symbol != "" {
			syms[strings.ToUpper(t.Symbol)] = struct{}{}
		}
	}
	return &Snapshot{chain: chain, fetchedAt: fetchedAt, byAddress: byAddr, symbols: syms}
}

// LookupByAddress returns the list token for a (case-insensitive) address.
func (s *Snapshot) LookupByAddress(addr string) (lifi.ListToken, bool) {
	t, ok := s.byAddress[strings.ToLower(strings.TrimSpace(addr))]
	return t, ok
}

// HasSymbol reports whether a (case-insensitive) symbol is in the list.
func (s *Snapshot) HasSymbol(sym string) bool {
	_, ok := s.symbols[strings.ToUpper(strings.TrimSpace(sym))]
	return ok
}

// Count returns the number of indexed addresses.
func (s *Snapshot) Count() int { return len(s.byAddress) }

// FetchedAt returns when the snapshot was fetched.
func (s *Snapshot) FetchedAt() time.Time { return s.fetchedAt }

// Holder stores the current snapshot for lock-free reads and atomic swaps.
// A nil current snapshot makes all lookups miss (never panics).
type Holder struct {
	ptr atomic.Pointer[Snapshot]
}

// Current returns the current snapshot (nil before the first Set).
func (h *Holder) Current() *Snapshot { return h.ptr.Load() }

// Set atomically swaps in a new snapshot.
func (h *Holder) Set(s *Snapshot) { h.ptr.Store(s) }

// LookupByAddress delegates to the current snapshot.
func (h *Holder) LookupByAddress(addr string) (lifi.ListToken, bool) {
	s := h.Current()
	if s == nil {
		return lifi.ListToken{}, false
	}
	return s.LookupByAddress(addr)
}

// HasSymbol delegates to the current snapshot.
func (h *Holder) HasSymbol(sym string) bool {
	s := h.Current()
	if s == nil {
		return false
	}
	return s.HasSymbol(sym)
}

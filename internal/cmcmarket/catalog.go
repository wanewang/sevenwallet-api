// Package cmcmarket resolves and caches CoinMarketCap market data.
package cmcmarket

import (
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"wallet-api/internal/coinmarketcap"
)

// CoinMapping maps one supported platform contract to a CoinMarketCap ID.
type CoinMapping struct {
	ID         int64
	Name       string
	Symbol     string
	PlatformID int64
	Address    string
	FetchedAt  time.Time
}

func normalizeAddress(address string) string {
	address = strings.TrimSpace(address)
	if strings.HasPrefix(strings.ToLower(address), "0x") {
		return strings.ToLower(address)
	}
	return address
}

// BuildMappings filters active CMC entries to explicitly supported platforms.
func BuildMappings(coins []coinmarketcap.Coin, supported map[int64]struct{}, fetchedAt time.Time) []CoinMapping {
	mappings := make([]CoinMapping, 0, len(coins))
	for _, coin := range coins {
		if coin.ID <= 0 || coin.IsActive != 1 || coin.Platform == nil {
			continue
		}
		if _, ok := supported[coin.Platform.ID]; !ok {
			continue
		}
		address := normalizeAddress(coin.Platform.TokenAddress)
		if address == "" {
			continue
		}
		mappings = append(mappings, CoinMapping{
			ID:         coin.ID,
			Name:       strings.TrimSpace(coin.Name),
			Symbol:     strings.TrimSpace(coin.Symbol),
			PlatformID: coin.Platform.ID,
			Address:    address,
			FetchedAt:  fetchedAt,
		})
	}
	sort.Slice(mappings, func(i, j int) bool {
		if mappings[i].ID != mappings[j].ID {
			return mappings[i].ID < mappings[j].ID
		}
		if mappings[i].PlatformID != mappings[j].PlatformID {
			return mappings[i].PlatformID < mappings[j].PlatformID
		}
		return mappings[i].Address < mappings[j].Address
	})
	return mappings
}

type Catalog struct {
	byContract map[string][]CoinMapping
	count      int
}

// NewCatalog builds an immutable contract lookup index.
func NewCatalog(mappings []CoinMapping) *Catalog {
	catalog := &Catalog{byContract: make(map[string][]CoinMapping), count: len(mappings)}
	for _, mapping := range mappings {
		key := contractLookupKey(mapping.PlatformID, mapping.Address)
		catalog.byContract[key] = append(catalog.byContract[key], mapping)
	}
	return catalog
}

func contractLookupKey(platformID int64, address string) string {
	return strconv.FormatInt(platformID, 10) + "\x00" + normalizeAddress(address)
}

// ResolveContract uses address identity first and symbol only for ambiguity.
func (c *Catalog) ResolveContract(platformID int64, address, symbol string) (int64, bool) {
	if c == nil {
		return 0, false
	}
	candidates := c.byContract[contractLookupKey(platformID, address)]
	ids := uniqueIDs(candidates, "")
	if len(ids) == 1 {
		return ids[0], true
	}
	if len(ids) <= 1 {
		return 0, false
	}
	ids = uniqueIDs(candidates, strings.TrimSpace(symbol))
	if len(ids) != 1 {
		return 0, false
	}
	return ids[0], true
}

func uniqueIDs(candidates []CoinMapping, symbol string) []int64 {
	seen := make(map[int64]struct{}, len(candidates))
	for _, candidate := range candidates {
		if symbol != "" && !strings.EqualFold(strings.TrimSpace(candidate.Symbol), symbol) {
			continue
		}
		seen[candidate.ID] = struct{}{}
	}
	ids := make([]int64, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (c *Catalog) Count() int {
	if c == nil {
		return 0
	}
	return c.count
}

// Holder atomically publishes immutable CMC catalogs.
type Holder struct{ ptr atomic.Pointer[Catalog] }

func (h *Holder) Current() *Catalog {
	if h == nil {
		return nil
	}
	return h.ptr.Load()
}

func (h *Holder) Set(catalog *Catalog) {
	if h != nil {
		h.ptr.Store(catalog)
	}
}

func (h *Holder) Count() int { return h.Current().Count() }

func (h *Holder) ResolveContract(platformID int64, address, symbol string) (int64, bool) {
	return h.Current().ResolveContract(platformID, address, symbol)
}

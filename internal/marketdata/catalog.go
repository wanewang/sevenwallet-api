package marketdata

import (
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"wallet-api/internal/coingecko"
)

const NativeAddress = "native"

type CoinMapping struct {
	ID        string
	Name      string
	Symbol    string
	Chain     string
	Address   string
	FetchedAt time.Time
}

func normalizePlatformAddress(address string) string {
	address = strings.TrimSpace(address)
	if strings.HasPrefix(strings.ToLower(address), "0x") {
		return strings.ToLower(address)
	}
	return address
}

func BuildMappings(coins []coingecko.Coin, fetchedAt time.Time) []CoinMapping {
	mappings := make([]CoinMapping, 0, len(coins))
	for _, coin := range coins {
		id := strings.TrimSpace(coin.ID)
		name := strings.TrimSpace(coin.Name)
		symbol := strings.TrimSpace(coin.Symbol)
		usablePlatform := false

		for platform, address := range coin.Platforms {
			chain := strings.ToLower(strings.TrimSpace(platform))
			address = normalizePlatformAddress(address)
			if chain == "" || address == "" {
				continue
			}
			usablePlatform = true
			mappings = append(mappings, CoinMapping{
				ID:        id,
				Name:      name,
				Symbol:    symbol,
				Chain:     chain,
				Address:   address,
				FetchedAt: fetchedAt,
			})
		}

		if !usablePlatform {
			mappings = append(mappings, CoinMapping{
				ID:        id,
				Name:      name,
				Symbol:    symbol,
				Chain:     strings.ToLower(symbol),
				Address:   NativeAddress,
				FetchedAt: fetchedAt,
			})
		}
	}

	sort.Slice(mappings, func(i, j int) bool {
		if mappings[i].ID != mappings[j].ID {
			return mappings[i].ID < mappings[j].ID
		}
		if mappings[i].Chain != mappings[j].Chain {
			return mappings[i].Chain < mappings[j].Chain
		}
		return mappings[i].Address < mappings[j].Address
	})
	return mappings
}

type Catalog struct {
	byContract map[string][]CoinMapping
	byNative   map[string][]CoinMapping
}

func NewCatalog(mappings []CoinMapping) *Catalog {
	catalog := &Catalog{
		byContract: make(map[string][]CoinMapping),
		byNative:   make(map[string][]CoinMapping),
	}
	for _, mapping := range mappings {
		if mapping.Address == NativeAddress {
			key := strings.ToLower(strings.TrimSpace(mapping.Symbol))
			catalog.byNative[key] = append(catalog.byNative[key], mapping)
			continue
		}
		key := contractLookupKey(mapping.Chain, mapping.Address)
		catalog.byContract[key] = append(catalog.byContract[key], mapping)
	}
	return catalog
}

func contractLookupKey(chain, address string) string {
	return strings.ToLower(strings.TrimSpace(chain)) + "\x00" + normalizePlatformAddress(address)
}

func (c *Catalog) ResolveContract(chain, address string) (string, bool) {
	if c == nil {
		return "", false
	}
	return oneID(c.byContract[contractLookupKey(chain, address)], nil)
}

func (c *Catalog) ResolveNative(symbol string, allowed map[string]struct{}) (string, bool) {
	if c == nil {
		return "", false
	}
	return oneID(c.byNative[strings.ToLower(strings.TrimSpace(symbol))], allowed)
}

func oneID(candidates []CoinMapping, allowed map[string]struct{}) (string, bool) {
	ids := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if allowed != nil {
			if _, ok := allowed[candidate.ID]; !ok {
				continue
			}
		}
		ids[candidate.ID] = struct{}{}
	}
	if len(ids) != 1 {
		return "", false
	}
	for id := range ids {
		return id, true
	}
	return "", false
}

type Holder struct {
	ptr atomic.Pointer[Catalog]
}

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

func (h *Holder) ResolveContract(chain, address string) (string, bool) {
	catalog := h.Current()
	if catalog == nil {
		return "", false
	}
	return catalog.ResolveContract(chain, address)
}

func (h *Holder) ResolveNative(symbol string, allowed map[string]struct{}) (string, bool) {
	catalog := h.Current()
	if catalog == nil {
		return "", false
	}
	return catalog.ResolveNative(symbol, allowed)
}

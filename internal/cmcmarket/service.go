package cmcmarket

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"wallet-api/internal/coinmarketcap"
	"wallet-api/internal/marketkey"
	"wallet-api/internal/marketpipeline"
	"wallet-api/internal/ptr"
	"wallet-api/internal/wallet"
)

type PriceClient interface {
	GetPrices(context.Context, []int64) (map[int64]coinmarketcap.SimplePrice, error)
}

type MarketCache interface {
	LoadCoinMarketCapMarketData(context.Context, []marketkey.Key) (map[marketkey.Key]Record, error)
	SaveCoinMarketCapMarketData(context.Context, []CacheWrite) error
}

type MarketStore interface {
	LoadCoinMarketCapMarketData(context.Context, []marketkey.Key) (map[marketkey.Key]Record, error)
	SaveCoinMarketCapMarketData(context.Context, []Record) error
}

// MissCache is the negative cache: tokens CoinMarketCap had no data for.
type MissCache interface {
	LoadCoinMarketCapMisses(context.Context, []marketkey.Key) (map[marketkey.Key]struct{}, error)
	SaveCoinMarketCapMisses(context.Context, []marketkey.Key, time.Duration) error
}

// Service resolves wallet tokens to CMC IDs and returns fresh market data.
type Service struct {
	client     PriceClient
	cache      MarketCache
	store      MarketStore
	misses     MissCache
	catalog    *Holder
	platformID int64
	nativeID   int64
	chain      string
	ttl        time.Duration
	missTTL    time.Duration
	now        func() time.Time
	logf       func(string, ...any)
}

func NewService(client PriceClient, cache MarketCache, store MarketStore, misses MissCache, catalog *Holder, platformID, nativeID int64, chain string, ttl, missTTL time.Duration) *Service {
	return &Service{
		client: client, cache: cache, store: store, misses: misses, catalog: catalog,
		platformID: platformID, nativeID: nativeID, chain: strings.ToLower(chain),
		ttl: ttl, missTTL: missTTL, now: time.Now, logf: log.Printf,
	}
}

// LookupFresh returns CMC data aligned with tokens. Expired records are misses,
// never fallbacks, and persistence failures do not discard usable provider data.
// The cascade itself lives in marketpipeline; this method only resolves tokens
// to CMC IDs and supplies the CMC-specific half.
func (s *Service) LookupFresh(ctx context.Context, tokens []wallet.Token) []*wallet.CoinMarketCapMarket {
	result := make([]*wallet.CoinMarketCapMarket, len(tokens))
	if len(tokens) == 0 || s == nil {
		return result
	}

	expectedID, indexesByKey, keyOrder := s.resolve(tokens)
	if len(keyOrder) == 0 {
		return result
	}

	marketpipeline.Run[int64, Record](
		ctx,
		&provider{service: s, result: result},
		marketpipeline.Request[int64]{
			KeyOrder:     keyOrder,
			ExpectedID:   expectedID,
			IndexesByKey: indexesByKey,
		},
		marketpipeline.Options[Record]{},
	)
	return result
}

// provider adapts Service to the shared cascade, closing over the output slice
// so Apply can write into it.
type provider struct {
	service *Service
	result  []*wallet.CoinMarketCapMarket
}

var _ marketpipeline.Provider[int64, Record] = (*provider)(nil)

func (p *provider) LoadCache(ctx context.Context, keys []marketkey.Key) (map[marketkey.Key]Record, error) {
	if p.service.cache == nil {
		return nil, nil
	}
	return p.service.cache.LoadCoinMarketCapMarketData(ctx, keys)
}

func (p *provider) SaveCache(ctx context.Context, writes []marketpipeline.Write[Record]) error {
	if p.service.cache == nil {
		return nil
	}
	out := make([]CacheWrite, len(writes))
	for i, w := range writes {
		out[i] = CacheWrite{Record: w.Record, TTL: w.TTL}
	}
	return p.service.cache.SaveCoinMarketCapMarketData(ctx, out)
}

func (p *provider) LoadStore(ctx context.Context, keys []marketkey.Key) (map[marketkey.Key]Record, error) {
	if p.service.store == nil {
		return nil, nil
	}
	return p.service.store.LoadCoinMarketCapMarketData(ctx, keys)
}

func (p *provider) SaveStore(ctx context.Context, records []Record) error {
	if p.service.store == nil {
		return nil
	}
	return p.service.store.SaveCoinMarketCapMarketData(ctx, records)
}

func (p *provider) Fetch(ctx context.Context, ids []int64) (map[int64]Record, error) {
	if p.service.client == nil {
		return nil, context.Canceled
	}
	prices, err := p.service.client.GetPrices(ctx, ids)
	if err != nil {
		return nil, err
	}
	fetchedAt := p.service.now().UTC()
	records := make(map[int64]Record, len(prices))
	for _, id := range ids {
		price, ok := prices[id]
		if !ok || price.ID != id {
			continue
		}
		records[id] = p.service.recordFromPrice(id, price, fetchedAt)
	}
	return records, nil
}

func (p *provider) LoadMisses(ctx context.Context, keys []marketkey.Key) (map[marketkey.Key]struct{}, error) {
	if p.service.misses == nil {
		return nil, nil
	}
	return p.service.misses.LoadCoinMarketCapMisses(ctx, keys)
}

func (p *provider) SaveMisses(ctx context.Context, keys []marketkey.Key, ttl time.Duration) error {
	if p.service.misses == nil {
		return nil
	}
	return p.service.misses.SaveCoinMarketCapMisses(ctx, keys, ttl)
}

// HasData asks whether CoinMarketCap returned anything at all, which for this
// provider is the same question Apply asks — a CMC record holds only a price
// and a 24h change, so a record with neither is empty by any measure.
func (p *provider) HasData(r Record) bool { return r.PriceUSD != nil || r.Change24H != nil }

func (p *provider) MissTTL() time.Duration { return p.service.missTTL }

func (p *provider) BatchLimit() int                          { return coinmarketcap.PriceBatchLimit }
func (p *provider) Matches(r Record, expected int64) bool    { return r.CoinMarketCapID == expected }
func (p *provider) FetchedAt(r Record) time.Time             { return r.FetchedAt }
func (p *provider) WithKey(r Record, k marketkey.Key) Record { r.Key = k; return r }
func (p *provider) TTL() time.Duration                       { return p.service.ttl }
func (p *provider) Now() time.Time                           { return p.service.now() }
func (p *provider) Logf(format string, args ...any)          { p.service.logf("cmcmarket: "+format, args...) }

// Apply reports false for a record carrying neither a price nor a 24h change,
// which is what keeps such a record out of the result and out of storage.
func (p *provider) Apply(indexes []int, r Record) bool {
	if r.PriceUSD == nil && r.Change24H == nil {
		return false
	}
	for _, index := range indexes {
		p.result[index] = &wallet.CoinMarketCapMarket{
			ID:               r.CoinMarketCapID,
			PriceUSD:         ptr.Clone(r.PriceUSD),
			Change24HPercent: ptr.Clone(r.Change24H),
		}
	}
	return true
}

func (s *Service) resolve(tokens []wallet.Token) (map[marketkey.Key]int64, map[marketkey.Key][]int, []marketkey.Key) {
	expectedID := make(map[marketkey.Key]int64, len(tokens))
	indexesByKey := make(map[marketkey.Key][]int, len(tokens))
	keyOrder := make([]marketkey.Key, 0, len(tokens))
	seen := make(map[marketkey.Key]struct{}, len(tokens))
	for index, token := range tokens {
		var key marketkey.Key
		var id int64
		var ok bool
		if token.IsNative || token.TokenAddress == nil {
			key = marketkey.NativeKey(s.chain, token.Symbol)
			id, ok = s.nativeID, s.nativeID > 0
		} else if strings.TrimSpace(*token.TokenAddress) != "" && s.catalog != nil {
			key = marketkey.ContractKey(s.chain, *token.TokenAddress)
			id, ok = s.catalog.ResolveContract(s.platformID, *token.TokenAddress, token.Symbol)
		}
		if !ok {
			if token.TokenAddress != nil && !token.IsNative {
				key = marketkey.ContractKey(s.chain, *token.TokenAddress)
				s.logf("cmcmarket: unresolved contract mapping source=cmc chain=%q address=%q", key.Chain, key.TokenKey)
			}
			continue
		}
		if prior, exists := expectedID[key]; exists && prior != id {
			continue
		}
		expectedID[key] = id
		indexesByKey[key] = append(indexesByKey[key], index)
		if _, exists := seen[key]; !exists {
			seen[key] = struct{}{}
			keyOrder = append(keyOrder, key)
		}
	}
	return expectedID, indexesByKey, keyOrder
}

func (s *Service) recordFromPrice(id int64, price coinmarketcap.SimplePrice, fetchedAt time.Time) Record {
	record := Record{CoinMarketCapID: id, FetchedAt: fetchedAt}
	if price.Price != nil {
		var parsed json.Number
		if err := json.Unmarshal([]byte(price.Price.String()), &parsed); err != nil || parsed.String() != price.Price.String() {
			if err == nil {
				err = errors.New("invalid JSON number")
			}
			s.logf("cmcmarket: invalid USD price for %d: %v", id, err)
		} else {
			value := price.Price.String()
			record.PriceUSD = &value
		}
	}
	if price.PercentChange24H != nil {
		value, err := price.PercentChange24H.Float64()
		if err != nil {
			s.logf("cmcmarket: invalid 24h change for %d: %v", id, err)
		} else {
			record.Change24H = &value
		}
	}
	return record
}

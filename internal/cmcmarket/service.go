package cmcmarket

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sort"
	"strings"
	"time"

	"wallet-api/internal/coinmarketcap"
	"wallet-api/internal/marketdata"
	"wallet-api/internal/wallet"
)

type PriceClient interface {
	GetPrices(context.Context, []int64) (map[int64]coinmarketcap.SimplePrice, error)
}

type MarketCache interface {
	LoadCoinMarketCapMarketData(context.Context, []marketdata.Key) (map[marketdata.Key]Record, error)
	SaveCoinMarketCapMarketData(context.Context, []CacheWrite) error
}

type MarketStore interface {
	LoadCoinMarketCapMarketData(context.Context, []marketdata.Key) (map[marketdata.Key]Record, error)
	SaveCoinMarketCapMarketData(context.Context, []Record) error
}

// Service resolves wallet tokens to CMC IDs and returns fresh market data.
type Service struct {
	client     PriceClient
	cache      MarketCache
	store      MarketStore
	catalog    *Holder
	platformID int64
	nativeID   int64
	chain      string
	ttl        time.Duration
	now        func() time.Time
	logf       func(string, ...any)
}

func NewService(client PriceClient, cache MarketCache, store MarketStore, catalog *Holder, platformID, nativeID int64, chain string, ttl time.Duration) *Service {
	return &Service{
		client: client, cache: cache, store: store, catalog: catalog,
		platformID: platformID, nativeID: nativeID, chain: strings.ToLower(chain),
		ttl: ttl, now: time.Now, logf: log.Printf,
	}
}

// LookupFresh returns CMC data aligned with tokens. Expired records are misses,
// never fallbacks, and persistence failures do not discard usable provider data.
func (s *Service) LookupFresh(ctx context.Context, tokens []wallet.Token) []*wallet.CoinMarketCapMarket {
	result := make([]*wallet.CoinMarketCapMarket, len(tokens))
	if len(tokens) == 0 || s == nil {
		return result
	}
	if ctx == nil {
		ctx = context.Background()
	}

	expectedID, indexesByKey, keyOrder := s.resolve(tokens)
	if len(keyOrder) == 0 {
		return result
	}
	now := s.now().UTC()
	complete := make(map[marketdata.Key]bool, len(keyOrder))

	redisRecords := make(map[marketdata.Key]Record)
	if s.cache != nil {
		loaded, err := s.cache.LoadCoinMarketCapMarketData(ctx, keyOrder)
		if err != nil {
			s.logf("cmcmarket: redis load failed: %v", err)
		} else {
			redisRecords = loaded
		}
	}
	for _, key := range keyOrder {
		record, ok := matchingMarketRecord(redisRecords, key, expectedID[key])
		if !ok || !record.Fresh(now, s.ttl) {
			continue
		}
		if applyResult(result, indexesByKey[key], record) {
			complete[key] = true
		}
	}

	remaining := incompleteMarketKeys(keyOrder, complete)
	postgresRecords := make(map[marketdata.Key]Record)
	if s.store != nil && len(remaining) > 0 {
		loaded, err := s.store.LoadCoinMarketCapMarketData(ctx, remaining)
		if err != nil {
			s.logf("cmcmarket: postgres load failed: %v", err)
		} else {
			postgresRecords = loaded
		}
	}
	promotions := make([]CacheWrite, 0, len(remaining))
	for _, key := range remaining {
		record, ok := matchingMarketRecord(postgresRecords, key, expectedID[key])
		if !ok || !record.Fresh(now, s.ttl) {
			continue
		}
		if !applyResult(result, indexesByKey[key], record) {
			continue
		}
		complete[key] = true
		if ttl := record.RemainingTTL(now, s.ttl); ttl > 0 {
			promotions = append(promotions, CacheWrite{Record: record, TTL: ttl})
		}
	}
	if len(promotions) > 0 && s.cache != nil {
		if err := s.cache.SaveCoinMarketCapMarketData(ctx, promotions); err != nil {
			s.logf("cmcmarket: redis promotion failed: %v", err)
		}
	}

	groups := make(map[int64][]marketdata.Key)
	for _, key := range keyOrder {
		if !complete[key] {
			groups[expectedID[key]] = append(groups[expectedID[key]], key)
		}
	}
	ids := make([]int64, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for start := 0; start < len(ids); start += coinmarketcap.PriceBatchLimit {
		if ctx.Err() != nil {
			break
		}
		end := start + coinmarketcap.PriceBatchLimit
		if end > len(ids) {
			end = len(ids)
		}
		batchIDs := ids[start:end]
		prices, err := s.clientPrices(ctx, batchIDs)
		if err != nil {
			s.logf("cmcmarket: price batch failed for %d IDs: %v", len(batchIDs), err)
			if ctx.Err() != nil {
				break
			}
			continue
		}

		fetchedAt := s.now().UTC()
		fetched := make([]Record, 0)
		for _, id := range batchIDs {
			price, ok := prices[id]
			if !ok || price.ID != id {
				continue
			}
			for _, key := range groups[id] {
				record := s.recordFromPrice(key, id, price, fetchedAt)
				if !applyResult(result, indexesByKey[key], record) {
					continue
				}
				complete[key] = true
				fetched = append(fetched, record)
			}
		}
		if len(fetched) == 0 {
			continue
		}
		if s.store != nil {
			if err := s.store.SaveCoinMarketCapMarketData(ctx, fetched); err != nil {
				s.logf("cmcmarket: postgres save failed: %v", err)
			}
		}
		if s.cache != nil {
			writes := make([]CacheWrite, len(fetched))
			for i, record := range fetched {
				writes[i] = CacheWrite{Record: record, TTL: s.ttl}
			}
			if err := s.cache.SaveCoinMarketCapMarketData(ctx, writes); err != nil {
				s.logf("cmcmarket: redis save failed: %v", err)
			}
		}
	}
	return result
}

func (s *Service) resolve(tokens []wallet.Token) (map[marketdata.Key]int64, map[marketdata.Key][]int, []marketdata.Key) {
	expectedID := make(map[marketdata.Key]int64, len(tokens))
	indexesByKey := make(map[marketdata.Key][]int, len(tokens))
	keyOrder := make([]marketdata.Key, 0, len(tokens))
	seen := make(map[marketdata.Key]struct{}, len(tokens))
	for index, token := range tokens {
		var key marketdata.Key
		var id int64
		var ok bool
		if token.IsNative || token.TokenAddress == nil {
			key = marketdata.NativeKey(s.chain, token.Symbol)
			id, ok = s.nativeID, s.nativeID > 0
		} else if strings.TrimSpace(*token.TokenAddress) != "" && s.catalog != nil {
			key = marketdata.ContractKey(s.chain, *token.TokenAddress)
			id, ok = s.catalog.ResolveContract(s.platformID, *token.TokenAddress, token.Symbol)
		}
		if !ok {
			if token.TokenAddress != nil && !token.IsNative {
				key = marketdata.ContractKey(s.chain, *token.TokenAddress)
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

func (s *Service) clientPrices(ctx context.Context, ids []int64) (map[int64]coinmarketcap.SimplePrice, error) {
	if s.client == nil {
		return nil, context.Canceled
	}
	return s.client.GetPrices(ctx, ids)
}

func matchingMarketRecord(records map[marketdata.Key]Record, key marketdata.Key, expectedID int64) (Record, bool) {
	record, ok := records[key]
	if !ok || record.CoinMarketCapID != expectedID {
		return Record{}, false
	}
	record.Key = key
	return record, true
}

func incompleteMarketKeys(keys []marketdata.Key, complete map[marketdata.Key]bool) []marketdata.Key {
	remaining := make([]marketdata.Key, 0, len(keys))
	for _, key := range keys {
		if !complete[key] {
			remaining = append(remaining, key)
		}
	}
	return remaining
}

func (s *Service) recordFromPrice(key marketdata.Key, id int64, price coinmarketcap.SimplePrice, fetchedAt time.Time) Record {
	record := Record{Key: key, CoinMarketCapID: id, FetchedAt: fetchedAt}
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

func applyResult(result []*wallet.CoinMarketCapMarket, indexes []int, record Record) bool {
	if record.PriceUSD == nil && record.Change24H == nil {
		return false
	}
	for _, index := range indexes {
		result[index] = &wallet.CoinMarketCapMarket{
			ID:               record.CoinMarketCapID,
			PriceUSD:         cloneString(record.PriceUSD),
			Change24HPercent: cloneFloat(record.Change24H),
		}
	}
	return true
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

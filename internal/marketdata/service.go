package marketdata

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sort"
	"strings"
	"time"

	"wallet-api/internal/coingecko"
	"wallet-api/internal/wallet"
)

const priceBatchSize = 100

type PriceClient interface {
	GetPrices(context.Context, []string) (map[string]coingecko.SimplePrice, error)
}

type MarketCache interface {
	LoadMarketData(context.Context, []Key) (map[Key]Record, error)
	SaveMarketData(context.Context, []CacheWrite) error
}

type MarketStore interface {
	LoadMarketData(context.Context, []Key) (map[Key]Record, error)
	SaveMarketData(context.Context, []Record) error
}

type Service struct {
	client        PriceClient
	cache         MarketCache
	store         MarketStore
	catalog       *Holder
	platform      string
	nativeIDs     map[string]struct{}
	ttl           time.Duration
	enrichTimeout time.Duration
	now           func() time.Time
	logf          func(string, ...any)
}

func NewService(client PriceClient, cache MarketCache, store MarketStore, catalog *Holder, platform string, nativeIDs []string, ttl, enrichTimeout time.Duration) *Service {
	allowed := make(map[string]struct{}, len(nativeIDs))
	for _, id := range nativeIDs {
		allowed[id] = struct{}{}
	}
	return &Service{
		client: client, cache: cache, store: store, catalog: catalog,
		platform: strings.ToLower(platform), nativeIDs: allowed,
		ttl: ttl, enrichTimeout: enrichTimeout, now: time.Now, logf: log.Printf,
	}
}

type resolvedToken struct {
	Index int
	Key   Key
	ID    string
}

var _ wallet.MarketEnricher = (*Service)(nil)

func (s *Service) EnrichTokens(ctx context.Context, tokens []wallet.Token) []wallet.Token {
	out := cloneTokens(tokens)
	if len(out) == 0 || s == nil || s.catalog == nil || s.catalog.Current() == nil {
		return out
	}
	if ctx == nil {
		ctx = context.Background()
	}
	enrichCtx, cancel := context.WithTimeout(ctx, s.enrichTimeout)
	defer cancel()

	resolved, expectedID, indexesByKey, keyOrder := s.resolve(s.catalog.Current(), out)
	if len(resolved) == 0 {
		return out
	}

	now := s.now().UTC()
	complete := make(map[Key]bool, len(keyOrder))
	stale := make(map[Key]Record)

	redisRecords := make(map[Key]Record)
	if s.cache != nil {
		loaded, err := s.cache.LoadMarketData(enrichCtx, keyOrder)
		if err != nil {
			s.logf("marketdata: redis load failed: %v", err)
		} else {
			redisRecords = loaded
		}
	}
	for _, key := range keyOrder {
		record, ok := matchingRecord(redisRecords, key, expectedID[key])
		if !ok {
			continue
		}
		if record.Fresh(now, s.ttl) {
			s.applyFresh(out, indexesByKey[key], record)
			complete[key] = true
			continue
		}
		stale[key] = record
	}

	remaining := incompleteKeys(keyOrder, complete)
	promotions := make([]CacheWrite, 0, len(remaining))
	postgresRecords := make(map[Key]Record)
	if s.store != nil && len(remaining) > 0 {
		loaded, err := s.store.LoadMarketData(enrichCtx, remaining)
		if err != nil {
			s.logf("marketdata: postgres load failed: %v", err)
		} else {
			postgresRecords = loaded
		}
	}
	for _, key := range remaining {
		record, ok := matchingRecord(postgresRecords, key, expectedID[key])
		if !ok {
			continue
		}
		if record.Fresh(now, s.ttl) {
			s.applyFresh(out, indexesByKey[key], record)
			complete[key] = true
			if ttl := record.RemainingTTL(now, s.ttl); ttl > 0 {
				promotions = append(promotions, CacheWrite{Record: record, TTL: ttl})
			}
			continue
		}
		stale[key] = newerStaleRecord(stale[key], record)
	}
	if len(promotions) > 0 && s.cache != nil {
		if err := s.cache.SaveMarketData(enrichCtx, promotions); err != nil {
			s.logf("marketdata: redis promotion failed: %v", err)
		}
	}

	groups := make(map[string][]Key)
	for _, key := range keyOrder {
		if complete[key] {
			continue
		}
		id := expectedID[key]
		groups[id] = append(groups[id], key)
	}
	ids := make([]string, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for start := 0; start < len(ids); start += priceBatchSize {
		if err := enrichCtx.Err(); err != nil {
			break
		}
		end := start + priceBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		batchIDs := ids[start:end]
		prices, err := s.clientPrices(enrichCtx, batchIDs)
		if err != nil {
			s.logf("marketdata: CoinGecko price batch failed for %d IDs: %v", len(batchIDs), err)
			if enrichCtx.Err() != nil {
				break
			}
			continue
		}

		fetchedAt := s.now().UTC()
		fetched := make([]Record, 0)
		for _, id := range batchIDs {
			price, ok := prices[id]
			if !ok {
				continue
			}
			for _, key := range groups[id] {
				if complete[key] {
					continue
				}
				record := s.recordFromPrice(key, id, price, fetchedAt)
				s.applyFresh(out, indexesByKey[key], record)
				complete[key] = true
				fetched = append(fetched, record)
			}
		}
		if len(fetched) == 0 {
			continue
		}
		if s.store != nil {
			if err := s.store.SaveMarketData(enrichCtx, fetched); err != nil {
				s.logf("marketdata: postgres save failed: %v", err)
			}
		}
		if s.cache != nil {
			writes := make([]CacheWrite, len(fetched))
			for i, record := range fetched {
				writes[i] = CacheWrite{Record: record, TTL: s.ttl}
			}
			if err := s.cache.SaveMarketData(enrichCtx, writes); err != nil {
				s.logf("marketdata: redis save failed: %v", err)
			}
		}
	}

	for _, key := range keyOrder {
		if !complete[key] {
			if record, ok := stale[key]; ok {
				s.applyStale(out, indexesByKey[key], record)
			}
		}
	}
	return out
}

func (s *Service) resolve(catalog *Catalog, tokens []wallet.Token) ([]resolvedToken, map[Key]string, map[Key][]int, []Key) {
	resolved := make([]resolvedToken, 0, len(tokens))
	expectedID := make(map[Key]string, len(tokens))
	indexesByKey := make(map[Key][]int, len(tokens))
	keyOrder := make([]Key, 0, len(tokens))
	seenKeys := make(map[Key]struct{}, len(tokens))
	for index, token := range tokens {
		var id string
		var ok bool
		var key Key
		if token.IsNative {
			id, ok = catalog.ResolveNative(token.Symbol, s.nativeIDs)
			key = NativeKey(s.platform, token.Symbol)
			if !ok {
				s.logf("marketdata: unresolved native mapping symbol=%q", strings.TrimSpace(token.Symbol))
			}
		} else if token.TokenAddress != nil && strings.TrimSpace(*token.TokenAddress) != "" {
			id, ok = catalog.ResolveContract(s.platform, *token.TokenAddress)
			key = ContractKey(s.platform, *token.TokenAddress)
			if !ok {
				s.logf("marketdata: unresolved contract mapping chain=%q address=%q", s.platform, strings.TrimSpace(*token.TokenAddress))
			}
		} else {
			s.logf("marketdata: unresolved contract mapping chain=%q address=%q", s.platform, "")
		}
		if !ok || strings.TrimSpace(id) == "" {
			continue
		}
		resolved = append(resolved, resolvedToken{Index: index, Key: key, ID: id})
		if _, exists := expectedID[key]; !exists {
			expectedID[key] = id
		}
		indexesByKey[key] = append(indexesByKey[key], index)
		if _, exists := seenKeys[key]; !exists {
			seenKeys[key] = struct{}{}
			keyOrder = append(keyOrder, key)
		}
	}
	return resolved, expectedID, indexesByKey, keyOrder
}

func (s *Service) clientPrices(ctx context.Context, ids []string) (map[string]coingecko.SimplePrice, error) {
	if s.client == nil {
		return nil, context.Canceled
	}
	return s.client.GetPrices(ctx, ids)
}

func matchingRecord(records map[Key]Record, key Key, expectedID string) (Record, bool) {
	record, ok := records[key]
	if !ok || record.CoinGeckoID != expectedID {
		return Record{}, false
	}
	record.Key = key
	return record, true
}

func incompleteKeys(keys []Key, complete map[Key]bool) []Key {
	remaining := make([]Key, 0, len(keys))
	for _, key := range keys {
		if !complete[key] {
			remaining = append(remaining, key)
		}
	}
	return remaining
}

func newerStaleRecord(current, candidate Record) Record {
	if current.CoinGeckoID == "" || candidate.FetchedAt.After(current.FetchedAt) {
		return candidate
	}
	return current
}

func (s *Service) recordFromPrice(key Key, id string, price coingecko.SimplePrice, fetchedAt time.Time) Record {
	record := Record{Key: key, CoinGeckoID: id, FetchedAt: fetchedAt}
	if price.USD != nil {
		var parsed json.Number
		if err := json.Unmarshal([]byte(price.USD.String()), &parsed); err != nil || parsed.String() != price.USD.String() {
			if err == nil {
				err = errors.New("invalid JSON number")
			}
			s.logf("marketdata: invalid USD price for %q: %v", id, err)
		} else {
			value := price.USD.String()
			record.PriceUSD = &value
		}
	}
	record.Change24HPercent = s.optionalFloat(id, "24h change", price.USD24HChange)
	record.MarketCapUSD = s.optionalFloat(id, "market cap", price.USDMarketCap)
	if price.LastUpdatedAt != nil {
		updated := time.Unix(*price.LastUpdatedAt, 0).UTC()
		record.MarketDataUpdatedAt = &updated
	}
	return record
}

func (s *Service) optionalFloat(id, field string, number *json.Number) *float64 {
	if number == nil {
		return nil
	}
	value, err := number.Float64()
	if err != nil {
		s.logf("marketdata: invalid %s for %q: %v", field, id, err)
		return nil
	}
	return &value
}

func (s *Service) applyFresh(tokens []wallet.Token, indexes []int, record Record) {
	for _, index := range indexes {
		tokens[index].Change24HPercent = cloneFloat(record.Change24HPercent)
		tokens[index].MarketCapUSD = cloneFloat(record.MarketCapUSD)
		tokens[index].MarketDataUpdatedAt = cloneMarketTime(record.MarketDataUpdatedAt)
		if record.PriceUSD == nil {
			continue
		}
		value := *record.PriceUSD
		tokens[index].PriceUSD = &value
		updated := ""
		if record.MarketDataUpdatedAt != nil {
			updated = record.MarketDataUpdatedAt.UTC().Format(time.RFC3339)
		}
		tokens[index].Price = &wallet.Price{Currency: "usd", Value: value, LastUpdatedAt: updated}
	}
}

func (s *Service) applyStale(tokens []wallet.Token, indexes []int, record Record) {
	for _, index := range indexes {
		tokens[index].Change24HPercent = cloneFloat(record.Change24HPercent)
		tokens[index].MarketCapUSD = cloneFloat(record.MarketCapUSD)
		tokens[index].MarketDataUpdatedAt = cloneMarketTime(record.MarketDataUpdatedAt)
	}
}

func cloneTokens(tokens []wallet.Token) []wallet.Token {
	if tokens == nil {
		return nil
	}
	out := make([]wallet.Token, len(tokens))
	copy(out, tokens)
	for i := range out {
		out[i].TokenAddress = cloneString(tokens[i].TokenAddress)
		out[i].LogoURI = cloneString(tokens[i].LogoURI)
		out[i].CoinKey = cloneString(tokens[i].CoinKey)
		out[i].PriceUSD = cloneString(tokens[i].PriceUSD)
		out[i].Change24HPercent = cloneFloat(tokens[i].Change24HPercent)
		out[i].MarketCapUSD = cloneFloat(tokens[i].MarketCapUSD)
		out[i].MarketDataUpdatedAt = cloneString(tokens[i].MarketDataUpdatedAt)
		if tokens[i].Price != nil {
			price := *tokens[i].Price
			out[i].Price = &price
		}
	}
	return out
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

func cloneMarketTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339)
	return &formatted
}

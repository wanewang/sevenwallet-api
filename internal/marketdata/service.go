package marketdata

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"wallet-api/internal/coingecko"
	"wallet-api/internal/marketkey"
	"wallet-api/internal/marketpipeline"
	"wallet-api/internal/ptr"
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

// MissCache is the negative cache: tokens CoinGecko had no data for.
type MissCache interface {
	LoadCoinGeckoMisses(context.Context, []Key) (map[Key]struct{}, error)
	SaveCoinGeckoMisses(context.Context, []Key, time.Duration) error
}

type Service struct {
	client        PriceClient
	cache         MarketCache
	store         MarketStore
	misses        MissCache
	catalog       *Holder
	platform      string
	nativeIDs     map[string]struct{}
	ttl           time.Duration
	missTTL       time.Duration
	enrichTimeout time.Duration
	now           func() time.Time
	logf          func(string, ...any)
}

func NewService(client PriceClient, cache MarketCache, store MarketStore, misses MissCache, catalog *Holder, platform string, nativeIDs []string, ttl, missTTL, enrichTimeout time.Duration) *Service {
	allowed := make(map[string]struct{}, len(nativeIDs))
	for _, id := range nativeIDs {
		allowed[id] = struct{}{}
	}
	return &Service{
		client: client, cache: cache, store: store, misses: misses, catalog: catalog,
		platform: strings.ToLower(platform), nativeIDs: allowed,
		ttl: ttl, missTTL: missTTL, enrichTimeout: enrichTimeout, now: time.Now, logf: log.Printf,
	}
}

var _ wallet.MarketEnricher = (*Service)(nil)

// LookupFresh returns CoinGecko comparison data aligned with tokens. It uses
// the existing catalog and cache hierarchy, but deliberately never serves an
// expired record and does not mutate the input token slice. The cascade itself
// lives in marketpipeline; this method only resolves tokens to CoinGecko IDs
// and supplies the CoinGecko-specific half.
func (s *Service) LookupFresh(ctx context.Context, tokens []wallet.Token) []*wallet.CoinGeckoMarket {
	return s.lookupFresh(ctx, tokens, nil)
}

// LookupFreshWithProgress is LookupFresh with snapshots published whenever a
// cascade tier or provider batch applies new results. The callback runs before
// persistence for a fetched batch, allowing a deadline response to retain the
// completed data even if a later write is still in flight.
func (s *Service) LookupFreshWithProgress(ctx context.Context, tokens []wallet.Token, progress func([]*wallet.CoinGeckoMarket)) []*wallet.CoinGeckoMarket {
	return s.lookupFresh(ctx, tokens, progress)
}

func (s *Service) lookupFresh(ctx context.Context, tokens []wallet.Token, progress func([]*wallet.CoinGeckoMarket)) []*wallet.CoinGeckoMarket {
	result := make([]*wallet.CoinGeckoMarket, len(tokens))
	if len(tokens) == 0 || s == nil || s.catalog == nil || s.catalog.Current() == nil {
		return result
	}

	request := s.resolve(s.catalog.Current(), tokens)
	if len(request.KeyOrder) == 0 {
		return result
	}

	options := marketpipeline.Options[Record]{}
	if progress != nil {
		options.Progress = func() { progress(result) }
	}
	marketpipeline.Run[string, Record](
		ctx,
		&lookupProvider{service: s, result: result},
		request,
		options,
	)
	return result
}

// lookupProvider adapts Service to the shared cascade for the comparison route,
// closing over the output slice so Apply can write into it.
type lookupProvider struct {
	service *Service
	result  []*wallet.CoinGeckoMarket
}

var _ marketpipeline.Provider[string, Record] = (*lookupProvider)(nil)

func (p *lookupProvider) LoadCache(ctx context.Context, keys []marketkey.Key) (map[marketkey.Key]Record, error) {
	if p.service.cache == nil {
		return nil, nil
	}
	return p.service.cache.LoadMarketData(ctx, keys)
}

func (p *lookupProvider) SaveCache(ctx context.Context, writes []marketpipeline.Write[Record]) error {
	if p.service.cache == nil {
		return nil
	}
	return p.service.cache.SaveMarketData(ctx, toCacheWrites(writes))
}

func (p *lookupProvider) LoadStore(ctx context.Context, keys []marketkey.Key) (map[marketkey.Key]Record, error) {
	if p.service.store == nil {
		return nil, nil
	}
	return p.service.store.LoadMarketData(ctx, keys)
}

func (p *lookupProvider) SaveStore(ctx context.Context, records []Record) error {
	if p.service.store == nil {
		return nil
	}
	return p.service.store.SaveMarketData(ctx, records)
}

func (p *lookupProvider) Fetch(ctx context.Context, ids []string) (map[string]Record, error) {
	return p.service.fetchRecords(ctx, ids)
}

func (p *lookupProvider) LoadMisses(ctx context.Context, keys []marketkey.Key) (map[marketkey.Key]struct{}, error) {
	if p.service.misses == nil {
		return nil, nil
	}
	return p.service.misses.LoadCoinGeckoMisses(ctx, keys)
}

func (p *lookupProvider) SaveMisses(ctx context.Context, keys []marketkey.Key, ttl time.Duration) error {
	if p.service.misses == nil {
		return nil
	}
	return p.service.misses.SaveCoinGeckoMisses(ctx, keys, ttl)
}

// HasData is broader than Apply on purpose. Apply asks whether the comparison
// route can render the record; HasData asks whether CoinGecko returned anything
// at all. A market-cap-only record fails Apply but has data, so it must not be
// recorded as a miss — enrichment can still use it.
func (p *lookupProvider) HasData(r Record) bool {
	return r.PriceUSD != nil || r.Change24HPercent != nil || r.MarketCapUSD != nil || r.MarketDataUpdatedAt != nil
}

func (p *lookupProvider) MissTTL() time.Duration { return p.service.missTTL }

func (p *lookupProvider) BatchLimit() int                          { return priceBatchSize }
func (p *lookupProvider) Matches(r Record, expected string) bool   { return r.CoinGeckoID == expected }
func (p *lookupProvider) FetchedAt(r Record) time.Time             { return r.FetchedAt }
func (p *lookupProvider) WithKey(r Record, k marketkey.Key) Record { r.Key = k; return r }
func (p *lookupProvider) TTL() time.Duration                       { return p.service.ttl }
func (p *lookupProvider) Now() time.Time                           { return p.service.now() }
func (p *lookupProvider) Logf(format string, args ...any) {
	p.service.logf("marketdata: fresh CoinGecko "+format, args...)
}

// Apply reports false for a record carrying neither a price nor a 24h change.
// The cascade honours that by leaving the key incomplete and writing nothing,
// so a payload with no data can no longer occupy the record for a whole TTL.
func (p *lookupProvider) Apply(indexes []int, record Record) bool {
	if record.PriceUSD == nil && record.Change24HPercent == nil {
		return false
	}
	for _, index := range indexes {
		p.result[index] = &wallet.CoinGeckoMarket{
			ID:               record.CoinGeckoID,
			PriceUSD:         ptr.Clone(record.PriceUSD),
			Change24HPercent: ptr.Clone(record.Change24HPercent),
		}
	}
	return true
}

func toCacheWrites(writes []marketpipeline.Write[Record]) []CacheWrite {
	out := make([]CacheWrite, len(writes))
	for i, w := range writes {
		out[i] = CacheWrite{Record: w.Record, TTL: w.TTL}
	}
	return out
}

// fetchRecords turns one batch of CoinGecko IDs into records. The key is left
// unset; the cascade stamps it per key, since one ID can back several keys.
func (s *Service) fetchRecords(ctx context.Context, ids []string) (map[string]Record, error) {
	prices, err := s.clientPrices(ctx, ids)
	if err != nil {
		return nil, err
	}
	fetchedAt := s.now().UTC()
	records := make(map[string]Record, len(prices))
	for _, id := range ids {
		price, ok := prices[id]
		if !ok {
			continue
		}
		records[id] = s.recordFromPrice(Key{}, id, price, fetchedAt)
	}
	return records, nil
}

// EnrichTokens fills market fields on a copy of tokens. It runs the same
// cascade as LookupFresh, adding a stale hook: when no tier has fresh data, the
// newest expired record still supplies the non-price fields.
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

	request := s.resolve(s.catalog.Current(), out)
	if len(request.KeyOrder) == 0 {
		return out
	}

	stale := make(map[Key]Record)
	complete := marketpipeline.Run[string, Record](
		enrichCtx,
		&enrichProvider{service: s, tokens: out},
		request,
		marketpipeline.Options[Record]{
			Stale: func(key Key, record Record) {
				stale[key] = newerStaleRecord(stale[key], record)
			},
		},
	)

	// Anything the cascade could not complete falls back to the newest expired
	// record seen, in the reduced stale form: no price, no Price object.
	for _, key := range request.KeyOrder {
		if complete[key] {
			continue
		}
		if record, ok := stale[key]; ok {
			s.applyStale(out, request.IndexesByKey[key], record)
		}
	}
	return out
}

// enrichProvider is the address route's half of the cascade. It differs from
// lookupProvider in exactly one way that matters: what counts as usable.
type enrichProvider struct {
	service *Service
	tokens  []wallet.Token
}

var _ marketpipeline.Provider[string, Record] = (*enrichProvider)(nil)

// Apply is deliberately more permissive than lookupProvider.Apply. Enrichment
// also renders MarketCapUSD and MarketDataUpdatedAt, so a record carrying only
// those is useful here even though the comparison route would reject it.
func (p *enrichProvider) Apply(indexes []int, record Record) bool {
	if record.PriceUSD == nil && record.Change24HPercent == nil &&
		record.MarketCapUSD == nil && record.MarketDataUpdatedAt == nil {
		return false
	}
	p.service.applyFresh(p.tokens, indexes, record)
	return true
}

func (p *enrichProvider) LoadCache(ctx context.Context, keys []marketkey.Key) (map[marketkey.Key]Record, error) {
	if p.service.cache == nil {
		return nil, nil
	}
	return p.service.cache.LoadMarketData(ctx, keys)
}

func (p *enrichProvider) SaveCache(ctx context.Context, writes []marketpipeline.Write[Record]) error {
	if p.service.cache == nil {
		return nil
	}
	return p.service.cache.SaveMarketData(ctx, toCacheWrites(writes))
}

func (p *enrichProvider) LoadStore(ctx context.Context, keys []marketkey.Key) (map[marketkey.Key]Record, error) {
	if p.service.store == nil {
		return nil, nil
	}
	return p.service.store.LoadMarketData(ctx, keys)
}

func (p *enrichProvider) SaveStore(ctx context.Context, records []Record) error {
	if p.service.store == nil {
		return nil
	}
	return p.service.store.SaveMarketData(ctx, records)
}

func (p *enrichProvider) Fetch(ctx context.Context, ids []string) (map[string]Record, error) {
	return p.service.fetchRecords(ctx, ids)
}

func (p *enrichProvider) LoadMisses(ctx context.Context, keys []marketkey.Key) (map[marketkey.Key]struct{}, error) {
	if p.service.misses == nil {
		return nil, nil
	}
	return p.service.misses.LoadCoinGeckoMisses(ctx, keys)
}

func (p *enrichProvider) SaveMisses(ctx context.Context, keys []marketkey.Key, ttl time.Duration) error {
	if p.service.misses == nil {
		return nil
	}
	return p.service.misses.SaveCoinGeckoMisses(ctx, keys, ttl)
}

// HasData matches lookupProvider.HasData: whether CoinGecko returned anything
// at all. A miss is a fact about the provider, so both callers agree on it even
// though they disagree about Apply.
func (p *enrichProvider) HasData(r Record) bool {
	return r.PriceUSD != nil || r.Change24HPercent != nil || r.MarketCapUSD != nil || r.MarketDataUpdatedAt != nil
}

func (p *enrichProvider) MissTTL() time.Duration                   { return p.service.missTTL }
func (p *enrichProvider) BatchLimit() int                          { return priceBatchSize }
func (p *enrichProvider) Matches(r Record, expected string) bool   { return r.CoinGeckoID == expected }
func (p *enrichProvider) FetchedAt(r Record) time.Time             { return r.FetchedAt }
func (p *enrichProvider) WithKey(r Record, k marketkey.Key) Record { r.Key = k; return r }
func (p *enrichProvider) TTL() time.Duration                       { return p.service.ttl }
func (p *enrichProvider) Now() time.Time                           { return p.service.now() }
func (p *enrichProvider) Logf(format string, args ...any) {
	p.service.logf("marketdata: "+format, args...)
}

func (s *Service) resolve(catalog *Catalog, tokens []wallet.Token) marketpipeline.Request[string] {
	request := marketpipeline.Request[string]{
		ExpectedID:   make(map[Key]string, len(tokens)),
		IndexesByKey: make(map[Key][]int, len(tokens)),
		KeyOrder:     make([]Key, 0, len(tokens)),
	}
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
				s.logf("marketdata: unresolved contract mapping source=cg chain=%q address=%q", key.Chain, key.TokenKey)
			}
		} else {
			key = ContractKey(s.platform, "")
			s.logf("marketdata: unresolved contract mapping source=cg chain=%q address=%q", key.Chain, key.TokenKey)
		}
		if !ok || strings.TrimSpace(id) == "" {
			continue
		}
		if _, exists := request.ExpectedID[key]; !exists {
			request.ExpectedID[key] = id
		}
		request.IndexesByKey[key] = append(request.IndexesByKey[key], index)
		if _, exists := seenKeys[key]; !exists {
			seenKeys[key] = struct{}{}
			request.KeyOrder = append(request.KeyOrder, key)
		}
	}
	return request
}

func (s *Service) clientPrices(ctx context.Context, ids []string) (map[string]coingecko.SimplePrice, error) {
	if s.client == nil {
		return nil, context.Canceled
	}
	return s.client.GetPrices(ctx, ids)
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
		tokens[index].Change24HPercent = ptr.Clone(record.Change24HPercent)
		tokens[index].MarketCapUSD = ptr.Clone(record.MarketCapUSD)
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
		tokens[index].Change24HPercent = ptr.Clone(record.Change24HPercent)
		tokens[index].MarketCapUSD = ptr.Clone(record.MarketCapUSD)
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
		out[i].TokenAddress = ptr.Clone(tokens[i].TokenAddress)
		out[i].LogoURI = ptr.Clone(tokens[i].LogoURI)
		out[i].CoinKey = ptr.Clone(tokens[i].CoinKey)
		out[i].PriceUSD = ptr.Clone(tokens[i].PriceUSD)
		out[i].Change24HPercent = ptr.Clone(tokens[i].Change24HPercent)
		out[i].MarketCapUSD = ptr.Clone(tokens[i].MarketCapUSD)
		out[i].MarketDataUpdatedAt = ptr.Clone(tokens[i].MarketDataUpdatedAt)
		if tokens[i].Price != nil {
			price := *tokens[i].Price
			out[i].Price = &price
		}
	}
	return out
}

func cloneMarketTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339)
	return &formatted
}

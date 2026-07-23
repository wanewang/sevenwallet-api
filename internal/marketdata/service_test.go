package marketdata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"wallet-api/internal/coingecko"
	"wallet-api/internal/wallet"
)

type fakePriceClient struct {
	mu        sync.Mutex
	batches   [][]string
	responses []map[string]coingecko.SimplePrice
	errors    []error
	handler   func(context.Context, []string) (map[string]coingecko.SimplePrice, error)
}

func (f *fakePriceClient) GetPrices(ctx context.Context, ids []string) (map[string]coingecko.SimplePrice, error) {
	f.mu.Lock()
	f.batches = append(f.batches, append([]string(nil), ids...))
	call := len(f.batches) - 1
	handler := f.handler
	var response map[string]coingecko.SimplePrice
	var err error
	if call < len(f.responses) {
		response = f.responses[call]
	}
	if call < len(f.errors) {
		err = f.errors[call]
	}
	f.mu.Unlock()
	if handler != nil {
		return handler(ctx, ids)
	}
	return response, err
}

type fakeMarketCache struct {
	mu       sync.Mutex
	loadKeys [][]Key
	loads    []map[Key]Record
	loadErr  error
	saves    [][]CacheWrite
	saveErr  error
}

func (f *fakeMarketCache) LoadMarketData(_ context.Context, keys []Key) (map[Key]Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loadKeys = append(f.loadKeys, append([]Key(nil), keys...))
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	if len(f.loads) == 0 {
		return map[Key]Record{}, nil
	}
	result := f.loads[0]
	f.loads = f.loads[1:]
	return result, nil
}

func (f *fakeMarketCache) SaveMarketData(_ context.Context, writes []CacheWrite) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saves = append(f.saves, append([]CacheWrite(nil), writes...))
	return f.saveErr
}

type fakeMarketStore struct {
	mu       sync.Mutex
	loadKeys [][]Key
	loads    []map[Key]Record
	loadErr  error
	saves    [][]Record
	saveErr  error
}

func (f *fakeMarketStore) LoadMarketData(_ context.Context, keys []Key) (map[Key]Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loadKeys = append(f.loadKeys, append([]Key(nil), keys...))
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	if len(f.loads) == 0 {
		return map[Key]Record{}, nil
	}
	result := f.loads[0]
	f.loads = f.loads[1:]
	return result, nil
}

func (f *fakeMarketStore) SaveMarketData(_ context.Context, records []Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saves = append(f.saves, append([]Record(nil), records...))
	return f.saveErr
}

func newServiceForTest(t *testing.T, cache *fakeMarketCache, store *fakeMarketStore, client *fakePriceClient, mappings []CoinMapping, nativeIDs []string, now time.Time) *Service {
	t.Helper()
	holder := &Holder{}
	holder.Set(NewCatalog(mappings))
	service := NewService(client, cache, store, holder, "ethereum", nativeIDs, 30*time.Minute, time.Second)
	service.now = func() time.Time { return now }
	service.logf = func(string, ...any) {}
	return service
}

func contractMapping(id, address string) CoinMapping {
	return CoinMapping{ID: id, Chain: "ethereum", Address: normalizePlatformAddress(address)}
}

func nativeMapping(id, symbol string) CoinMapping {
	return CoinMapping{ID: id, Symbol: symbol, Chain: strings.ToLower(symbol), Address: NativeAddress}
}

func contractToken(address, symbol string) wallet.Token {
	address = strings.ToLower(address)
	return wallet.Token{TokenAddress: &address, Symbol: symbol, PriceUSD: stringPtr("original")}
}

func nativeToken(symbol string) wallet.Token {
	return wallet.Token{Symbol: symbol, IsNative: true, PriceUSD: stringPtr("original")}
}

func stringPtr(value string) *string { return &value }

func floatPtr(value float64) *float64 { return &value }

func timePtr(value time.Time) *time.Time { return &value }

func price(value string) *wallet.Price {
	return &wallet.Price{Currency: "eur", Value: value, LastUpdatedAt: "old"}
}

func record(key Key, id string, fetchedAt time.Time, priceUSD *string, change, cap *float64, updated *time.Time) Record {
	return Record{Key: key, CoinGeckoID: id, PriceUSD: priceUSD, Change24HPercent: change, MarketCapUSD: cap, MarketDataUpdatedAt: updated, FetchedAt: fetchedAt}
}

func simplePrice(usd, change, cap string, updated int64) coingecko.SimplePrice {
	usdNumber := json.Number(usd)
	changeNumber := json.Number(change)
	capNumber := json.Number(cap)
	return coingecko.SimplePrice{USD: &usdNumber, USD24HChange: &changeNumber, USDMarketCap: &capNumber, LastUpdatedAt: &updated}
}

func TestEnrichFreshRedisHitSkipsPostgresAndCoinGecko(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	key := ContractKey("ethereum", "0xA0B8")
	fetchedAt := now.Add(-time.Minute)
	updated := now.Add(-30 * time.Second)
	cache := &fakeMarketCache{loads: []map[Key]Record{{key: record(key, "usd-coin", fetchedAt, stringPtr("3210.45"), floatPtr(-1.23), floatPtr(387123456789.45), timePtr(updated))}}}
	store := &fakeMarketStore{}
	client := &fakePriceClient{}
	service := newServiceForTest(t, cache, store, client, []CoinMapping{contractMapping("usd-coin", "0xA0B8")}, nil, now)
	original := contractToken("0xA0B8", "USDC")
	original.Price = price("old")

	got := service.EnrichTokens(context.Background(), []wallet.Token{original})

	if got[0].PriceUSD == nil || *got[0].PriceUSD != "3210.45" || got[0].Price == nil || got[0].Price.Value != "3210.45" {
		t.Fatalf("fresh Redis price = %#v, want exact market price", got[0])
	}
	if got[0].Change24HPercent == nil || *got[0].Change24HPercent != -1.23 || got[0].MarketCapUSD == nil || *got[0].MarketCapUSD != 387123456789.45 {
		t.Fatalf("fresh Redis optional fields = %#v", got[0])
	}
	if got[0].MarketDataUpdatedAt == nil || *got[0].MarketDataUpdatedAt != updated.Format(time.RFC3339) {
		t.Fatalf("updated at = %#v, want %q", got[0].MarketDataUpdatedAt, updated.Format(time.RFC3339))
	}
	if len(store.loadKeys) != 0 || len(client.batches) != 0 {
		t.Fatalf("downstream calls: postgres=%v CoinGecko=%v", store.loadKeys, client.batches)
	}
}

func TestEnrichFreshPostgresHitPromotesRemainingTTL(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 30, 0, 0, time.UTC)
	key := ContractKey("ethereum", "0xA0B8")
	fetchedAt := now.Add(-20 * time.Minute)
	cache := &fakeMarketCache{loads: []map[Key]Record{{}}}
	store := &fakeMarketStore{loads: []map[Key]Record{{key: record(key, "usd-coin", fetchedAt, stringPtr("10"), nil, nil, nil)}}}
	service := newServiceForTest(t, cache, store, &fakePriceClient{}, []CoinMapping{contractMapping("usd-coin", "0xA0B8")}, nil, now)

	got := service.EnrichTokens(context.Background(), []wallet.Token{contractToken("0xA0B8", "USDC")})

	if got[0].PriceUSD == nil || *got[0].PriceUSD != "10" {
		t.Fatalf("price = %#v, want fresh PostgreSQL price", got[0].PriceUSD)
	}
	if len(cache.saves) != 1 || len(cache.saves[0]) != 1 || cache.saves[0][0].TTL != 10*time.Minute {
		t.Fatalf("promotion writes = %#v, want one write with 10m TTL", cache.saves)
	}
}

func TestEnrichStaleMatchingRecordUsesOnlyNonPriceFallback(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	key := ContractKey("ethereum", "0xA0B8")
	updated := now.Add(-time.Hour)
	cache := &fakeMarketCache{loads: []map[Key]Record{{key: record(key, "usd-coin", now.Add(-time.Hour), stringPtr("999"), floatPtr(-4.5), floatPtr(12), timePtr(updated))}}}
	client := &fakePriceClient{errors: []error{errors.New("rate limited")}}
	service := newServiceForTest(t, cache, &fakeMarketStore{}, client, []CoinMapping{contractMapping("usd-coin", "0xA0B8")}, nil, now)
	token := contractToken("0xA0B8", "USDC")
	token.Price = price("original")

	got := service.EnrichTokens(context.Background(), []wallet.Token{token})

	if got[0].PriceUSD == nil || *got[0].PriceUSD != "original" || got[0].Price == nil || got[0].Price.Value != "original" {
		t.Fatalf("stale record changed price: %#v", got[0])
	}
	if got[0].Change24HPercent == nil || *got[0].Change24HPercent != -4.5 || got[0].MarketCapUSD == nil || *got[0].MarketCapUSD != 12 || got[0].MarketDataUpdatedAt == nil {
		t.Fatalf("stale fallback = %#v", got[0])
	}
}

func TestEnrichStaleMismatchedRecordIsDiscarded(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	key := ContractKey("ethereum", "0xA0B8")
	wrongKeyRecord := record(key, "wrong-id", now.Add(-time.Hour), stringPtr("999"), floatPtr(-4.5), floatPtr(12), timePtr(now.Add(-time.Hour)))
	cache := &fakeMarketCache{loads: []map[Key]Record{{key: wrongKeyRecord}}}
	client := &fakePriceClient{errors: []error{errors.New("unavailable")}}
	service := newServiceForTest(t, cache, &fakeMarketStore{}, client, []CoinMapping{contractMapping("usd-coin", "0xA0B8")}, nil, now)

	got := service.EnrichTokens(context.Background(), []wallet.Token{contractToken("0xA0B8", "USDC")})

	if got[0].PriceUSD == nil || *got[0].PriceUSD != "original" || got[0].Change24HPercent != nil || got[0].MarketCapUSD != nil || got[0].MarketDataUpdatedAt != nil {
		t.Fatalf("mismatched stale record applied: %#v", got[0])
	}
}

func TestEnrichFetchedPriceOverwritesOriginalImmediately(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	cache := &fakeMarketCache{loads: []map[Key]Record{{}}, saveErr: errors.New("redis write failed")}
	store := &fakeMarketStore{loads: []map[Key]Record{{}}, saveErr: errors.New("postgres write failed")}
	client := &fakePriceClient{responses: []map[string]coingecko.SimplePrice{{"usd-coin": simplePrice("3210.45", "-1.23", "387123456789.45", 1784781600)}}}
	service := newServiceForTest(t, cache, store, client, []CoinMapping{contractMapping("usd-coin", "0xA0B8")}, nil, now)

	got := service.EnrichTokens(context.Background(), []wallet.Token{contractToken("0xA0B8", "USDC")})

	if got[0].PriceUSD == nil || *got[0].PriceUSD != "3210.45" || got[0].Price == nil || got[0].Price.Value != "3210.45" {
		t.Fatalf("fetched price = %#v", got[0])
	}
	if len(store.saves) != 1 || len(cache.saves) != 1 {
		t.Fatalf("independent persistence calls: postgres=%v redis=%v", store.saves, cache.saves)
	}
}

func TestEnrichOutOfFloatRangeUSDIsPreservedExactly(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	smallUSD := json.Number("1e-400")
	largeUSD := json.Number("1e400")
	client := &fakePriceClient{responses: []map[string]coingecko.SimplePrice{{
		"small": {USD: &smallUSD},
		"large": {USD: &largeUSD},
	}}}
	service := newServiceForTest(t, &fakeMarketCache{loads: []map[Key]Record{{}}}, &fakeMarketStore{loads: []map[Key]Record{{}}}, client, []CoinMapping{
		contractMapping("small", "0xA0B8"),
		contractMapping("large", "0xB0B8"),
	}, nil, now)
	tokens := []wallet.Token{contractToken("0xA0B8", "SMALL"), contractToken("0xB0B8", "LARGE")}

	got := service.EnrichTokens(context.Background(), tokens)

	want := []string{"1e-400", "1e400"}
	for i, value := range want {
		if got[i].PriceUSD == nil || *got[i].PriceUSD != value || got[i].Price == nil || got[i].Price.Value != value {
			t.Fatalf("token %d USD = %#v, want exact %q", i, got[i], value)
		}
	}
	if len(service.store.(*fakeMarketStore).saves) != 1 || len(service.store.(*fakeMarketStore).saves[0]) != 2 {
		t.Fatalf("persisted records = %#v, want both fetched prices", service.store.(*fakeMarketStore).saves)
	}
	wantByID := map[string]string{"small": want[0], "large": want[1]}
	for _, persisted := range service.store.(*fakeMarketStore).saves[0] {
		if persisted.PriceUSD == nil || *persisted.PriceUSD != wantByID[persisted.CoinGeckoID] {
			t.Fatalf("persisted record %q USD = %#v, want exact %q", persisted.CoinGeckoID, persisted.PriceUSD, wantByID[persisted.CoinGeckoID])
		}
	}
}

func TestEnrichFreshRecordWithoutUSDDoesNotEraseOriginalPrice(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	key := ContractKey("ethereum", "0xA0B8")
	updated := now.Add(-time.Minute)
	cache := &fakeMarketCache{loads: []map[Key]Record{{key: record(key, "usd-coin", updated, nil, floatPtr(2), floatPtr(3), timePtr(updated))}}}
	service := newServiceForTest(t, cache, &fakeMarketStore{}, &fakePriceClient{}, []CoinMapping{contractMapping("usd-coin", "0xA0B8")}, nil, now)
	token := contractToken("0xA0B8", "USDC")
	token.Price = price("original")

	got := service.EnrichTokens(context.Background(), []wallet.Token{token})

	if got[0].PriceUSD == nil || *got[0].PriceUSD != "original" || got[0].Price == nil || got[0].Price.Value != "original" {
		t.Fatalf("fresh record without USD erased price: %#v", got[0])
	}
	if got[0].Change24HPercent == nil || *got[0].Change24HPercent != 2 || got[0].MarketCapUSD == nil || *got[0].MarketCapUSD != 3 {
		t.Fatalf("fresh non-price fields = %#v", got[0])
	}
}

func TestEnrichPartialResponseUsesStaleForOmittedID(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	keyA := ContractKey("ethereum", "0xA0B8")
	keyB := ContractKey("ethereum", "0xB0B8")
	staleAt := now.Add(-time.Hour)
	cache := &fakeMarketCache{loads: []map[Key]Record{{
		keyA: record(keyA, "id-a", staleAt, stringPtr("old-a"), floatPtr(1), floatPtr(2), timePtr(staleAt)),
		keyB: record(keyB, "id-b", staleAt, stringPtr("old-b"), floatPtr(3), floatPtr(4), timePtr(staleAt)),
	}}}
	client := &fakePriceClient{responses: []map[string]coingecko.SimplePrice{{"id-a": simplePrice("5.00", "5", "6", 1784781600)}}}
	service := newServiceForTest(t, cache, &fakeMarketStore{}, client, []CoinMapping{contractMapping("id-a", "0xA0B8"), contractMapping("id-b", "0xB0B8")}, nil, now)
	tokens := []wallet.Token{contractToken("0xA0B8", "A"), contractToken("0xB0B8", "B")}
	tokens[1].Price = price("original-b")

	got := service.EnrichTokens(context.Background(), tokens)

	if got[0].PriceUSD == nil || *got[0].PriceUSD != "5.00" || got[0].Change24HPercent == nil || *got[0].Change24HPercent != 5 {
		t.Fatalf("returned ID did not become fresh: %#v", got[0])
	}
	if got[1].PriceUSD == nil || *got[1].PriceUSD != "original" || got[1].Price == nil || got[1].Price.Value != "original-b" || got[1].Change24HPercent == nil || *got[1].Change24HPercent != 3 {
		t.Fatalf("omitted ID stale fallback = %#v", got[1])
	}
}

func TestEnrichBatchesSortedIDsAtMost100(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	mappings := make([]CoinMapping, 101)
	tokens := make([]wallet.Token, 101)
	for i := range mappings {
		address := fmt.Sprintf("0x%040x", i+1)
		mappings[i] = contractMapping(fmt.Sprintf("id-%03d", i), address)
		tokens[i] = contractToken(address, "T")
	}
	client := &fakePriceClient{responses: []map[string]coingecko.SimplePrice{{}, {}}}
	service := newServiceForTest(t, &fakeMarketCache{loads: []map[Key]Record{{}}}, &fakeMarketStore{loads: []map[Key]Record{{}}}, client, mappings, nil, now)

	service.EnrichTokens(context.Background(), tokens)

	if len(client.batches) != 2 || len(client.batches[0]) != 100 || len(client.batches[1]) != 1 {
		t.Fatalf("batches = %v", client.batches)
	}
	if !sort.StringsAreSorted(client.batches[0]) || client.batches[1][0] != "id-100" {
		t.Fatalf("batches are not deterministic: %v", client.batches)
	}
}

func TestEnrichDeduplicatesRepeatedKeysAndCoinGeckoIDs(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	keyA := ContractKey("ethereum", "0xA0B8")
	keyB := ContractKey("ethereum", "0xB0B8")
	cache := &fakeMarketCache{loads: []map[Key]Record{{}}}
	client := &fakePriceClient{responses: []map[string]coingecko.SimplePrice{{"same-id": simplePrice("2", "", "", 1)}}}
	service := newServiceForTest(t, cache, &fakeMarketStore{loads: []map[Key]Record{{}}}, client, []CoinMapping{contractMapping("same-id", "0xA0B8"), contractMapping("same-id", "0xB0B8")}, nil, now)

	got := service.EnrichTokens(context.Background(), []wallet.Token{contractToken("0xA0B8", "A"), contractToken("0xA0B8", "A"), contractToken("0xB0B8", "B")})

	if len(cache.loadKeys) != 1 || !reflect.DeepEqual(cache.loadKeys[0], []Key{keyA, keyB}) {
		t.Fatalf("Redis keys = %v", cache.loadKeys)
	}
	if len(client.batches) != 1 || !reflect.DeepEqual(client.batches[0], []string{"same-id"}) {
		t.Fatalf("CoinGecko IDs = %v", client.batches)
	}
	for i := range got {
		if got[i].PriceUSD == nil || *got[i].PriceUSD != "2" {
			t.Fatalf("token %d = %#v", i, got[i])
		}
	}
}

func TestEnrichNativeResolutionUsesNativeKeyAndAllowedIDs(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	key := NativeKey("ethereum", "ETH")
	cache := &fakeMarketCache{loads: []map[Key]Record{{}}}
	client := &fakePriceClient{responses: []map[string]coingecko.SimplePrice{{"ethereum": simplePrice("2", "", "", 1)}}}
	service := newServiceForTest(t, cache, &fakeMarketStore{loads: []map[Key]Record{{}}}, client, []CoinMapping{nativeMapping("ethereum", "ETH")}, []string{"ethereum"}, now)

	service.EnrichTokens(context.Background(), []wallet.Token{nativeToken("eth")})

	if len(cache.loadKeys) != 1 || !reflect.DeepEqual(cache.loadKeys[0], []Key{key}) {
		t.Fatalf("native Redis key = %v, want %v", cache.loadKeys, key)
	}
	if len(client.batches) != 1 || !reflect.DeepEqual(client.batches[0], []string{"ethereum"}) {
		t.Fatalf("native CoinGecko IDs = %v", client.batches)
	}
}

func TestEnrichAmbiguousMappingSkipsCacheAndCoinGecko(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	cache := &fakeMarketCache{}
	client := &fakePriceClient{}
	service := newServiceForTest(t, cache, &fakeMarketStore{}, client, []CoinMapping{nativeMapping("first", "ETH"), nativeMapping("second", "ETH")}, []string{"first", "second"}, now)

	got := service.EnrichTokens(context.Background(), []wallet.Token{nativeToken("ETH")})

	if len(cache.loadKeys) != 0 || len(client.batches) != 0 || got[0].PriceUSD == nil || *got[0].PriceUSD != "original" {
		t.Fatalf("ambiguous mapping performed enrichment: cache=%v client=%v token=%#v", cache.loadKeys, client.batches, got[0])
	}
}

func TestEnrichRedisReadErrorFallsThroughToPostgres(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	key := ContractKey("ethereum", "0xA0B8")
	cache := &fakeMarketCache{loadErr: errors.New("redis down")}
	store := &fakeMarketStore{loads: []map[Key]Record{{key: record(key, "usd-coin", now.Add(-time.Minute), stringPtr("10"), nil, nil, nil)}}}
	client := &fakePriceClient{}
	service := newServiceForTest(t, cache, store, client, []CoinMapping{contractMapping("usd-coin", "0xA0B8")}, nil, now)

	got := service.EnrichTokens(context.Background(), []wallet.Token{contractToken("0xA0B8", "USDC")})

	if len(store.loadKeys) != 1 || got[0].PriceUSD == nil || *got[0].PriceUSD != "10" || len(client.batches) != 0 {
		t.Fatalf("Redis fallback failed: store=%v token=%#v client=%v", store.loadKeys, got[0], client.batches)
	}
}

func TestEnrichPostgresReadErrorFetchesWithoutStaleFallback(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	client := &fakePriceClient{errors: []error{errors.New("CoinGecko down")}}
	service := newServiceForTest(t, &fakeMarketCache{loads: []map[Key]Record{{}}}, &fakeMarketStore{loadErr: errors.New("postgres down")}, client, []CoinMapping{contractMapping("usd-coin", "0xA0B8")}, nil, now)
	token := contractToken("0xA0B8", "USDC")
	token.Change24HPercent = floatPtr(8)

	got := service.EnrichTokens(context.Background(), []wallet.Token{token})

	if len(client.batches) != 1 || got[0].Change24HPercent == nil || *got[0].Change24HPercent != 8 || got[0].MarketCapUSD != nil || got[0].MarketDataUpdatedAt != nil {
		t.Fatalf("Postgres error incorrectly supplied fallback: batches=%v token=%#v", client.batches, got[0])
	}
}

func TestEnrichDeadlineCancelsBlockingClient(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	started := make(chan struct{})
	client := &fakePriceClient{handler: func(ctx context.Context, _ []string) (map[string]coingecko.SimplePrice, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	service := newServiceForTest(t, &fakeMarketCache{loads: []map[Key]Record{{}}}, &fakeMarketStore{loads: []map[Key]Record{{}}}, client, []CoinMapping{contractMapping("usd-coin", "0xA0B8")}, nil, now)
	service.enrichTimeout = 20 * time.Millisecond

	done := make(chan []wallet.Token, 1)
	go func() {
		done <- service.EnrichTokens(context.Background(), []wallet.Token{contractToken("0xA0B8", "USDC")})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("client was not called")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("enrichment did not stop after deadline")
	}
}

func TestEnrichFirstBatchRemainsAppliedWhenLaterBatchHitsDeadline(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	mappings := make([]CoinMapping, 101)
	tokens := make([]wallet.Token, 101)
	for i := range mappings {
		address := fmt.Sprintf("0x%040x", i+1)
		mappings[i] = contractMapping(fmt.Sprintf("id-%03d", i), address)
		tokens[i] = contractToken(address, "T")
	}
	client := &fakePriceClient{handler: func(ctx context.Context, ids []string) (map[string]coingecko.SimplePrice, error) {
		if len(ids) == 1 {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		result := make(map[string]coingecko.SimplePrice, len(ids))
		for _, id := range ids {
			result[id] = simplePrice("1000.00", "", "", 1)
		}
		return result, nil
	}}
	cache := &fakeMarketCache{loads: []map[Key]Record{{}}}
	store := &fakeMarketStore{loads: []map[Key]Record{{}}}
	service := newServiceForTest(t, cache, store, client, mappings, nil, now)
	service.enrichTimeout = 20 * time.Millisecond

	got := service.EnrichTokens(context.Background(), tokens)

	if got[0].PriceUSD == nil || *got[0].PriceUSD != "1000.00" || got[99].PriceUSD == nil || *got[99].PriceUSD != "1000.00" {
		t.Fatalf("first batch was not applied: first=%#v last=%#v", got[0], got[99])
	}
	if got[100].PriceUSD == nil || *got[100].PriceUSD != "original" {
		t.Fatalf("later batch unexpectedly applied: %#v", got[100])
	}
	if len(store.saves) != 1 || len(store.saves[0]) != 100 || len(cache.saves) != 1 || len(cache.saves[0]) != 100 {
		t.Fatalf("first batch persistence = postgres=%d redis=%d", len(store.saves), len(cache.saves))
	}
}

func TestEnrichInvalidOptionalNumbersAreNilAndLogged(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	badChange := json.Number("not-a-number")
	badCap := json.Number("also-not-a-number")
	usd := json.Number("3210.45")
	client := &fakePriceClient{responses: []map[string]coingecko.SimplePrice{{"usd-coin": {USD: &usd, USD24HChange: &badChange, USDMarketCap: &badCap}}}}
	service := newServiceForTest(t, &fakeMarketCache{loads: []map[Key]Record{{}}}, &fakeMarketStore{loads: []map[Key]Record{{}}}, client, []CoinMapping{contractMapping("usd-coin", "0xA0B8")}, nil, now)
	var logs []string
	service.logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }

	got := service.EnrichTokens(context.Background(), []wallet.Token{contractToken("0xA0B8", "USDC")})

	if got[0].PriceUSD == nil || *got[0].PriceUSD != "3210.45" || got[0].Change24HPercent != nil || got[0].MarketCapUSD != nil {
		t.Fatalf("invalid optional numbers = %#v", got[0])
	}
	if len(logs) != 2 || !strings.Contains(logs[0], "usd-coin") || !strings.Contains(logs[1], "usd-coin") {
		t.Fatalf("invalid-number logs = %v", logs)
	}
}

func TestEnrichClonesInputAndHandlesNilOrEmptySafely(t *testing.T) {
	service := &Service{}
	if got := service.EnrichTokens(context.Background(), nil); got != nil {
		t.Fatalf("nil input = %#v, want nil", got)
	}
	if got := service.EnrichTokens(context.Background(), []wallet.Token{}); got == nil || len(got) != 0 {
		t.Fatalf("empty input = %#v", got)
	}

	address := "0xA0B8"
	input := []wallet.Token{{TokenAddress: &address, Price: &wallet.Price{Currency: "usd", Value: "1"}}}
	got := service.EnrichTokens(context.Background(), input)
	if &got[0] == &input[0] || got[0].TokenAddress == input[0].TokenAddress || got[0].Price == input[0].Price {
		t.Fatalf("output was not cloned: input=%p output=%p", &input[0], &got[0])
	}
	*got[0].TokenAddress = "changed"
	got[0].Price.Value = "changed"
	if *input[0].TokenAddress != "0xA0B8" || input[0].Price.Value != "1" {
		t.Fatalf("mutating output changed input: %#v", input)
	}
}

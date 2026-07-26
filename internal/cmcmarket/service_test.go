package cmcmarket

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"wallet-api/internal/coinmarketcap"
	"wallet-api/internal/marketdata"
	"wallet-api/internal/wallet"
)

type fakeCMCPriceClient struct {
	mu      sync.Mutex
	batches [][]int64
	handler func(context.Context, []int64) (map[int64]coinmarketcap.SimplePrice, error)
}

func (f *fakeCMCPriceClient) GetPrices(ctx context.Context, ids []int64) (map[int64]coinmarketcap.SimplePrice, error) {
	f.mu.Lock()
	f.batches = append(f.batches, append([]int64(nil), ids...))
	f.mu.Unlock()
	if f.handler != nil {
		return f.handler(ctx, ids)
	}
	return map[int64]coinmarketcap.SimplePrice{}, nil
}

type fakeCMCCache struct {
	loads   map[marketdata.Key]Record
	loadErr error
	saves   [][]CacheWrite
}

func (f *fakeCMCCache) LoadCoinMarketCapMarketData(context.Context, []marketdata.Key) (map[marketdata.Key]Record, error) {
	return f.loads, f.loadErr
}
func (f *fakeCMCCache) SaveCoinMarketCapMarketData(_ context.Context, writes []CacheWrite) error {
	f.saves = append(f.saves, append([]CacheWrite(nil), writes...))
	return nil
}

type fakeCMCStore struct {
	loads map[marketdata.Key]Record
	saves [][]Record
}

func (f *fakeCMCStore) LoadCoinMarketCapMarketData(context.Context, []marketdata.Key) (map[marketdata.Key]Record, error) {
	return f.loads, nil
}
func (f *fakeCMCStore) SaveCoinMarketCapMarketData(_ context.Context, records []Record) error {
	f.saves = append(f.saves, append([]Record(nil), records...))
	return nil
}

func cmcService(now time.Time, mappings []CoinMapping, client *fakeCMCPriceClient, cache *fakeCMCCache, store *fakeCMCStore) *Service {
	holder := &Holder{}
	holder.Set(NewCatalog(mappings))
	s := NewService(client, cache, store, nil, holder, 1, 1027, "ethereum", 30*time.Minute, 2*time.Hour)
	s.now = func() time.Time { return now }
	s.logf = func(string, ...any) {}
	return s
}

func cmcToken(address, symbol string) wallet.Token {
	return wallet.Token{TokenAddress: &address, Symbol: symbol}
}

func cmcPrice(id int64, price, change string) coinmarketcap.SimplePrice {
	p := json.Number(price)
	c := json.Number(change)
	return coinmarketcap.SimplePrice{ID: id, Price: &p, PercentChange24H: &c}
}

func TestLookupFreshUsesRedisAndRejectsMismatchedID(t *testing.T) {
	now := time.Now().UTC()
	key := marketdata.ContractKey("ethereum", "0xabc")
	price := "1.25"
	cache := &fakeCMCCache{loads: map[marketdata.Key]Record{
		key: {Key: key, CoinMarketCapID: 999, PriceUSD: &price, FetchedAt: now},
	}}
	client := &fakeCMCPriceClient{handler: func(_ context.Context, ids []int64) (map[int64]coinmarketcap.SimplePrice, error) {
		return map[int64]coinmarketcap.SimplePrice{1660: cmcPrice(1660, "2.50", "-1.5")}, nil
	}}
	s := cmcService(now, []CoinMapping{{ID: 1660, PlatformID: 1, Address: "0xabc"}}, client, cache, &fakeCMCStore{})

	got := s.LookupFresh(context.Background(), []wallet.Token{cmcToken("0xABC", "TKN")})
	if got[0] == nil || got[0].ID != 1660 || got[0].PriceUSD == nil || *got[0].PriceUSD != "2.50" {
		t.Fatalf("result = %#v", got)
	}
	if len(client.batches) != 1 || !reflect.DeepEqual(client.batches[0], []int64{1660}) {
		t.Fatalf("batches = %#v", client.batches)
	}
}

func TestLookupFreshRedisHitSkipsDownstream(t *testing.T) {
	now := time.Now().UTC()
	key := marketdata.ContractKey("ethereum", "0xabc")
	price := "1.25"
	cache := &fakeCMCCache{loads: map[marketdata.Key]Record{
		key: {Key: key, CoinMarketCapID: 1660, PriceUSD: &price, FetchedAt: now.Add(-time.Minute)},
	}}
	client := &fakeCMCPriceClient{}
	store := &fakeCMCStore{}
	s := cmcService(now, []CoinMapping{{ID: 1660, PlatformID: 1, Address: "0xabc"}}, client, cache, store)
	got := s.LookupFresh(context.Background(), []wallet.Token{cmcToken("0xabc", "TKN")})
	if got[0] == nil || got[0].PriceUSD == nil || *got[0].PriceUSD != price || len(client.batches) != 0 || len(store.saves) != 0 {
		t.Fatalf("result=%#v batches=%#v saves=%#v", got, client.batches, store.saves)
	}
}

func TestLookupFreshPartialResponseUpdatesOnlyReturnedIDAndDeduplicates(t *testing.T) {
	now := time.Now().UTC()
	client := &fakeCMCPriceClient{handler: func(_ context.Context, _ []int64) (map[int64]coinmarketcap.SimplePrice, error) {
		return map[int64]coinmarketcap.SimplePrice{10: cmcPrice(10, "10", "1")}, nil
	}}
	s := cmcService(now, []CoinMapping{
		{ID: 10, PlatformID: 1, Address: "0xaaa"},
		{ID: 20, PlatformID: 1, Address: "0xbbb"},
	}, client, &fakeCMCCache{}, &fakeCMCStore{})
	tokens := []wallet.Token{cmcToken("0xaaa", "A"), cmcToken("0xaaa", "A"), cmcToken("0xbbb", "B")}
	got := s.LookupFresh(context.Background(), tokens)
	if len(client.batches) != 1 || !reflect.DeepEqual(client.batches[0], []int64{10, 20}) {
		t.Fatalf("batches = %#v", client.batches)
	}
	if got[0] == nil || got[1] == nil || got[2] != nil {
		t.Fatalf("results = %#v", got)
	}
}

func TestLookupFreshPromotesPostgresWithRemainingTTL(t *testing.T) {
	now := time.Now().UTC()
	key := marketdata.ContractKey("ethereum", "0xabc")
	price := "3"
	store := &fakeCMCStore{loads: map[marketdata.Key]Record{
		key: {Key: key, CoinMarketCapID: 1660, PriceUSD: &price, FetchedAt: now.Add(-20 * time.Minute)},
	}}
	cache := &fakeCMCCache{loads: map[marketdata.Key]Record{}}
	s := cmcService(now, []CoinMapping{{ID: 1660, PlatformID: 1, Address: "0xabc"}}, &fakeCMCPriceClient{}, cache, store)

	got := s.LookupFresh(context.Background(), []wallet.Token{cmcToken("0xabc", "TKN")})
	if got[0] == nil || len(cache.saves) != 1 || cache.saves[0][0].TTL != 10*time.Minute {
		t.Fatalf("result=%#v promotions=%#v", got, cache.saves)
	}
}

func TestLookupFreshNeverReturnsExpiredDataOrEmptyProvider(t *testing.T) {
	now := time.Now().UTC()
	key := marketdata.ContractKey("ethereum", "0xabc")
	price := "stale"
	cache := &fakeCMCCache{loads: map[marketdata.Key]Record{
		key: {Key: key, CoinMarketCapID: 1660, PriceUSD: &price, FetchedAt: now.Add(-time.Hour)},
	}}
	s := cmcService(now, []CoinMapping{{ID: 1660, PlatformID: 1, Address: "0xabc"}}, &fakeCMCPriceClient{}, cache, &fakeCMCStore{})
	if got := s.LookupFresh(context.Background(), []wallet.Token{cmcToken("0xabc", "TKN")}); got[0] != nil {
		t.Fatalf("expired data returned: %#v", got[0])
	}
}

func TestLookupFreshNativeAndSequentialFiftyIDBatches(t *testing.T) {
	now := time.Now().UTC()
	mappings := make([]CoinMapping, 51)
	tokens := make([]wallet.Token, 52)
	tokens[0] = wallet.Token{Symbol: "ETH", IsNative: true}
	for i := range mappings {
		address := fmt.Sprintf("0x%040x", i+1)
		mappings[i] = CoinMapping{ID: int64(2000 + i), PlatformID: 1, Address: address}
		tokens[i+1] = cmcToken(address, "T")
	}
	client := &fakeCMCPriceClient{handler: func(_ context.Context, ids []int64) (map[int64]coinmarketcap.SimplePrice, error) {
		out := make(map[int64]coinmarketcap.SimplePrice, len(ids))
		for _, id := range ids {
			out[id] = cmcPrice(id, "1e-400", "1")
		}
		return out, nil
	}}
	s := cmcService(now, mappings, client, &fakeCMCCache{}, &fakeCMCStore{})

	got := s.LookupFresh(context.Background(), tokens)
	if len(client.batches) != 2 || len(client.batches[0]) != 50 || len(client.batches[1]) != 2 {
		t.Fatalf("batch sizes = %v", client.batches)
	}
	if !sort.SliceIsSorted(client.batches[0], func(i, j int) bool { return client.batches[0][i] < client.batches[0][j] }) {
		t.Fatalf("batch not sorted: %v", client.batches[0])
	}
	if got[0] == nil || got[0].ID != 1027 || got[51] == nil || got[51].PriceUSD == nil || *got[51].PriceUSD != "1e-400" {
		t.Fatalf("results = native:%#v last:%#v", got[0], got[51])
	}
}

func TestLookupFreshCatalogSymbolAmbiguitySkipsSameSymbol(t *testing.T) {
	now := time.Now().UTC()
	s := cmcService(now, []CoinMapping{
		{ID: 10, Symbol: "TKN", PlatformID: 1, Address: "0xabc"},
		{ID: 20, Symbol: "tkn", PlatformID: 1, Address: "0xabc"},
	}, &fakeCMCPriceClient{}, &fakeCMCCache{}, &fakeCMCStore{})
	var logs []string
	s.logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	got := s.LookupFresh(context.Background(), []wallet.Token{cmcToken("0xabc", "TKN")})
	if got[0] != nil || len(s.client.(*fakeCMCPriceClient).batches) != 0 {
		t.Fatalf("ambiguous mapping was fetched: %#v", got)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], `source=cmc chain="ethereum" address="0xabc"`) {
		t.Fatalf("logs = %#v", logs)
	}
}

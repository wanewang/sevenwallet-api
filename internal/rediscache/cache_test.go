package rediscache

import (
	"context"
	"os"
	"testing"
	"time"

	"wallet-api/internal/cmcmarket"
	"wallet-api/internal/lifi"
	"wallet-api/internal/marketdata"
	"wallet-api/internal/tokenvalidity"
)

func newTestCache(t *testing.T) *Cache {
	t.Helper()
	url := os.Getenv("WALLET_TEST_REDIS_URL")
	if url == "" {
		t.Skip("set WALLET_TEST_REDIS_URL to run redis integration tests")
	}
	c, err := New(url, time.Hour)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	_, _ = c.client.Del(ctx, key("ETH")).Result()
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func deleteCMCMarketKeys(t *testing.T, c *Cache, keys ...marketdata.Key) {
	t.Helper()
	redisKeys := make([]string, 0, len(keys))
	for _, key := range keys {
		redisKeys = append(redisKeys, cmcMarketKey(key))
	}
	if len(redisKeys) > 0 {
		if err := c.client.Del(context.Background(), redisKeys...).Err(); err != nil {
			t.Fatalf("delete CMC keys: %v", err)
		}
		t.Cleanup(func() { _ = c.client.Del(context.Background(), redisKeys...).Err() })
	}
}

func deleteMarketKeys(t *testing.T, c *Cache, keys ...marketdata.Key) {
	t.Helper()
	ctx := context.Background()
	redisKeys := make([]string, 0, len(keys))
	for _, k := range keys {
		redisKeys = append(redisKeys, marketKey(k))
	}
	if len(redisKeys) > 0 {
		if err := c.client.Del(ctx, redisKeys...).Err(); err != nil {
			t.Fatalf("delete market keys: %v", err)
		}
		t.Cleanup(func() { _ = c.client.Del(context.Background(), redisKeys...).Err() })
	}
}

func TestRedisSaveAndLoadTokenList(t *testing.T) {
	c := newTestCache(t)
	ctx := context.Background()

	if _, _, ok, err := c.LoadTokenList(ctx, "ETH"); err != nil || ok {
		t.Fatalf("empty load: ok=%v err=%v", ok, err)
	}

	fetched := time.Now().UTC().Truncate(time.Second)
	tokens := []lifi.ListToken{{Address: "0xA0B8", Symbol: "USDC", Decimals: 6, PriceUSD: "1.00"}}
	if err := c.SaveTokenList(ctx, "ETH", tokens, fetched); err != nil {
		t.Fatalf("SaveTokenList: %v", err)
	}
	got, gotAt, ok, err := c.LoadTokenList(ctx, "ETH")
	if err != nil || !ok {
		t.Fatalf("LoadTokenList ok=%v err=%v", ok, err)
	}
	if len(got) != 1 || got[0].Symbol != "USDC" || got[0].PriceUSD != "1.00" {
		t.Errorf("round-trip wrong: %+v", got)
	}
	if !gotAt.Equal(fetched) {
		t.Errorf("fetchedAt = %v, want %v", gotAt, fetched)
	}
}

func TestRedisSaveAndLoadTokenMeta(t *testing.T) {
	c := newTestCache(t)
	ctx := context.Background()
	_, _ = c.client.Del(ctx, metaKey("eth", "0xfee7")).Result()

	if _, ok, err := c.LoadTokenMeta(ctx, "eth", "0xFEE7"); err != nil || ok {
		t.Fatalf("empty load: ok=%v err=%v", ok, err)
	}
	at := time.Now().UTC().Truncate(time.Second)
	r := tokenvalidity.Record{Verified: true, Symbol: "PEPE", Logo: "L", Decimals: 18, FetchedAt: at}
	if err := c.SaveTokenMeta(ctx, "eth", "0xFEE7", r, time.Hour); err != nil {
		t.Fatalf("SaveTokenMeta: %v", err)
	}
	got, ok, err := c.LoadTokenMeta(ctx, "eth", "0xFEE7")
	if err != nil || !ok {
		t.Fatalf("LoadTokenMeta ok=%v err=%v", ok, err)
	}
	if got.Symbol != "PEPE" || got.Logo != "L" || got.Decimals != 18 || !got.Verified {
		t.Errorf("round-trip wrong: %+v", got)
	}
	if !got.FetchedAt.Equal(at) {
		t.Errorf("fetchedAt = %v, want %v", got.FetchedAt, at)
	}
}

func TestMarketDataBatchRoundTripAndTTL(t *testing.T) {
	c := newTestCache(t)
	ctx := context.Background()
	key1 := marketdata.ContractKey("ethereum", "0xA0B8")
	key2 := marketdata.NativeKey("Ethereum", "ETH")
	missing := marketdata.ContractKey("ethereum", "0xmissing")
	deleteMarketKeys(t, c, key1, key2, missing)

	price := "1.0001"
	change := -0.25
	capUSD := 32_000_000_000.0
	updatedAt := time.Unix(1_784_781_600, 0).UTC()
	fetchedAt := time.Unix(2_000, 0).UTC()
	first := marketdata.Record{
		Key: key1, CoinGeckoID: "usd-coin", PriceUSD: &price, Change24HPercent: &change,
		MarketCapUSD: &capUSD, MarketDataUpdatedAt: &updatedAt, FetchedAt: fetchedAt,
	}
	second := marketdata.Record{Key: key2, CoinGeckoID: "ethereum", FetchedAt: fetchedAt}
	if err := c.SaveMarketData(ctx, []marketdata.CacheWrite{{Record: first, TTL: 10 * time.Minute}, {Record: second, TTL: 10 * time.Minute}}); err != nil {
		t.Fatalf("SaveMarketData: %v", err)
	}

	ttl, err := c.client.TTL(ctx, marketKey(key1)).Result()
	if err != nil {
		t.Fatalf("TTL: %v", err)
	}
	if ttl <= 9*time.Minute || ttl > 10*time.Minute {
		t.Fatalf("market TTL = %v, want about 10m and not the LI.FI safety TTL", ttl)
	}

	newPrice := "1.0002"
	first.PriceUSD = &newPrice
	first.CoinGeckoID = "usd-coin-updated"
	if err := c.SaveMarketData(ctx, []marketdata.CacheWrite{{Record: first, TTL: 10 * time.Minute}}); err != nil {
		t.Fatalf("SaveMarketData upsert: %v", err)
	}
	got, err := c.LoadMarketData(ctx, []marketdata.Key{key1, key2, missing})
	if err != nil {
		t.Fatalf("LoadMarketData: %v", err)
	}
	if len(got) != 2 || got[key1].CoinGeckoID != "usd-coin-updated" || got[key1].PriceUSD == nil || *got[key1].PriceUSD != newPrice {
		t.Fatalf("loaded market data = %#v", got)
	}
	if got[key2].PriceUSD != nil || got[key2].MarketDataUpdatedAt != nil {
		t.Fatalf("nullable record = %#v", got[key2])
	}
}

func TestMarketDataOlderRedisWriteCannotReplaceOrExtendNewerRecord(t *testing.T) {
	c := newTestCache(t)
	ctx := context.Background()
	key := marketdata.ContractKey("ethereum", "0xA0B8")
	deleteMarketKeys(t, c, key)
	newPrice := "2.00"
	oldPrice := "1.00"
	newer := marketdata.Record{Key: key, CoinGeckoID: "newer", PriceUSD: &newPrice, FetchedAt: time.Unix(2_000, 0).UTC()}
	older := marketdata.Record{Key: key, CoinGeckoID: "older", PriceUSD: &oldPrice, FetchedAt: time.Unix(1_000, 0).UTC()}
	if err := c.SaveMarketData(ctx, []marketdata.CacheWrite{{Record: newer, TTL: 10 * time.Minute}}); err != nil {
		t.Fatalf("newer SaveMarketData: %v", err)
	}
	before, err := c.client.TTL(ctx, marketKey(key)).Result()
	if err != nil {
		t.Fatalf("TTL before stale write: %v", err)
	}
	if err := c.SaveMarketData(ctx, []marketdata.CacheWrite{{Record: older, TTL: time.Hour}}); err != nil {
		t.Fatalf("older SaveMarketData: %v", err)
	}
	after, err := c.client.TTL(ctx, marketKey(key)).Result()
	if err != nil {
		t.Fatalf("TTL after stale write: %v", err)
	}
	got, err := c.LoadMarketData(ctx, []marketdata.Key{key})
	if err != nil {
		t.Fatalf("LoadMarketData: %v", err)
	}
	if got[key].CoinGeckoID != "newer" || got[key].PriceUSD == nil || *got[key].PriceUSD != newPrice {
		t.Fatalf("older write replaced newer record: %#v", got[key])
	}
	if after > before+time.Second {
		t.Fatalf("older write extended TTL from %v to %v", before, after)
	}
}

func TestDecodeMarketPayloadRejectsMalformedEnvelopes(t *testing.T) {
	key := marketdata.ContractKey("ethereum", "0xA0B8")
	valid := []byte(`{"chain":"ethereum","tokenKey":"0xa0b8","coingeckoID":"usd-coin","fetchedAt":"2023-11-14T22:13:20.123456789Z","fetchedAtUnixNano":"1700000000123456789"}`)

	tests := []struct {
		name string
		raw  string
	}{
		{name: "null", raw: `null`},
		{name: "empty object", raw: `{}`},
		{name: "array", raw: `[]`},
		{name: "number", raw: `1`},
		{name: "string", raw: `"market"`},
		{name: "missing version", raw: `{"chain":"ethereum","tokenKey":"0xa0b8","coingeckoID":"usd-coin","fetchedAt":"2023-11-14T22:13:20.123456789Z"}`},
		{name: "nonnumeric version", raw: `{"chain":"ethereum","tokenKey":"0xa0b8","coingeckoID":"usd-coin","fetchedAt":"2023-11-14T22:13:20.123456789Z","fetchedAtUnixNano":"not-a-number"}`},
		{name: "wrong key", raw: `{"chain":"ethereum","tokenKey":"0xdead","coingeckoID":"usd-coin","fetchedAt":"2023-11-14T22:13:20.123456789Z","fetchedAtUnixNano":"1700000000123456789"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := decodeMarketPayload([]byte(tt.raw), key); ok {
				t.Fatalf("decodeMarketPayload accepted %s", tt.raw)
			}
		})
	}
	if got, ok := decodeMarketPayload(valid, key); !ok || got.Key != key || got.CoinGeckoID != "usd-coin" {
		t.Fatalf("valid payload decode = %#v, ok=%v", got, ok)
	}
}

func TestMarketDataPrecisionBoundaryDoesNotReplaceNewerRecord(t *testing.T) {
	c := newTestCache(t)
	ctx := context.Background()
	key := marketdata.ContractKey("ethereum", "0xA0B8")
	deleteMarketKeys(t, c, key)

	newPrice := "2.00"
	oldPrice := "1.00"
	newer := marketdata.Record{
		Key: key, CoinGeckoID: "newer", PriceUSD: &newPrice,
		FetchedAt: time.Unix(1_700_000_000, 123456789).UTC(),
	}
	older := newer
	older.CoinGeckoID = "older"
	older.PriceUSD = &oldPrice
	older.FetchedAt = time.Unix(1_700_000_000, 123456788).UTC()
	if err := c.SaveMarketData(ctx, []marketdata.CacheWrite{{Record: newer, TTL: 10 * time.Minute}}); err != nil {
		t.Fatalf("newer SaveMarketData: %v", err)
	}
	if err := c.SaveMarketData(ctx, []marketdata.CacheWrite{{Record: older, TTL: time.Hour}}); err != nil {
		t.Fatalf("precision-boundary older SaveMarketData: %v", err)
	}
	got, err := c.LoadMarketData(ctx, []marketdata.Key{key})
	if err != nil {
		t.Fatalf("LoadMarketData: %v", err)
	}
	if got[key].CoinGeckoID != "newer" || got[key].PriceUSD == nil || *got[key].PriceUSD != newPrice {
		t.Fatalf("one-nanosecond older write replaced newer record: %#v", got[key])
	}
}

func TestMarketDataMalformedRedisEnvelopesAreMissesAndReplaceable(t *testing.T) {
	c := newTestCache(t)
	ctx := context.Background()
	key := marketdata.ContractKey("ethereum", "0xA0B8")
	deleteMarketKeys(t, c, key)

	malformed := []string{
		`null`, `{}`, `[]`, `1`, `"market"`,
		`{"fetchedAtUnixNano":"not-a-number"}`,
		`{"chain":"ethereum","tokenKey":"0xdead","coingeckoID":"future","fetchedAt":"2286-11-20T17:46:39.999999999Z","fetchedAtUnixNano":"9999999999999999999"}`,
	}
	for i, raw := range malformed {
		if err := c.client.Set(ctx, marketKey(key), raw, time.Hour).Err(); err != nil {
			t.Fatalf("seed malformed envelope %d: %v", i, err)
		}
		if got, err := c.LoadMarketData(ctx, []marketdata.Key{key}); err != nil {
			t.Fatalf("LoadMarketData malformed envelope %d: %v", i, err)
		} else if len(got) != 0 {
			t.Fatalf("malformed envelope %d returned a record: %#v", i, got)
		}
		valid := marketdata.Record{Key: key, CoinGeckoID: "usd-coin", FetchedAt: time.Unix(2_000, 0).UTC()}
		if err := c.SaveMarketData(ctx, []marketdata.CacheWrite{{Record: valid, TTL: 10 * time.Minute}}); err != nil {
			t.Fatalf("replace malformed envelope %d: %v", i, err)
		}
		if got, err := c.LoadMarketData(ctx, []marketdata.Key{key}); err != nil {
			t.Fatalf("LoadMarketData replaced envelope %d: %v", i, err)
		} else if got[key].CoinGeckoID != "usd-coin" {
			t.Fatalf("replacement %d = %#v", i, got[key])
		}
	}
}

func TestMarketDataMalformedSameKeyEnvelopesAreReplaceable(t *testing.T) {
	c := newTestCache(t)
	ctx := context.Background()
	key := marketdata.ContractKey("ethereum", "0xA0B8")
	deleteMarketKeys(t, c, key)

	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "oversized version",
			raw:  `{"chain":"ethereum","tokenKey":"0xa0b8","coingeckoID":"malformed","fetchedAt":"2023-11-14T22:13:20.123456789Z","fetchedAtUnixNano":"9223372036854775808"}`,
		},
		{
			name: "invalid timestamp",
			raw:  `{"chain":"ethereum","tokenKey":"0xa0b8","coingeckoID":"malformed","fetchedAt":"not-a-timestamp","fetchedAtUnixNano":"1700000000123456789"}`,
		},
		{
			name: "mismatched version",
			raw:  `{"chain":"ethereum","tokenKey":"0xa0b8","coingeckoID":"malformed","fetchedAt":"2023-11-14T22:13:20.123456789Z","fetchedAtUnixNano":"1700000000123456788"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := c.client.Set(ctx, marketKey(key), tt.raw, time.Hour).Err(); err != nil {
				t.Fatalf("seed malformed envelope: %v", err)
			}

			valid := marketdata.Record{Key: key, CoinGeckoID: "legitimate", FetchedAt: time.Unix(2_000, 0).UTC()}
			if err := c.SaveMarketData(ctx, []marketdata.CacheWrite{{Record: valid, TTL: 10 * time.Minute}}); err != nil {
				t.Fatalf("replace malformed envelope: %v", err)
			}
			got, err := c.LoadMarketData(ctx, []marketdata.Key{key})
			if err != nil {
				t.Fatalf("load replacement: %v", err)
			}
			if got[key].CoinGeckoID != "legitimate" {
				t.Fatalf("replacement = %#v", got[key])
			}
		})
	}
}

func TestDecodeCMCMarketPayload(t *testing.T) {
	key := marketdata.ContractKey("ethereum", "0xA0B8")
	valid := []byte(`{"chain":"ethereum","tokenKey":"0xa0b8","coinMarketCapID":1660,"fetchedAt":"2023-11-14T22:13:20.123456789Z","fetchedAtUnixNano":"1700000000123456789"}`)
	if got, ok := decodeCMCMarketPayload(valid, key); !ok || got.CoinMarketCapID != 1660 || got.Key != key {
		t.Fatalf("valid CMC payload = %#v ok=%v", got, ok)
	}
	for _, raw := range []string{`null`, `{}`, `{"chain":"ethereum","tokenKey":"0xa0b8","coinMarketCapID":0}`, `{"chain":"ethereum","tokenKey":"0xdead","coinMarketCapID":1660,"fetchedAt":"2023-11-14T22:13:20.123456789Z","fetchedAtUnixNano":"1700000000123456789"}`} {
		if _, ok := decodeCMCMarketPayload([]byte(raw), key); ok {
			t.Fatalf("accepted malformed CMC payload %s", raw)
		}
	}
}

func TestCoinMarketCapMarketRoundTripTTLAndOlderGuard(t *testing.T) {
	c := newTestCache(t)
	ctx := context.Background()
	key := marketdata.ContractKey("ethereum", "0xA0B8")
	deleteMarketKeys(t, c, key)
	deleteCMCMarketKeys(t, c, key)
	price := "1e-400"
	newer := cmcmarket.Record{Key: key, CoinMarketCapID: 1660, PriceUSD: &price, FetchedAt: time.Unix(2_000, 0).UTC()}
	if err := c.SaveCoinMarketCapMarketData(ctx, []cmcmarket.CacheWrite{{Record: newer, TTL: 10 * time.Minute}}); err != nil {
		t.Fatalf("save newer: %v", err)
	}
	if exists, err := c.client.Exists(ctx, marketKey(key)).Result(); err != nil || exists != 0 {
		t.Fatalf("CMC write collided with CG namespace: exists=%d err=%v", exists, err)
	}
	before, _ := c.client.TTL(ctx, cmcMarketKey(key)).Result()
	oldPrice := "9"
	older := cmcmarket.Record{Key: key, CoinMarketCapID: 9, PriceUSD: &oldPrice, FetchedAt: time.Unix(1_000, 0).UTC()}
	if err := c.SaveCoinMarketCapMarketData(ctx, []cmcmarket.CacheWrite{{Record: older, TTL: time.Hour}}); err != nil {
		t.Fatalf("save older: %v", err)
	}
	after, _ := c.client.TTL(ctx, cmcMarketKey(key)).Result()
	got, err := c.LoadCoinMarketCapMarketData(ctx, []marketdata.Key{key})
	if err != nil || got[key].CoinMarketCapID != 1660 || got[key].PriceUSD == nil || *got[key].PriceUSD != price {
		t.Fatalf("load = %#v err=%v", got, err)
	}
	if after > before+time.Second {
		t.Fatalf("older write extended TTL from %v to %v", before, after)
	}
}

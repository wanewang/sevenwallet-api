package rediscache

import (
	"context"
	"os"
	"testing"
	"time"

	"wallet-api/internal/lifi"
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

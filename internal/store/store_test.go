package store

import (
	"context"
	"os"
	"testing"
	"time"

	"wallet-api/internal/wallet"
)

func newTestStore(t *testing.T) *Postgres {
	t.Helper()
	dsn := os.Getenv("WALLET_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set WALLET_TEST_DATABASE_URL to run store integration tests")
	}
	ctx := context.Background()
	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// Clean slate.
	_, _ = s.pool.Exec(ctx, "TRUNCATE wallet_tokens, token_fetch_meta, tx_cache")
	t.Cleanup(s.Close)
	return s
}

func usdc(a string) *string { return &a }

func TestSaveAndGetFreshTokens(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := &wallet.TokenPortfolio{
		Address: "0xabc", Network: "eth-mainnet", FetchedAt: time.Now().UTC(),
		Tokens: []wallet.Token{
			{TokenAddress: nil, Symbol: "ETH", Name: "Ethereum", Decimals: 18, RawBalance: "15", Balance: "1.5", IsNative: true,
				Price: &wallet.Price{Currency: "usd", Value: "3200.50", LastUpdatedAt: "2026-06-23T00:00:00Z"}},
			{TokenAddress: usdc("0xA0B8"), Symbol: "USDC", Decimals: 6, RawBalance: "12500000", Balance: "12.5"},
		},
	}
	if err := s.SaveTokens(ctx, p); err != nil {
		t.Fatalf("SaveTokens: %v", err)
	}
	got, ok, err := s.GetFreshTokens(ctx, "0xabc", "eth-mainnet", time.Minute)
	if err != nil || !ok {
		t.Fatalf("GetFreshTokens ok=%v err=%v", ok, err)
	}
	if len(got.Tokens) != 2 {
		t.Fatalf("got %d tokens, want 2", len(got.Tokens))
	}
}

func TestGetFreshTokensExpired(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := &wallet.TokenPortfolio{Address: "0xabc", Network: "eth-mainnet", FetchedAt: time.Now().Add(-time.Hour).UTC()}
	if err := s.SaveTokens(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetFreshTokens(ctx, "0xabc", "eth-mainnet", time.Minute); ok {
		t.Error("expected stale snapshot to be reported not-fresh")
	}
}

func TestSaveTokensReplacesSnapshot(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	first := &wallet.TokenPortfolio{Address: "0xabc", Network: "eth-mainnet", FetchedAt: time.Now().UTC(),
		Tokens: []wallet.Token{{Symbol: "OLD", Decimals: 18, RawBalance: "1", Balance: "1", IsNative: true}}}
	if err := s.SaveTokens(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := &wallet.TokenPortfolio{Address: "0xabc", Network: "eth-mainnet", FetchedAt: time.Now().UTC(),
		Tokens: []wallet.Token{{TokenAddress: usdc("0xnew"), Symbol: "NEW", Decimals: 6, RawBalance: "2", Balance: "2"}}}
	if err := s.SaveTokens(ctx, second); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.GetFreshTokens(ctx, "0xabc", "eth-mainnet", time.Minute)
	if len(got.Tokens) != 1 || got.Tokens[0].Symbol != "NEW" {
		t.Errorf("snapshot not replaced: %+v", got.Tokens)
	}
}

func TestSaveAndGetFreshTransactions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	page := &wallet.TransactionPage{Address: "0xabc", Transfers: []wallet.Transfer{{Hash: "0x1", Asset: "ETH", Value: "0.5"}}}
	if err := s.SaveTransactions(ctx, "0xabc", "limit=25", page); err != nil {
		t.Fatalf("SaveTransactions: %v", err)
	}
	got, ok, err := s.GetFreshTransactions(ctx, "0xabc", "limit=25", time.Minute)
	if err != nil || !ok {
		t.Fatalf("GetFreshTransactions ok=%v err=%v", ok, err)
	}
	if len(got.Transfers) != 1 || got.Transfers[0].Hash != "0x1" {
		t.Errorf("round-trip wrong: %+v", got)
	}
}

package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"wallet-api/internal/lifi"
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
	_, _ = s.pool.Exec(ctx, "TRUNCATE wallet_tokens, token_fetch_meta, tx_cache, lifi_token_lists")
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
	// Assert native token price round-trips correctly.
	var native *wallet.Token
	for i := range got.Tokens {
		if got.Tokens[i].TokenAddress == nil {
			native = &got.Tokens[i]
			break
		}
	}
	if native == nil {
		t.Fatal("native token not found in result")
	}
	if native.Price == nil {
		t.Fatal("native token Price is nil, want non-nil")
	}
	if native.Price.Currency != "usd" {
		t.Errorf("native Price.Currency = %q, want %q", native.Price.Currency, "usd")
	}
	if native.Price.Value != "3200.50" {
		t.Errorf("native Price.Value = %q, want %q", native.Price.Value, "3200.50")
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

func TestSaveTokensLowercasesKeys(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	mixedAddr := "0xAbCdEf0000000000000000000000000000000001"
	p := &wallet.TokenPortfolio{
		Address: "0xABC", Network: "eth-mainnet", FetchedAt: time.Now().UTC(),
		Tokens: []wallet.Token{
			{TokenAddress: &mixedAddr, Symbol: "MIX", Decimals: 18, RawBalance: "1", Balance: "1"},
		},
	}
	if err := s.SaveTokens(ctx, p); err != nil {
		t.Fatalf("SaveTokens: %v", err)
	}
	// Read back using lowercase address — must find the snapshot.
	got, ok, err := s.GetFreshTokens(ctx, "0xabc", "eth-mainnet", time.Minute)
	if err != nil || !ok {
		t.Fatalf("GetFreshTokens ok=%v err=%v", ok, err)
	}
	if len(got.Tokens) != 1 {
		t.Fatalf("got %d tokens, want 1", len(got.Tokens))
	}
	tok := got.Tokens[0]
	if tok.TokenAddress == nil {
		t.Fatal("TokenAddress is nil, want lowercase hex")
	}
	if *tok.TokenAddress != strings.ToLower(mixedAddr) {
		t.Errorf("TokenAddress = %q, want %q", *tok.TokenAddress, strings.ToLower(mixedAddr))
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

func TestSaveAndLoadTokenList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, _, ok, err := s.LoadTokenList(ctx, "ETH"); err != nil || ok {
		t.Fatalf("empty load: ok=%v err=%v", ok, err)
	}

	fetched := time.Now().UTC().Truncate(time.Second)
	tokens := []lifi.ListToken{
		{Address: "0xA0B8", Symbol: "USDC", Name: "USD Coin", Decimals: 6, CoinKey: "USDC", LogoURI: "u", PriceUSD: "1.00"},
		{Address: "0xdAC1", Symbol: "USDT", Name: "Tether", Decimals: 6},
	}
	if err := s.SaveTokenList(ctx, "ETH", tokens, fetched); err != nil {
		t.Fatalf("SaveTokenList: %v", err)
	}
	got, gotAt, ok, err := s.LoadTokenList(ctx, "ETH")
	if err != nil || !ok {
		t.Fatalf("LoadTokenList ok=%v err=%v", ok, err)
	}
	if len(got) != 2 || got[0].Symbol != "USDC" || got[0].Decimals != 6 || got[0].PriceUSD != "1.00" {
		t.Errorf("round-trip wrong: %+v", got)
	}
	if !gotAt.Equal(fetched) {
		t.Errorf("fetchedAt = %v, want %v", gotAt, fetched)
	}

	// Upsert replaces the blob for the same chain.
	if err := s.SaveTokenList(ctx, "ETH", tokens[:1], fetched); err != nil {
		t.Fatalf("SaveTokenList upsert: %v", err)
	}
	got, _, _, _ = s.LoadTokenList(ctx, "ETH")
	if len(got) != 1 {
		t.Errorf("upsert did not replace: got %d tokens, want 1", len(got))
	}
}

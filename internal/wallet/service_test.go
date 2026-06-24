package wallet

import (
	"context"
	"errors"
	"testing"
	"time"

	"wallet-api/internal/alchemy"
	"wallet-api/internal/lifi"
)

func usdc(addr string) *string { return &addr }

// allowUSDC permits the test USDC address (0xA0B8) and the ETH/USDC symbols.
func allowUSDC() *fakeAllowlist {
	return &fakeAllowlist{
		byAddr: map[string]lifi.ListToken{
			"0xa0b8": {Address: "0xA0B8", Symbol: "USDC", Name: "USD Coin", Decimals: 6, CoinKey: "USDC", LogoURI: "https://logo/usdc.png", PriceUSD: "1.0001"},
		},
		symbols: map[string]bool{"ETH": true, "USDC": true},
	}
}

func TestGetTokensCacheMissFetchesAndSaves(t *testing.T) {
	fa := &fakeAlchemy{tokens: []alchemy.Token{
		{TokenAddress: nil, Symbol: "ETH", Name: "Ethereum", Decimals: 18, RawBalance: "1500000000000000000",
			Price: &alchemy.Price{Currency: "usd", Value: "3200.50", LastUpdatedAt: "2026-06-23T00:00:00Z"}},
		{TokenAddress: usdc("0xA0B8"), Symbol: "USDC", Name: "USD Coin", Decimals: 6, RawBalance: "12500000"},
	}}
	ts := &fakeTokenStore{}
	svc := NewService(fa, ts, &fakeTxCache{}, allowUSDC(), "eth-mainnet", time.Minute)

	p, err := svc.GetTokens(context.Background(), "0xABC")
	if err != nil {
		t.Fatalf("GetTokens error: %v", err)
	}
	if fa.tokenCalls != 1 || ts.saveCalls != 1 {
		t.Errorf("expected 1 fetch + 1 save, got fetch=%d save=%d", fa.tokenCalls, ts.saveCalls)
	}
	// Raw (unfiltered) snapshot is what gets cached.
	if ts.saved == nil || len(ts.saved.Tokens) != 2 {
		t.Errorf("cache should store raw 2-token snapshot, got %+v", ts.saved)
	}
	if p.Address != "0xabc" || p.Network != "eth-mainnet" {
		t.Errorf("portfolio header wrong: %+v", p)
	}
	if len(p.Tokens) != 2 {
		t.Fatalf("got %d tokens, want 2 (ETH + allowlisted USDC)", len(p.Tokens))
	}
	if !p.Tokens[0].IsNative || p.Tokens[0].Balance != "1.5" {
		t.Errorf("native token normalization wrong: %+v", p.Tokens[0])
	}
	// USDC enriched from the allowlist.
	u := p.Tokens[1]
	if u.IsNative || u.Balance != "12.5" {
		t.Errorf("erc20 normalization wrong: %+v", u)
	}
	if u.LogoURI == nil || *u.LogoURI != "https://logo/usdc.png" {
		t.Errorf("USDC LogoURI not enriched: %+v", u.LogoURI)
	}
	if u.PriceUSD == nil || *u.PriceUSD != "1.0001" {
		t.Errorf("USDC PriceUSD not enriched: %+v", u.PriceUSD)
	}
	if u.CoinKey == nil || *u.CoinKey != "USDC" {
		t.Errorf("USDC CoinKey not enriched: %+v", u.CoinKey)
	}
}

func TestGetTokensDropsUnknownTokens(t *testing.T) {
	fa := &fakeAlchemy{tokens: []alchemy.Token{
		{TokenAddress: nil, Symbol: "ETH", Decimals: 18, RawBalance: "1000000000000000000"},
		{TokenAddress: usdc("0xA0B8"), Symbol: "USDC", Decimals: 6, RawBalance: "12500000"},
		{TokenAddress: usdc("0xSPAM"), Symbol: "SCAM", Decimals: 18, RawBalance: "999"},
	}}
	svc := NewService(fa, &fakeTokenStore{}, &fakeTxCache{}, allowUSDC(), "eth-mainnet", time.Minute)
	p, err := svc.GetTokens(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Tokens) != 2 {
		t.Fatalf("expected ETH + USDC only, got %d: %+v", len(p.Tokens), p.Tokens)
	}
	for _, tok := range p.Tokens {
		if tok.Symbol == "SCAM" {
			t.Error("unknown SCAM token should have been dropped")
		}
	}
}

func TestGetTokensRescalesBalanceOnDecimalsOverride(t *testing.T) {
	// Alchemy reports decimals=18 for an address the allowlist says is 6 decimals.
	fa := &fakeAlchemy{tokens: []alchemy.Token{
		{TokenAddress: usdc("0xA0B8"), Symbol: "usdc", Name: "wrong", Decimals: 18, RawBalance: "12500000"},
	}}
	svc := NewService(fa, &fakeTokenStore{}, &fakeTxCache{}, allowUSDC(), "eth-mainnet", time.Minute)
	p, err := svc.GetTokens(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Tokens) != 1 {
		t.Fatalf("got %d tokens, want 1", len(p.Tokens))
	}
	tok := p.Tokens[0]
	if tok.Decimals != 6 {
		t.Errorf("decimals = %d, want 6 (overridden)", tok.Decimals)
	}
	if tok.Balance != "12.5" {
		t.Errorf("balance = %q, want 12.5 (re-scaled with 6 decimals)", tok.Balance)
	}
	if tok.Symbol != "USDC" || tok.Name != "USD Coin" {
		t.Errorf("symbol/name not overridden from allowlist: %+v", tok)
	}
}

func TestGetTokensCacheHitFiltersCachedSnapshot(t *testing.T) {
	cached := &TokenPortfolio{Address: "0xabc", Network: "eth-mainnet", Tokens: []Token{
		{TokenAddress: nil, Symbol: "ETH", Decimals: 18, RawBalance: "0", Balance: "0", IsNative: true},
		{TokenAddress: usdc("0xSPAM"), Symbol: "SCAM", Decimals: 18, RawBalance: "1", Balance: "0"},
	}}
	fa := &fakeAlchemy{}
	ts := &fakeTokenStore{saved: cached, fresh: true}
	svc := NewService(fa, ts, &fakeTxCache{}, allowUSDC(), "eth-mainnet", time.Minute)

	p, err := svc.GetTokens(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if fa.tokenCalls != 0 {
		t.Errorf("cache hit should not call alchemy, got %d calls", fa.tokenCalls)
	}
	if len(p.Tokens) != 1 || !p.Tokens[0].IsNative {
		t.Errorf("cache-hit filtering wrong: want only native ETH, got %+v", p.Tokens)
	}
}

func TestGetTokensWrapsUpstreamError(t *testing.T) {
	fa := &fakeAlchemy{err: context.DeadlineExceeded}
	svc := NewService(fa, &fakeTokenStore{}, &fakeTxCache{}, allowUSDC(), "eth-mainnet", time.Minute)
	_, err := svc.GetTokens(context.Background(), "0xABC")
	if err == nil || !errorsIs(err, ErrUpstream) {
		t.Errorf("expected ErrUpstream, got %v", err)
	}
}

func TestGetTokensWrapsSaveError(t *testing.T) {
	fa := &fakeAlchemy{tokens: []alchemy.Token{
		{TokenAddress: nil, Symbol: "ETH", Decimals: 18, RawBalance: "1500000000000000000"},
	}}
	ts := &fakeTokenStore{saveErr: errors.New("db down")}
	svc := NewService(fa, ts, &fakeTxCache{}, allowUSDC(), "eth-mainnet", time.Minute)

	_, err := svc.GetTokens(context.Background(), "0xABC")
	if err == nil || !errorsIs(err, ErrStore) {
		t.Errorf("expected ErrStore from SaveTokens failure, got %v", err)
	}
}

func errorsIs(err, target error) bool { return errors.Is(err, target) }

func TestGetTransactionsCacheMissFiltersAndSaves(t *testing.T) {
	fa := &fakeAlchemy{transfers: alchemy.TransfersResult{Transfers: []alchemy.Transfer{
		{Hash: "0x1", From: "0xabc", To: "0xdef", Asset: "ETH", Value: "0.5", BlockNum: "0x20", Category: "external"},
		{Hash: "0x2", From: "0xabc", To: "0xdef", Asset: "USDC", Value: "10", BlockNum: "0x21", Category: "erc20"},
		{Hash: "0x3", From: "0xabc", To: "0xdef", Asset: "SCAM", Value: "999", BlockNum: "0x22", Category: "erc20"},
	}}}
	tc := &fakeTxCache{}
	svc := NewService(fa, &fakeTokenStore{}, tc, allowUSDC(), "eth-mainnet", time.Minute)

	page, err := svc.GetTransactions(context.Background(), "0xABC", 25, "")
	if err != nil {
		t.Fatal(err)
	}
	if fa.txCalls != 1 || tc.saveCalls != 1 {
		t.Errorf("expected 1 fetch + 1 save, got fetch=%d save=%d", fa.txCalls, tc.saveCalls)
	}
	if tc.saved == nil || len(tc.saved.Transfers) != 3 {
		t.Errorf("cache should store raw 3-transfer snapshot, got %+v", tc.saved)
	}
	if len(page.Transfers) != 2 {
		t.Fatalf("expected ETH+USDC kept, SCAM dropped; got %+v", page.Transfers)
	}
	for _, tr := range page.Transfers {
		if tr.Asset == "SCAM" {
			t.Error("SCAM transfer should be filtered out")
		}
	}
}

func TestGetTransactionsCacheHitFilters(t *testing.T) {
	cached := &TransactionPage{Address: "0xabc", Transfers: []Transfer{
		{Hash: "0xkeep", Asset: "ETH"},
		{Hash: "0xdrop", Asset: "SCAM"},
	}}
	fa := &fakeAlchemy{}
	tc := &fakeTxCache{saved: cached, fresh: true}
	svc := NewService(fa, &fakeTokenStore{}, tc, allowUSDC(), "eth-mainnet", time.Minute)

	page, err := svc.GetTransactions(context.Background(), "0xABC", 25, "")
	if err != nil {
		t.Fatal(err)
	}
	if fa.txCalls != 0 {
		t.Errorf("expected cache hit, got fetch=%d", fa.txCalls)
	}
	if len(page.Transfers) != 1 || page.Transfers[0].Hash != "0xkeep" {
		t.Errorf("cache-hit transfer filtering wrong: %+v", page.Transfers)
	}
}

func TestGetTransactionsPageKeyBypassesCache(t *testing.T) {
	fa := &fakeAlchemy{transfers: alchemy.TransfersResult{Transfers: []alchemy.Transfer{{Hash: "0x2", Asset: "ETH"}}}}
	tc := &fakeTxCache{saved: &TransactionPage{Transfers: []Transfer{{Hash: "0xcached", Asset: "ETH"}}}, fresh: true}
	svc := NewService(fa, &fakeTokenStore{}, tc, allowUSDC(), "eth-mainnet", time.Minute)

	page, err := svc.GetTransactions(context.Background(), "0xABC", 25, "PAGEKEY123")
	if err != nil {
		t.Fatal(err)
	}
	if fa.txCalls != 1 {
		t.Errorf("pageKey should force a fetch, got %d", fa.txCalls)
	}
	if tc.saveCalls != 0 {
		t.Errorf("pageKey fetches must not be cached, got %d saves", tc.saveCalls)
	}
	if len(page.Transfers) != 1 || page.Transfers[0].Hash != "0x2" {
		t.Errorf("expected fresh filtered page, got %+v", page)
	}
}

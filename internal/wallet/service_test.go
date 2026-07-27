package wallet

import (
	"context"
	"errors"
	"reflect"
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
	svc := NewService(fa, ts, &fakeTxCache{}, allowUSDC(), denyValidator(), nil, "eth-mainnet", time.Minute)

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
	svc := NewService(fa, &fakeTokenStore{}, &fakeTxCache{}, allowUSDC(), denyValidator(), nil, "eth-mainnet", time.Minute)
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

func TestGetTokensEnrichesNativeMetadataWhenAlchemyOmitsIt(t *testing.T) {
	allow := allowUSDC()
	allow.native = lifi.ListToken{Symbol: "ETH", Name: "Ethereum", Decimals: 18, CoinKey: "ETH", PriceUSD: "3200.50"}
	allow.nativeFetchedAt = time.Date(2026, 7, 22, 12, 30, 0, 0, time.UTC)
	allow.nativeOK = true

	fa := &fakeAlchemy{tokens: []alchemy.Token{{
		TokenAddress: nil, RawBalance: "2945090757010143844", Decimals: 0,
	}}}
	svc := NewService(fa, &fakeTokenStore{}, &fakeTxCache{}, allow, denyValidator(), nil, "eth-mainnet", time.Minute)

	portfolio, err := svc.GetTokens(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if len(portfolio.Tokens) != 1 {
		t.Fatalf("tokens = %+v, want one native token", portfolio.Tokens)
	}
	native := portfolio.Tokens[0]
	if native.Symbol != "ETH" || native.Name != "Ethereum" || native.Decimals != 18 {
		t.Fatalf("native metadata = %+v, want ETH/Ethereum/18", native)
	}
	if native.CoinKey == nil || *native.CoinKey != "ETH" {
		t.Errorf("native coinKey = %v, want ETH", native.CoinKey)
	}
	if native.Balance != "2.945090757010143844" {
		t.Errorf("native balance = %q, want scaled balance", native.Balance)
	}
}

func TestGetTokensCacheHitEnrichesNativeMetadataWhenAlchemyOmitsIt(t *testing.T) {
	allow := allowUSDC()
	allow.native = lifi.ListToken{Symbol: "ETH", Name: "Ethereum", Decimals: 18}
	allow.nativeOK = true
	cached := &TokenPortfolio{Address: "0xabc", Network: "eth-mainnet", Tokens: []Token{{
		IsNative: true, RawBalance: "1000000000000000000", Balance: "1000000000000000000",
	}}}

	svc := NewService(&fakeAlchemy{}, &fakeTokenStore{saved: cached, fresh: true}, &fakeTxCache{}, allow, denyValidator(), nil, "eth-mainnet", time.Minute)
	portfolio, err := svc.GetTokens(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if len(portfolio.Tokens) != 1 || portfolio.Tokens[0].Symbol != "ETH" || portfolio.Tokens[0].Name != "Ethereum" {
		t.Fatalf("cached native metadata = %+v, want ETH/Ethereum", portfolio.Tokens)
	}
}

func TestGetTokensRescalesBalanceOnDecimalsOverride(t *testing.T) {
	// Alchemy reports decimals=18 for an address the allowlist says is 6 decimals.
	fa := &fakeAlchemy{tokens: []alchemy.Token{
		{TokenAddress: usdc("0xA0B8"), Symbol: "usdc", Name: "wrong", Decimals: 18, RawBalance: "12500000"},
	}}
	svc := NewService(fa, &fakeTokenStore{}, &fakeTxCache{}, allowUSDC(), denyValidator(), nil, "eth-mainnet", time.Minute)
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

func TestEnrichTokenKeepsDecimalsWhenRescaleFails(t *testing.T) {
	// RawBalance is corrupt (e.g. bad DB snapshot), so re-scaling fails: the old
	// Decimals/Balance pair must be kept rather than mixing old Balance with new
	// Decimals.
	in := Token{Symbol: "usdc", Decimals: 18, RawBalance: "corrupt", Balance: "12.5"}
	got := enrichToken(in, lifi.ListToken{Symbol: "USDC", Decimals: 6})
	if got.Decimals != 18 {
		t.Errorf("decimals = %d, want 18 (unchanged when re-scale fails)", got.Decimals)
	}
	if got.Balance != "12.5" {
		t.Errorf("balance = %q, want 12.5 (unchanged)", got.Balance)
	}
}

func TestEnrichFromValidationKeepsDecimalsWhenRescaleFails(t *testing.T) {
	in := Token{Symbol: "new", Decimals: 18, RawBalance: "corrupt", Balance: "12.5"}
	got := enrichFromValidation(in, Validation{Valid: true, Symbol: "NEW", Decimals: 6})
	if got.Decimals != 18 {
		t.Errorf("decimals = %d, want 18 (unchanged when re-scale fails)", got.Decimals)
	}
	if got.Balance != "12.5" {
		t.Errorf("balance = %q, want 12.5 (unchanged)", got.Balance)
	}
}

func TestGetTokensCacheHitFiltersCachedSnapshot(t *testing.T) {
	cached := &TokenPortfolio{Address: "0xabc", Network: "eth-mainnet", Tokens: []Token{
		{TokenAddress: nil, Symbol: "ETH", Decimals: 18, RawBalance: "0", Balance: "0", IsNative: true},
		{TokenAddress: usdc("0xSPAM"), Symbol: "SCAM", Decimals: 18, RawBalance: "1", Balance: "0"},
	}}
	fa := &fakeAlchemy{}
	ts := &fakeTokenStore{saved: cached, fresh: true}
	svc := NewService(fa, ts, &fakeTxCache{}, allowUSDC(), denyValidator(), nil, "eth-mainnet", time.Minute)

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

func TestGetWalletTokensSkipsMarketEnrichmentOnCacheMiss(t *testing.T) {
	fa := &fakeAlchemy{tokens: []alchemy.Token{
		{TokenAddress: nil, Symbol: "ETH", Name: "Ethereum", Decimals: 18, RawBalance: "1000000000000000000",
			Price: &alchemy.Price{Currency: "usd", Value: "3200.50", LastUpdatedAt: "2026-06-23T00:00:00Z"}},
		{TokenAddress: usdc("0xA0B8"), Symbol: "USDC", Name: "USD Coin", Decimals: 6, RawBalance: "12500000"},
		{TokenAddress: usdc("0xSPAM"), Symbol: "SCAM", Decimals: 18, RawBalance: "999"},
	}}
	ts := &fakeTokenStore{}
	market := &fakeMarketEnricher{}
	svc := NewService(fa, ts, &fakeTxCache{}, allowUSDC(), denyValidator(), market, "eth-mainnet", time.Minute)

	got, err := svc.GetWalletTokens(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if fa.tokenCalls != 1 || ts.saveCalls != 1 {
		t.Fatalf("fetch/save calls = %d/%d, want 1/1", fa.tokenCalls, ts.saveCalls)
	}
	if market.calls != 0 {
		t.Fatalf("market enricher calls = %d, want 0", market.calls)
	}
	if len(got.Tokens) != 2 || got.Tokens[0].Symbol != "ETH" || got.Tokens[1].Symbol != "USDC" {
		t.Fatalf("filtered tokens = %+v, want ETH + USDC", got.Tokens)
	}

	// Skipping CoinGecko must not cost the route any pre-CoinGecko data:
	// Alchemy prices and LI.FI list metadata still have to survive.
	if eth := got.Tokens[0]; eth.Price == nil || eth.Price.Value != "3200.50" {
		t.Errorf("Alchemy price not preserved on ETH: %+v", eth.Price)
	}
	u := got.Tokens[1]
	if u.LogoURI == nil || *u.LogoURI != "https://logo/usdc.png" {
		t.Errorf("USDC LogoURI not enriched from LI.FI: %v", u.LogoURI)
	}
	if u.PriceUSD == nil || *u.PriceUSD != "1.0001" {
		t.Errorf("USDC PriceUSD not enriched from LI.FI: %v", u.PriceUSD)
	}
	if u.CoinKey == nil || *u.CoinKey != "USDC" {
		t.Errorf("USDC CoinKey not enriched from LI.FI: %v", u.CoinKey)
	}
	// CoinGecko-only fields stay null on this route.
	for _, tok := range got.Tokens {
		if tok.Change24HPercent != nil || tok.MarketCapUSD != nil || tok.MarketDataUpdatedAt != nil {
			t.Errorf("%s carries CoinGecko market fields: %+v", tok.Symbol, tok)
		}
	}
}

func TestGetWalletTokensSkipsMarketEnrichmentOnCacheHit(t *testing.T) {
	cached := &TokenPortfolio{Address: "0xabc", Network: "eth-mainnet", Tokens: []Token{
		{IsNative: true, Symbol: "ETH", Decimals: 18, RawBalance: "0", Balance: "0"},
		{TokenAddress: usdc("0xA0B8"), Symbol: "usdc", Decimals: 6, RawBalance: "12500000", Balance: "12.5"},
	}}
	fa := &fakeAlchemy{}
	market := &fakeMarketEnricher{}
	svc := NewService(fa, &fakeTokenStore{saved: cached, fresh: true}, &fakeTxCache{}, allowUSDC(), denyValidator(), market, "eth-mainnet", time.Minute)

	got, err := svc.GetWalletTokens(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if fa.tokenCalls != 0 {
		t.Fatalf("Alchemy calls = %d, want 0", fa.tokenCalls)
	}
	if market.calls != 0 {
		t.Fatalf("market enricher calls = %d, want 0", market.calls)
	}
	if len(got.Tokens) != 2 || got.Tokens[1].Symbol != "USDC" {
		t.Fatalf("cached tokens = %+v", got.Tokens)
	}
}

func TestGetWalletTokensReturnsEmptyAfterFiltering(t *testing.T) {
	fa := &fakeAlchemy{tokens: []alchemy.Token{{
		TokenAddress: usdc("0xSPAM"), Symbol: "SCAM", Decimals: 18, RawBalance: "999",
	}}}
	market := &fakeMarketEnricher{}
	svc := NewService(fa, &fakeTokenStore{}, &fakeTxCache{}, allowUSDC(), denyValidator(), market, "eth-mainnet", time.Minute)

	got, err := svc.GetWalletTokens(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tokens) != 0 {
		t.Fatalf("tokens = %+v, want empty portfolio", got.Tokens)
	}
	if market.calls != 0 {
		t.Fatalf("market enricher calls = %d, want 0", market.calls)
	}
}

func TestGetTokenMarketsUsesLatestSnapshotFiltersAndNeverCallsAlchemy(t *testing.T) {
	fetchedAt := time.Now().UTC().Add(-24 * time.Hour)
	cached := &TokenPortfolio{Address: "0xabc", Network: "eth-mainnet", FetchedAt: fetchedAt, Tokens: []Token{
		{Symbol: "ETH", Name: "Ethereum", Decimals: 18, Balance: "2", IsNative: true},
		{TokenAddress: usdc("0xA0B8"), Symbol: "usdc", Name: "old", Decimals: 6, Balance: "12.5"},
		{TokenAddress: usdc("0xSPAM"), Symbol: "SCAM", Decimals: 18, Balance: "9"},
	}}
	price := "1.00"
	change := 2.5
	comparator := &fakeMarketComparator{out: []MarketPair{
		{CG: &CoinGeckoMarket{ID: "ethereum", PriceUSD: &price}},
		{CMC: &CoinMarketCapMarket{ID: 3408, Change24HPercent: &change}},
	}}
	alchemy := &fakeAlchemy{}
	svc := NewService(alchemy, &fakeTokenStore{saved: cached}, &fakeTxCache{}, allowUSDC(), denyValidator(), nil, "eth-mainnet", time.Minute)
	svc.SetMarketComparator(comparator)

	got, err := svc.GetTokenMarkets(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if alchemy.tokenCalls != 0 {
		t.Fatalf("Alchemy calls = %d, want 0", alchemy.tokenCalls)
	}
	if comparator.calls != 1 || len(comparator.seen) != 2 {
		t.Fatalf("comparator calls=%d seen=%#v", comparator.calls, comparator.seen)
	}
	if got.Wallet != "0xabc" || got.PortfolioFetchedAt != fetchedAt || len(got.Tokens) != 2 {
		t.Fatalf("portfolio = %#v", got)
	}
	if got.Tokens[1].Symbol != "USDC" || got.Tokens[0].CG == nil || got.Tokens[1].CMC == nil {
		t.Fatalf("tokens = %#v", got.Tokens)
	}
}

func TestGetTokenMarketsMissingAndStorageErrors(t *testing.T) {
	for _, tc := range []struct {
		store *fakeTokenStore
		want  error
	}{
		{store: &fakeTokenStore{}, want: ErrWalletNotCached},
		{store: &fakeTokenStore{getErr: errors.New("db down")}, want: ErrStore},
	} {
		svc := NewService(&fakeAlchemy{}, tc.store, &fakeTxCache{}, allowUSDC(), denyValidator(), nil, "eth-mainnet", time.Minute)
		_, err := svc.GetTokenMarkets(context.Background(), "0xABC")
		if !errors.Is(err, tc.want) {
			t.Fatalf("error = %v, want %v", err, tc.want)
		}
	}
}

func TestGetTokensMarketEnrichmentRunsAfterCacheMissFiltering(t *testing.T) {
	fa := &fakeAlchemy{tokens: []alchemy.Token{
		{TokenAddress: nil, Symbol: "ETH", Decimals: 18, RawBalance: "1000000000000000000"},
		{TokenAddress: usdc("0xSPAM"), Symbol: "SCAM", Decimals: 18, RawBalance: "999"},
	}}
	market := &fakeMarketEnricher{out: []Token{{Symbol: "ENRICHED"}}}
	svc := NewService(fa, &fakeTokenStore{}, &fakeTxCache{}, allowUSDC(), denyValidator(), market, "eth-mainnet", time.Minute)

	got, err := svc.GetTokens(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if market.calls != 1 {
		t.Fatalf("market enricher calls = %d, want 1", market.calls)
	}
	for _, tok := range market.seen {
		if tok.Symbol == "SCAM" {
			t.Fatal("SCAM must be filtered before market enrichment")
		}
	}
	if !reflect.DeepEqual(got.Tokens, market.out) {
		t.Errorf("tokens = %+v, want enricher output %+v", got.Tokens, market.out)
	}
}

func TestGetTokensMarketEnrichmentRunsAfterCacheHitFiltering(t *testing.T) {
	cached := &TokenPortfolio{Address: "0xabc", Network: "eth-mainnet", Tokens: []Token{
		{TokenAddress: nil, Symbol: "ETH", Decimals: 18, RawBalance: "0", Balance: "0", IsNative: true},
		{TokenAddress: usdc("0xSPAM"), Symbol: "SCAM", Decimals: 18, RawBalance: "1", Balance: "0"},
	}}
	market := &fakeMarketEnricher{out: []Token{{Symbol: "CACHE_ENRICHED"}}}
	svc := NewService(&fakeAlchemy{}, &fakeTokenStore{saved: cached, fresh: true}, &fakeTxCache{}, allowUSDC(), denyValidator(), market, "eth-mainnet", time.Minute)

	got, err := svc.GetTokens(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if market.calls != 1 {
		t.Fatalf("market enricher calls = %d, want 1", market.calls)
	}
	for _, tok := range market.seen {
		if tok.Symbol == "SCAM" {
			t.Fatal("SCAM must be filtered before market enrichment")
		}
	}
	if !reflect.DeepEqual(got.Tokens, market.out) {
		t.Errorf("tokens = %+v, want enricher output %+v", got.Tokens, market.out)
	}
}

func TestGetTokensWrapsUpstreamError(t *testing.T) {
	fa := &fakeAlchemy{err: context.DeadlineExceeded}
	svc := NewService(fa, &fakeTokenStore{}, &fakeTxCache{}, allowUSDC(), denyValidator(), nil, "eth-mainnet", time.Minute)
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
	svc := NewService(fa, ts, &fakeTxCache{}, allowUSDC(), denyValidator(), nil, "eth-mainnet", time.Minute)

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
	svc := NewService(fa, &fakeTokenStore{}, tc, allowUSDC(), denyValidator(), nil, "eth-mainnet", time.Minute)

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
	svc := NewService(fa, &fakeTokenStore{}, tc, allowUSDC(), denyValidator(), nil, "eth-mainnet", time.Minute)

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
	svc := NewService(fa, &fakeTokenStore{}, tc, allowUSDC(), denyValidator(), nil, "eth-mainnet", time.Minute)

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

func TestGetTokensKeepsValidatedUnlistedToken(t *testing.T) {
	fa := &fakeAlchemy{tokens: []alchemy.Token{
		{TokenAddress: usdc("0xFEE7"), Symbol: "pepe", Name: "old", Decimals: 9, RawBalance: "12500000"},
	}}
	v := &fakeValidator{result: Validation{Valid: true, Symbol: "PEPE", Name: "Pepe", LogoURI: "https://logo/pepe.png", Decimals: 18}}
	svc := NewService(fa, &fakeTokenStore{}, &fakeTxCache{}, allowUSDC(), v, nil, "eth-mainnet", time.Minute)

	p, err := svc.GetTokens(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Tokens) != 1 {
		t.Fatalf("got %d tokens, want 1", len(p.Tokens))
	}
	tok := p.Tokens[0]
	if tok.Symbol != "PEPE" || tok.Name != "Pepe" {
		t.Errorf("metadata not overlaid: %+v", tok)
	}
	if tok.LogoURI == nil || *tok.LogoURI != "https://logo/pepe.png" {
		t.Errorf("logo not set: %+v", tok.LogoURI)
	}
	if tok.Decimals != 18 {
		t.Errorf("decimals = %d, want 18", tok.Decimals)
	}
	if v.calls != 1 {
		t.Errorf("validator calls = %d, want 1", v.calls)
	}
}

func TestGetTokensDropsInvalidUnlistedToken(t *testing.T) {
	fa := &fakeAlchemy{tokens: []alchemy.Token{
		{TokenAddress: usdc("0xSPAM"), Symbol: "SCAM", Decimals: 18, RawBalance: "999"},
	}}
	svc := NewService(fa, &fakeTokenStore{}, &fakeTxCache{}, allowUSDC(), denyValidator(), nil, "eth-mainnet", time.Minute)
	p, err := svc.GetTokens(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Tokens) != 0 {
		t.Fatalf("expected invalid token dropped, got %d", len(p.Tokens))
	}
}

func TestGetNativeTokensMapsLifiToken(t *testing.T) {
	fetchedAt := time.Date(2026, 7, 22, 20, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	allow := &fakeAllowlist{
		native: lifi.ListToken{
			Address:  "0x0000000000000000000000000000000000000000",
			Symbol:   "ETH",
			Name:     "Ethereum",
			Decimals: 18,
			CoinKey:  "ETH",
			LogoURI:  "https://logo/eth.png",
			PriceUSD: "3200.50",
		},
		nativeFetchedAt: fetchedAt,
		nativeOK:        true,
	}
	svc := NewService(&fakeAlchemy{}, &fakeTokenStore{}, &fakeTxCache{}, allow, denyValidator(), nil, "eth-mainnet", time.Minute)

	got, err := svc.GetNativeTokens(context.Background())
	if err != nil {
		t.Fatalf("GetNativeTokens: %v", err)
	}
	want := []Token{{
		TokenAddress: nil,
		Symbol:       "ETH",
		Name:         "Ethereum",
		Decimals:     18,
		RawBalance:   "0",
		Balance:      "0",
		IsNative:     true,
		Price: &Price{
			Currency:      "usd",
			Value:         "3200.50",
			LastUpdatedAt: "2026-07-22T12:30:00Z",
		},
		LogoURI:  strptr("https://logo/eth.png"),
		CoinKey:  strptr("ETH"),
		PriceUSD: strptr("3200.50"),
	}}
	if got == nil || !reflect.DeepEqual(got, want) {
		t.Errorf("tokens = %+v, want %+v", got, want)
	}
}

func TestGetNativeTokensMarketEnrichmentRunsAfterLifiConstruction(t *testing.T) {
	allow := &fakeAllowlist{
		native:          lifi.ListToken{Symbol: "ETH", Name: "Ethereum", Decimals: 18, PriceUSD: "3200.50"},
		nativeFetchedAt: time.Date(2026, 7, 22, 12, 30, 0, 0, time.UTC),
		nativeOK:        true,
	}
	market := &fakeMarketEnricher{out: []Token{{Symbol: "MARKET_ETH"}}}
	svc := NewService(&fakeAlchemy{}, &fakeTokenStore{}, &fakeTxCache{}, allow, denyValidator(), market, "eth-mainnet", time.Minute)

	got, err := svc.GetNativeTokens(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if market.calls != 1 {
		t.Fatalf("market enricher calls = %d, want 1", market.calls)
	}
	if len(market.seen) != 1 || market.seen[0].Symbol != "ETH" || !market.seen[0].IsNative {
		t.Errorf("enricher saw %+v, want completed native ETH token", market.seen)
	}
	if !reflect.DeepEqual(got, market.out) {
		t.Errorf("tokens = %+v, want enricher output %+v", got, market.out)
	}
}

func TestGetNativeTokensLeavesOptionalMetadataNil(t *testing.T) {
	allow := &fakeAllowlist{
		native:          lifi.ListToken{Symbol: "ETH", Name: "Ethereum", Decimals: 18, PriceUSD: "3200.50"},
		nativeFetchedAt: time.Date(2026, 7, 22, 12, 30, 0, 0, time.UTC),
		nativeOK:        true,
	}
	svc := NewService(&fakeAlchemy{}, &fakeTokenStore{}, &fakeTxCache{}, allow, denyValidator(), nil, "eth-mainnet", time.Minute)

	got, err := svc.GetNativeTokens(context.Background())
	if err != nil {
		t.Fatalf("GetNativeTokens: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("token count = %d, want 1", len(got))
	}
	if got[0].LogoURI != nil || got[0].CoinKey != nil {
		t.Errorf("optional metadata should remain nil: %+v", got[0])
	}
}

func TestGetNativeTokensTrimsPrice(t *testing.T) {
	allow := &fakeAllowlist{
		native:          lifi.ListToken{Symbol: "ETH", Name: "Ethereum", Decimals: 18, PriceUSD: "  3200.50  "},
		nativeFetchedAt: time.Date(2026, 7, 22, 12, 30, 0, 0, time.UTC),
		nativeOK:        true,
	}
	svc := NewService(&fakeAlchemy{}, &fakeTokenStore{}, &fakeTxCache{}, allow, denyValidator(), nil, "eth-mainnet", time.Minute)

	got, err := svc.GetNativeTokens(context.Background())
	if err != nil {
		t.Fatalf("GetNativeTokens: %v", err)
	}
	if got[0].Price.Value != "3200.50" {
		t.Errorf("price.value = %q, want trimmed %q", got[0].Price.Value, "3200.50")
	}
	if got[0].PriceUSD == nil || *got[0].PriceUSD != "3200.50" {
		t.Errorf("priceUSD = %v, want trimmed %q", got[0].PriceUSD, "3200.50")
	}
}

func TestGetNativeTokensUnavailable(t *testing.T) {
	fetchedAt := time.Date(2026, 7, 22, 12, 30, 0, 0, time.UTC)
	tests := []struct {
		name  string
		allow *fakeAllowlist
	}{
		{name: "missing snapshot or native entry", allow: &fakeAllowlist{}},
		{name: "empty price", allow: &fakeAllowlist{
			native: lifi.ListToken{Symbol: "ETH"}, nativeFetchedAt: fetchedAt, nativeOK: true,
		}},
		{name: "zero fetch time", allow: &fakeAllowlist{
			native: lifi.ListToken{Symbol: "ETH", PriceUSD: "3200.50"}, nativeOK: true,
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(&fakeAlchemy{}, &fakeTokenStore{}, &fakeTxCache{}, tt.allow, denyValidator(), nil, "eth-mainnet", time.Minute)
			got, err := svc.GetNativeTokens(context.Background())
			if got != nil {
				t.Errorf("tokens = %+v, want nil", got)
			}
			if !errors.Is(err, ErrNativeTokenUnavailable) {
				t.Errorf("error = %v, want ErrNativeTokenUnavailable", err)
			}
		})
	}
}

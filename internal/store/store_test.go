package store

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"wallet-api/internal/lifi"
	"wallet-api/internal/marketdata"
	"wallet-api/internal/tokenvalidity"
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
	_, _ = s.pool.Exec(ctx, "TRUNCATE wallet_tokens, token_fetch_meta, tx_cache, lifi_token_lists, token_metadata, coingecko_coin_mappings, coingecko_market_data")
	t.Cleanup(s.Close)
	return s
}

func usdc(a string) *string { return &a }

func TestSchemaStoresMarketPriceUSDAsText(t *testing.T) {
	normalized := strings.Join(strings.Fields(schemaSQL), " ")
	if !strings.Contains(normalized, "price_usd TEXT") {
		t.Fatal("coingecko_market_data.price_usd is not declared as TEXT")
	}
	if !strings.Contains(normalized, "ALTER COLUMN price_usd TYPE TEXT USING price_usd::text") {
		t.Fatal("schema does not migrate an existing numeric price_usd column to TEXT")
	}
}

type fakeBatchResults struct {
	execErr    error
	closeErr   error
	execCalls  int
	closeCalls int
}

func (r *fakeBatchResults) Exec() (pgconn.CommandTag, error) {
	r.execCalls++
	return pgconn.CommandTag{}, r.execErr
}

func (r *fakeBatchResults) Query() (pgx.Rows, error) { panic("unused") }

func (r *fakeBatchResults) QueryRow() pgx.Row { panic("unused") }

func (r *fakeBatchResults) Close() error {
	r.closeCalls++
	return r.closeErr
}

func TestFinishMarketBatchReturnsCloseError(t *testing.T) {
	closeErr := errors.New("batch close failed")
	results := &fakeBatchResults{closeErr: closeErr}
	if err := finishMarketBatch(results, 1); !errors.Is(err, closeErr) {
		t.Fatalf("finishMarketBatch error = %v, want %v", err, closeErr)
	}
	if results.execCalls != 1 || results.closeCalls != 1 {
		t.Fatalf("batch calls = exec %d close %d, want 1/1", results.execCalls, results.closeCalls)
	}
}

func TestFinishMarketBatchPreservesFirstExecError(t *testing.T) {
	execErr := errors.New("batch exec failed")
	closeErr := errors.New("batch close failed")
	results := &fakeBatchResults{execErr: execErr, closeErr: closeErr}
	err := finishMarketBatch(results, 2)
	if !errors.Is(err, execErr) {
		t.Fatalf("finishMarketBatch error = %v, want first exec error %v", err, execErr)
	}
	if errors.Is(err, closeErr) {
		t.Fatalf("finishMarketBatch returned close error instead of first exec error: %v", err)
	}
	if results.execCalls != 1 || results.closeCalls != 1 {
		t.Fatalf("batch calls = exec %d close %d, want 1/1", results.execCalls, results.closeCalls)
	}
}

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

func TestSaveAndGetTokenMeta(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Second)
	r := tokenvalidity.Record{PossibleSpam: false, Verified: true, Symbol: "PEPE", Name: "Pepe", Logo: "https://logo/pepe.png", Decimals: 18, FetchedAt: at}

	if _, ok, err := s.GetTokenMeta(ctx, "eth", "0xFEE7"); err != nil || ok {
		t.Fatalf("empty get: ok=%v err=%v", ok, err)
	}
	if err := s.SaveTokenMeta(ctx, "eth", "0xFEE7", r); err != nil {
		t.Fatalf("SaveTokenMeta: %v", err)
	}
	got, ok, err := s.GetTokenMeta(ctx, "eth", "0xFEE7")
	if err != nil || !ok {
		t.Fatalf("GetTokenMeta ok=%v err=%v", ok, err)
	}
	if got.Symbol != "PEPE" || got.Logo != "https://logo/pepe.png" || got.Decimals != 18 || !got.Verified || got.PossibleSpam {
		t.Errorf("round-trip wrong: %+v", got)
	}
	if !got.FetchedAt.Equal(at) {
		t.Errorf("fetchedAt = %v, want %v", got.FetchedAt, at)
	}

	// Upsert overwrites.
	r2 := r
	r2.PossibleSpam = true
	r2.Symbol = "SPAM"
	if err := s.SaveTokenMeta(ctx, "eth", "0xFEE7", r2); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got2, _, _ := s.GetTokenMeta(ctx, "eth", "0xFEE7")
	if !got2.PossibleSpam || got2.Symbol != "SPAM" {
		t.Errorf("upsert did not overwrite: %+v", got2)
	}
}

func TestLoadCoinMappingsAbsent(t *testing.T) {
	s := newTestStore(t)
	got, ok, err := s.LoadCoinMappings(context.Background())
	if err != nil || ok || got != nil {
		t.Fatalf("LoadCoinMappings empty ok=%v err=%v got=%#v", ok, err, got)
	}
}

func TestReplaceAndLoadCoinMappings(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	fetchedAt := time.Now().UTC().Truncate(time.Second)
	first := []marketdata.CoinMapping{
		{ID: "ethereum", Name: "Ethereum", Symbol: "eth", Chain: "eth", Address: marketdata.NativeAddress, FetchedAt: fetchedAt},
		{ID: "usd-coin", Name: "USDC", Symbol: "usdc", Chain: "ethereum", Address: "0xa0b8", FetchedAt: fetchedAt},
	}
	if err := s.ReplaceCoinMappings(ctx, first); err != nil {
		t.Fatalf("ReplaceCoinMappings: %v", err)
	}
	got, ok, err := s.LoadCoinMappings(ctx)
	if err != nil || !ok || !reflect.DeepEqual(got, first) {
		t.Fatalf("LoadCoinMappings ok=%v err=%v got=%#v", ok, err, got)
	}
}

func TestReplaceCoinMappingsReplacesAllRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	fetchedAt := time.Now().UTC().Truncate(time.Second)
	first := []marketdata.CoinMapping{
		{ID: "old-coin", Name: "Old", Symbol: "old", Chain: "ethereum", Address: "0xold", FetchedAt: fetchedAt},
	}
	second := []marketdata.CoinMapping{
		{ID: "new-coin", Name: "New", Symbol: "new", Chain: "ethereum", Address: "0xnew", FetchedAt: fetchedAt},
	}
	if err := s.ReplaceCoinMappings(ctx, first); err != nil {
		t.Fatalf("first ReplaceCoinMappings: %v", err)
	}
	if err := s.ReplaceCoinMappings(ctx, second); err != nil {
		t.Fatalf("second ReplaceCoinMappings: %v", err)
	}
	got, ok, err := s.LoadCoinMappings(ctx)
	if err != nil || !ok || !reflect.DeepEqual(got, second) {
		t.Fatalf("LoadCoinMappings after replacement ok=%v err=%v got=%#v", ok, err, got)
	}
}

func TestReplaceCoinMappingsRollsBack(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	fetchedAt := time.Now().UTC().Truncate(time.Second)
	original := []marketdata.CoinMapping{
		{ID: "ethereum", Name: "Ethereum", Symbol: "eth", Chain: "eth", Address: marketdata.NativeAddress, FetchedAt: fetchedAt},
	}
	if err := s.ReplaceCoinMappings(ctx, original); err != nil {
		t.Fatalf("initial ReplaceCoinMappings: %v", err)
	}
	duplicate := []marketdata.CoinMapping{
		{ID: "new-coin", Name: "New", Symbol: "new", Chain: "ethereum", Address: "0xnew", FetchedAt: fetchedAt},
		{ID: "new-coin", Name: "Changed", Symbol: "new", Chain: "ethereum", Address: "0xnew", FetchedAt: fetchedAt},
	}
	if err := s.ReplaceCoinMappings(ctx, duplicate); err == nil {
		t.Fatal("ReplaceCoinMappings duplicate input returned nil error")
	}
	got, ok, err := s.LoadCoinMappings(ctx)
	if err != nil || !ok || !reflect.DeepEqual(got, original) {
		t.Fatalf("LoadCoinMappings after rollback ok=%v err=%v got=%#v", ok, err, got)
	}
}

func TestMarketDataBatchRoundTripAndRequestedKeys(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	price := "1.0001"
	change := -0.25
	capUSD := 32_000_000_000.0
	updatedAt := time.Unix(1_784_781_600, 0).UTC()
	fetchedAt := time.Unix(2_000, 0).UTC()
	first := marketdata.Record{
		Key: marketdata.ContractKey("ethereum", "0xA0B8"), CoinGeckoID: "usd-coin",
		PriceUSD: &price, Change24HPercent: &change, MarketCapUSD: &capUSD,
		MarketDataUpdatedAt: &updatedAt, FetchedAt: fetchedAt,
	}
	second := marketdata.Record{
		Key: marketdata.NativeKey("Ethereum", "ETH"), CoinGeckoID: "ethereum", FetchedAt: fetchedAt,
	}
	if err := s.SaveMarketData(ctx, []marketdata.Record{first, second}); err != nil {
		t.Fatalf("SaveMarketData: %v", err)
	}

	newPrice := "1.0002"
	first.PriceUSD = &newPrice
	first.CoinGeckoID = "usd-coin-updated"
	if err := s.SaveMarketData(ctx, []marketdata.Record{first}); err != nil {
		t.Fatalf("SaveMarketData upsert: %v", err)
	}

	wanted := []marketdata.Key{first.Key, second.Key, marketdata.ContractKey("ethereum", "0xmissing")}
	got, err := s.LoadMarketData(ctx, wanted)
	if err != nil {
		t.Fatalf("LoadMarketData: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("LoadMarketData returned %d records, want 2: %#v", len(got), got)
	}
	if got[first.Key].CoinGeckoID != "usd-coin-updated" || got[first.Key].PriceUSD == nil || *got[first.Key].PriceUSD != newPrice {
		t.Fatalf("updated record = %#v", got[first.Key])
	}
	if !reflect.DeepEqual(got[first.Key].FetchedAt, fetchedAt) {
		t.Errorf("FetchedAt = %#v, want UTC timestamp %#v", got[first.Key].FetchedAt, fetchedAt)
	}
	if got[first.Key].MarketDataUpdatedAt == nil || !reflect.DeepEqual(*got[first.Key].MarketDataUpdatedAt, updatedAt) {
		t.Errorf("MarketDataUpdatedAt = %#v, want UTC timestamp %#v", got[first.Key].MarketDataUpdatedAt, updatedAt)
	}
	if got[second.Key].CoinGeckoID != "ethereum" || got[second.Key].PriceUSD != nil || got[second.Key].MarketDataUpdatedAt != nil {
		t.Fatalf("nullable record = %#v", got[second.Key])
	}
}

func TestMarketDataPriceUSDPreservesExactScientificNotation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	wantByKey := map[marketdata.Key]string{
		marketdata.ContractKey("ethereum", "0xsmall"): "1e-400",
		marketdata.ContractKey("ethereum", "0xlarge"): "1e400",
	}
	records := make([]marketdata.Record, 0, len(wantByKey))
	keys := make([]marketdata.Key, 0, len(wantByKey))
	for key, price := range wantByKey {
		price := price
		keys = append(keys, key)
		records = append(records, marketdata.Record{
			Key: key, CoinGeckoID: key.TokenKey, PriceUSD: &price,
			FetchedAt: time.Unix(2_000, 0).UTC(),
		})
	}
	if err := s.SaveMarketData(ctx, records); err != nil {
		t.Fatalf("SaveMarketData: %v", err)
	}

	got, err := s.LoadMarketData(ctx, keys)
	if err != nil {
		t.Fatalf("LoadMarketData: %v", err)
	}
	for key, want := range wantByKey {
		if got[key].PriceUSD == nil || *got[key].PriceUSD != want {
			t.Errorf("PriceUSD for %v = %#v, want exact %q", key, got[key].PriceUSD, want)
		}
	}
}

func TestMarketDataOlderPostgresWriteCannotReplaceNewerRecord(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	key := marketdata.ContractKey("ethereum", "0xA0B8")
	newPrice := "2.00"
	oldPrice := "1.00"
	newer := marketdata.Record{Key: key, CoinGeckoID: "newer", PriceUSD: &newPrice, FetchedAt: time.Unix(2_000, 0).UTC()}
	older := marketdata.Record{Key: key, CoinGeckoID: "older", PriceUSD: &oldPrice, FetchedAt: time.Unix(1_000, 0).UTC()}
	if err := s.SaveMarketData(ctx, []marketdata.Record{newer}); err != nil {
		t.Fatalf("newer SaveMarketData: %v", err)
	}
	if err := s.SaveMarketData(ctx, []marketdata.Record{older}); err != nil {
		t.Fatalf("older SaveMarketData: %v", err)
	}
	got, err := s.LoadMarketData(ctx, []marketdata.Key{key})
	if err != nil {
		t.Fatalf("LoadMarketData: %v", err)
	}
	if got[key].CoinGeckoID != "newer" || got[key].PriceUSD == nil || *got[key].PriceUSD != newPrice {
		t.Fatalf("older write replaced newer record: %#v", got[key])
	}
}

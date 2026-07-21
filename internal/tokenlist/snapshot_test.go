package tokenlist

import (
	"testing"
	"time"

	"wallet-api/internal/lifi"
)

func sampleTokens() []lifi.ListToken {
	return []lifi.ListToken{
		{Address: "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", Symbol: "USDC", Name: "USD Coin", Decimals: 6, LogoURI: "u", PriceUSD: "1.00"},
		{Address: "0xdAC17F958D2ee523a2206206994597C13D831ec7", Symbol: "USDT", Name: "Tether", Decimals: 6},
	}
}

func TestSnapshotLookupByAddressCaseInsensitive(t *testing.T) {
	s := NewSnapshot("ETH", sampleTokens(), time.Now())
	got, ok := s.LookupByAddress("0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48")
	if !ok {
		t.Fatal("expected USDC lookup hit (lowercased)")
	}
	if got.Symbol != "USDC" || got.Decimals != 6 {
		t.Errorf("wrong token: %+v", got)
	}
	if _, ok := s.LookupByAddress("0xdeadbeef"); ok {
		t.Error("unknown address should miss")
	}
}

func TestSnapshotHasSymbolCaseInsensitive(t *testing.T) {
	s := NewSnapshot("ETH", sampleTokens(), time.Now())
	if !s.HasSymbol("usdc") || !s.HasSymbol("USDT") {
		t.Error("expected known symbols to match case-insensitively")
	}
	if s.HasSymbol("SCAM") {
		t.Error("unknown symbol should not match")
	}
}

func TestHolderAtomicSetAndCurrent(t *testing.T) {
	var h Holder
	if h.Current() != nil {
		t.Fatal("zero-value holder Current() should be nil")
	}
	if _, ok := h.LookupByAddress("0xanything"); ok {
		t.Error("nil snapshot lookup should miss, not panic")
	}
	if h.HasSymbol("ETH") {
		t.Error("nil snapshot HasSymbol should be false")
	}
	h.Set(NewSnapshot("ETH", sampleTokens(), time.Now()))
	if h.Current() == nil || h.Current().Count() != 2 {
		t.Fatalf("after Set, Count = %v, want 2", h.Current())
	}
	if _, ok := h.LookupByAddress("0xdAC17F958D2ee523a2206206994597C13D831ec7"); !ok {
		t.Error("holder should delegate lookup to current snapshot")
	}
}

func TestSnapshotLookupNative(t *testing.T) {
	fetchedAt := time.Date(2026, 7, 22, 12, 30, 0, 0, time.UTC)
	native := lifi.ListToken{
		Address:  "0x0000000000000000000000000000000000000000",
		Symbol:   "ETH",
		Name:     "Ethereum",
		Decimals: 18,
		PriceUSD: "3200.50",
	}
	s := NewSnapshot("ETH", append(sampleTokens(), native), fetchedAt)

	got, gotAt, ok := s.LookupNative()
	if !ok {
		t.Fatal("expected native ETH lookup hit")
	}
	if got != native {
		t.Errorf("token = %+v, want %+v", got, native)
	}
	if !gotAt.Equal(fetchedAt) {
		t.Errorf("fetchedAt = %v, want %v", gotAt, fetchedAt)
	}

	missing := NewSnapshot("ETH", sampleTokens(), fetchedAt)
	if _, _, ok := missing.LookupNative(); ok {
		t.Error("snapshot without zero-address token should miss")
	}
}

func TestHolderLookupNative(t *testing.T) {
	var h Holder
	if _, _, ok := h.LookupNative(); ok {
		t.Error("holder without a snapshot should miss, not panic")
	}

	fetchedAt := time.Date(2026, 7, 22, 12, 30, 0, 0, time.UTC)
	native := lifi.ListToken{
		Address:  "0x0000000000000000000000000000000000000000",
		Symbol:   "ETH",
		PriceUSD: "3200.50",
	}
	h.Set(NewSnapshot("ETH", []lifi.ListToken{native}, fetchedAt))

	got, gotAt, ok := h.LookupNative()
	if !ok || got.Symbol != "ETH" || !gotAt.Equal(fetchedAt) {
		t.Fatalf("holder native lookup = (%+v, %v, %v)", got, gotAt, ok)
	}
}

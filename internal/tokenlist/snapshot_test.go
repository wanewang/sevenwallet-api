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

package marketdata

import (
	"reflect"
	"testing"
	"time"

	"wallet-api/internal/coingecko"
)

func TestBuildMappingsExpandsPlatformsAndNativeCoins(t *testing.T) {
	fetchedAt := time.Unix(1_700_000_000, 0).UTC()
	coins := []coingecko.Coin{
		{ID: "usd-coin", Name: "USDC", Symbol: "usdc", Platforms: map[string]string{
			"ethereum": "0xA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48",
			"solana":   "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v",
		}},
		{ID: "ethereum", Name: "Ethereum", Symbol: "ETH", Platforms: map[string]string{}},
	}
	got := BuildMappings(coins, fetchedAt)
	want := []CoinMapping{
		{ID: "ethereum", Name: "Ethereum", Symbol: "ETH", Chain: "eth", Address: NativeAddress, FetchedAt: fetchedAt},
		{ID: "usd-coin", Name: "USDC", Symbol: "usdc", Chain: "ethereum", Address: "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48", FetchedAt: fetchedAt},
		{ID: "usd-coin", Name: "USDC", Symbol: "usdc", Chain: "solana", Address: "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v", FetchedAt: fetchedAt},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mappings = %#v, want %#v", got, want)
	}
}

func TestBuildMappingsIgnoresBlankPlatforms(t *testing.T) {
	fetchedAt := time.Unix(1_700_000_000, 0).UTC()
	coins := []coingecko.Coin{{
		ID: "usd-coin", Name: " USDC ", Symbol: " usdc ", Platforms: map[string]string{
			" ":         "0xdeadbeef",
			"ethereum":  "  ",
			" base ":    " 0xABC ",
			" optimism": "0x123 ",
		},
	}}

	got := BuildMappings(coins, fetchedAt)
	want := []CoinMapping{
		{ID: "usd-coin", Name: "USDC", Symbol: "usdc", Chain: "base", Address: "0xabc", FetchedAt: fetchedAt},
		{ID: "usd-coin", Name: "USDC", Symbol: "usdc", Chain: "optimism", Address: "0x123", FetchedAt: fetchedAt},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mappings = %#v, want %#v", got, want)
	}
}

func TestBuildMappingsUsesNativeForNoUsablePlatform(t *testing.T) {
	fetchedAt := time.Unix(1_700_000_000, 0).UTC()
	coins := []coingecko.Coin{{
		ID: "solana", Name: " Solana ", Symbol: " SOL ", Platforms: map[string]string{"solana": " "},
	}}

	got := BuildMappings(coins, fetchedAt)
	want := []CoinMapping{{
		ID: "solana", Name: "Solana", Symbol: "SOL", Chain: "sol", Address: NativeAddress, FetchedAt: fetchedAt,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mappings = %#v, want %#v", got, want)
	}
}

func TestBuildMappingsSkipsBlankIDsAndLeavesLookupsUnresolved(t *testing.T) {
	coins := []coingecko.Coin{
		{ID: " ", Symbol: " ETH ", Platforms: map[string]string{
			"ethereum": "0xabc",
		}},
		{ID: "\t", Symbol: " SOL ", Platforms: map[string]string{}},
	}

	mappings := BuildMappings(coins, time.Unix(1_700_000_000, 0).UTC())
	if len(mappings) != 0 {
		t.Fatalf("mappings = %#v, want no rows for blank IDs", mappings)
	}

	catalog := NewCatalog(mappings)
	if _, ok := catalog.ResolveContract("ethereum", "0xabc"); ok {
		t.Fatal("blank contract ID should remain unresolved")
	}
	if _, ok := catalog.ResolveNative("SOL", nil); ok {
		t.Fatal("blank native ID should remain unresolved")
	}
}

func TestCatalogResolveEthereumAddressCaseInsensitive(t *testing.T) {
	catalog := NewCatalog([]CoinMapping{{
		ID: "usd-coin", Chain: "ethereum", Address: "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
	}})

	if got, ok := catalog.ResolveContract("ETHEREUM", " 0xA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48 "); !ok || got != "usd-coin" {
		t.Fatalf("ResolveContract() = (%q, %v), want (usd-coin, true)", got, ok)
	}
}

func TestCatalogResolveSolanaAddressCaseSensitive(t *testing.T) {
	catalog := NewCatalog([]CoinMapping{{
		ID: "usd-coin", Chain: "solana", Address: "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v",
	}})

	if got, ok := catalog.ResolveContract("solana", "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"); !ok || got != "usd-coin" {
		t.Fatalf("exact Solana lookup = (%q, %v), want (usd-coin, true)", got, ok)
	}
	if _, ok := catalog.ResolveContract("solana", "ePjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"); ok {
		t.Fatal("case-changed Solana lookup should miss")
	}
}

func TestCatalogDuplicateContractIDsAreAmbiguous(t *testing.T) {
	catalog := NewCatalog([]CoinMapping{
		{ID: "first", Chain: "ethereum", Address: "0xabc"},
		{ID: "second", Chain: "ethereum", Address: "0xABC"},
	})

	if _, ok := catalog.ResolveContract("ethereum", "0xabc"); ok {
		t.Fatal("duplicate contract IDs should be ambiguous")
	}
}

func TestCatalogResolveNativeFiltersAllowedIDs(t *testing.T) {
	catalog := NewCatalog([]CoinMapping{
		{ID: "ethereum", Symbol: "ETH", Chain: "eth", Address: NativeAddress},
		{ID: "wrapped-ether", Symbol: "eth", Chain: "eth", Address: NativeAddress},
	})

	if _, ok := catalog.ResolveNative(" eth ", map[string]struct{}{"wrapped-ether": {}}); !ok {
		t.Fatal("allowed native ID should resolve")
	}
	if _, ok := catalog.ResolveNative("ETH", map[string]struct{}{"unknown": {}}); ok {
		t.Fatal("native ID outside the allowlist should miss")
	}
}

func TestCatalogDuplicateNativeSameIDIsNotAmbiguous(t *testing.T) {
	catalog := NewCatalog([]CoinMapping{
		{ID: "ethereum", Symbol: "ETH", Chain: "eth", Address: NativeAddress},
		{ID: "ethereum", Symbol: "eth", Chain: "eth", Address: NativeAddress},
	})

	if got, ok := catalog.ResolveNative("ETH", nil); !ok || got != "ethereum" {
		t.Fatalf("duplicate native ID = (%q, %v), want (ethereum, true)", got, ok)
	}
}

func TestCatalogCountReportsMappingRows(t *testing.T) {
	mappings := []CoinMapping{
		{ID: "ethereum", Symbol: "ETH", Chain: "eth", Address: NativeAddress},
		{ID: "usd-coin", Chain: "ethereum", Address: "0xabc"},
	}

	if got := NewCatalog(mappings).Count(); got != len(mappings) {
		t.Fatalf("catalog Count() = %d, want %d", got, len(mappings))
	}
}

func TestHolderNilReturnsMissesWithoutPanicking(t *testing.T) {
	var holder Holder
	if holder.Current() != nil {
		t.Fatal("zero-value holder should have no catalog")
	}
	if got := holder.Count(); got != 0 {
		t.Fatalf("nil holder Count() = %d, want 0", got)
	}
	if _, ok := holder.ResolveContract("ethereum", "0xabc"); ok {
		t.Fatal("nil holder contract lookup should miss")
	}
	if _, ok := holder.ResolveNative("ETH", nil); ok {
		t.Fatal("nil holder native lookup should miss")
	}
}

func TestHolderCountDelegatesToCurrentCatalog(t *testing.T) {
	var holder Holder
	holder.Set(NewCatalog([]CoinMapping{{ID: "ethereum", Address: NativeAddress}}))

	if got := holder.Count(); got != 1 {
		t.Fatalf("holder Count() = %d, want 1", got)
	}
}

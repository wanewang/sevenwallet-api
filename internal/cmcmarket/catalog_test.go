package cmcmarket

import (
	"reflect"
	"testing"
	"time"

	"wallet-api/internal/coinmarketcap"
)

func TestBuildMappingsFiltersAndNormalizes(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	coins := []coinmarketcap.Coin{
		{ID: 1660, Name: " Monolith ", Symbol: " TKN ", IsActive: 1, Platform: &coinmarketcap.Platform{ID: 1, TokenAddress: " 0xAaAF "}},
		{ID: 2, Symbol: "OLD", IsActive: 0, Platform: &coinmarketcap.Platform{ID: 1, TokenAddress: "0xold"}},
		{ID: 3, Symbol: "BSC", IsActive: 1, Platform: &coinmarketcap.Platform{ID: 14, TokenAddress: "0xbsc"}},
		{ID: 1027, Symbol: "ETH", IsActive: 1},
	}
	want := []CoinMapping{{ID: 1660, Name: "Monolith", Symbol: "TKN", PlatformID: 1, Address: "0xaaaf", FetchedAt: at}}
	if got := BuildMappings(coins, map[int64]struct{}{1: {}}, at); !reflect.DeepEqual(got, want) {
		t.Fatalf("mappings = %#v, want %#v", got, want)
	}
}

func TestResolveContractUsesSymbolOnlyForAmbiguity(t *testing.T) {
	catalog := NewCatalog([]CoinMapping{
		{ID: 10, Symbol: "AAA", PlatformID: 1, Address: "0xAbC"},
		{ID: 20, Symbol: "BBB", PlatformID: 1, Address: "0xabc"},
	})
	if got, ok := catalog.ResolveContract(1, " 0xABC ", "bbb"); !ok || got != 20 {
		t.Fatalf("symbol resolution = (%d,%v)", got, ok)
	}
	if _, ok := catalog.ResolveContract(1, "0xabc", "CCC"); ok {
		t.Fatal("unmatched symbol should remain ambiguous")
	}

	single := NewCatalog([]CoinMapping{{ID: 10, Symbol: "AAA", PlatformID: 1, Address: "0xabc"}})
	if got, ok := single.ResolveContract(1, "0xABC", "wrong"); !ok || got != 10 {
		t.Fatalf("sole candidate = (%d,%v)", got, ok)
	}
}

func TestResolveContractSkipsSameSymbolAmbiguity(t *testing.T) {
	catalog := NewCatalog([]CoinMapping{
		{ID: 10, Symbol: "TKN", PlatformID: 1, Address: "0xabc"},
		{ID: 20, Symbol: "tkn", PlatformID: 1, Address: "0xabc"},
	})
	if _, ok := catalog.ResolveContract(1, "0xabc", "TKN"); ok {
		t.Fatal("same-symbol duplicate should remain ambiguous")
	}
}

func TestHolderZeroValueIsSafe(t *testing.T) {
	var holder Holder
	if holder.Count() != 0 {
		t.Fatal("empty holder count")
	}
	if _, ok := holder.ResolveContract(1, "0xabc", "TKN"); ok {
		t.Fatal("empty holder resolved")
	}
	holder.Set(NewCatalog([]CoinMapping{{ID: 1, PlatformID: 1, Address: "0xabc"}}))
	if holder.Count() != 1 {
		t.Fatal("holder did not publish catalog")
	}
}

package rediscache

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"wallet-api/internal/cmcmarket"
	"wallet-api/internal/marketdata"
	"wallet-api/internal/marketkey"
)

// These tests need no Redis. They pin the two things a refactor of the market
// packages can silently break: the Redis key strings, and the stored JSON that
// both CAS scripts parse. The literals were captured from the implementation
// before the shared-pipeline refactor.

func TestMarketKeyStringsAreStable(t *testing.T) {
	contract := marketkey.ContractKey("Ethereum", "0xA0B8")
	native := marketkey.NativeKey("Ethereum", "eth")

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"coingecko contract", marketKey(contract), "coingecko:market:ethereum:0xa0b8"},
		{"coingecko native", marketKey(native), "coingecko:market:ethereum:native:ETH"},
		{"cmc contract", cmcMarketKey(contract), "cmc:market:ethereum:0xa0b8"},
		{"cmc native", cmcMarketKey(native), "cmc:market:ethereum:native:ETH"},
		// The negative cache lives in its own namespace so a miss can never be
		// mistaken for, or collide with, a market record.
		{"coingecko miss", coinGeckoMissKey(contract), "coingecko:miss:ethereum:0xa0b8"},
		{"coingecko miss native", coinGeckoMissKey(native), "coingecko:miss:ethereum:native:ETH"},
		{"cmc miss", cmcMissKey(contract), "cmc:miss:ethereum:0xa0b8"},
		{"cmc miss native", cmcMissKey(native), "cmc:miss:ethereum:native:ETH"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("key = %q, want %q", tc.got, tc.want)
			}
		})
	}
}

// The CMC script used to be produced by string-replacing a literal line of the
// CoinGecko script. Any whitespace edit there silently produced a script with
// no coinMarketCapID validation at all. These assertions fail loudly instead.
func TestBothCASScriptsCarryTheirOwnValidation(t *testing.T) {
	if strings.Contains(marketCASLua, casValidationMarker) || strings.Contains(cmcMarketCASLua, casValidationMarker) {
		t.Fatal("a CAS script still contains the unsubstituted validation marker")
	}

	t.Run("coingecko requires its own ID", func(t *testing.T) {
		if !strings.Contains(marketCASLua, "'coingeckoID'") {
			t.Error("CoinGecko script lost its coingeckoID requirement")
		}
		if strings.Contains(marketCASLua, "coinMarketCapID") {
			t.Error("CoinGecko script picked up CMC validation")
		}
	})

	t.Run("cmc requires a positive numeric ID", func(t *testing.T) {
		if !strings.Contains(cmcMarketCASLua, "decoded['coinMarketCapID'] <= 0") {
			t.Error("CMC script lost its coinMarketCapID validation")
		}
		if strings.Contains(cmcMarketCASLua, "'coingeckoID'") {
			t.Error("CMC script still requires a coingeckoID")
		}
	})

	// Both must keep the out-of-order write guard the template provides.
	for name, script := range map[string]string{"coingecko": marketCASLua, "cmc": cmcMarketCASLua} {
		t.Run(name+" rejects older writes", func(t *testing.T) {
			if !strings.Contains(script, "compareDecimals(ARGV[2], decoded['fetchedAtUnixNano']) == -1") {
				t.Error("script lost its timestamp comparison")
			}
			if !strings.Contains(script, "canonicalInt64(decoded['fetchedAtUnixNano'])") {
				t.Error("script lost its fetchedAtUnixNano validation")
			}
		})
	}
}

func TestMarketPayloadShapeIsStable(t *testing.T) {
	fetchedAt := time.Unix(1_700_000_000, 0).UTC()
	marketDataUpdatedAt := time.Unix(1_699_999_000, 0).UTC()
	price := "1.23"
	change := 4.5
	marketCap := 6.7

	t.Run("coingecko", func(t *testing.T) {
		encoded, err := json.Marshal(marketPayload{
			Record: marketdata.Record{
				Key:                 marketkey.ContractKey("ethereum", "0xABC"),
				CoinGeckoID:         "usd-coin",
				PriceUSD:            &price,
				Change24HPercent:    &change,
				MarketCapUSD:        &marketCap,
				MarketDataUpdatedAt: &marketDataUpdatedAt,
				FetchedAt:           fetchedAt,
			},
			FetchedAtUnixNano: "1700000000000000000",
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		want := `{"chain":"ethereum","tokenKey":"0xabc","coingeckoID":"usd-coin","priceUSD":"1.23","change24hPercent":4.5,"marketCapUSD":6.7,"marketDataUpdatedAt":"2023-11-14T21:56:40Z","fetchedAt":"2023-11-14T22:13:20Z","fetchedAtUnixNano":"1700000000000000000"}`
		if string(encoded) != want {
			t.Errorf("payload changed\n got: %s\nwant: %s", encoded, want)
		}
	})

	t.Run("coinmarketcap", func(t *testing.T) {
		encoded, err := json.Marshal(cmcMarketPayload{
			Record: cmcmarket.Record{
				Key:             marketkey.ContractKey("ethereum", "0xABC"),
				CoinMarketCapID: 1660,
				PriceUSD:        &price,
				Change24H:       &change,
				FetchedAt:       fetchedAt,
			},
			FetchedAtUnixNano: "1700000000000000000",
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		want := `{"chain":"ethereum","tokenKey":"0xabc","coinMarketCapID":1660,"priceUSD":"1.23","change24hPercent":4.5,"fetchedAt":"2023-11-14T22:13:20Z","fetchedAtUnixNano":"1700000000000000000"}`
		if string(encoded) != want {
			t.Errorf("payload changed\n got: %s\nwant: %s", encoded, want)
		}
	})
}

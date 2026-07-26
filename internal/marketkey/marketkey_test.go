package marketkey

import (
	"testing"
	"time"
)

// Cache and database keys derive from these outputs. The literals below were
// captured from the pre-refactor implementation; changing them strands every
// stored record, so they are asserted rather than recomputed.
func TestKeyNormalizationIsStable(t *testing.T) {
	tests := []struct {
		name         string
		key          Key
		wantChain    string
		wantTokenKey string
	}{
		{
			name:         "contract lowercases chain and hex address",
			key:          ContractKey("Ethereum", "0xA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48"),
			wantChain:    "ethereum",
			wantTokenKey: "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
		},
		{
			name:         "contract trims surrounding whitespace",
			key:          ContractKey("  ethereum  ", "  0xdAC17F958D2ee523a2206206994597C13D831ec7  "),
			wantChain:    "ethereum",
			wantTokenKey: "0xdac17f958d2ee523a2206206994597c13d831ec7",
		},
		{
			name:         "contract leaves non-hex identifiers untouched",
			key:          ContractKey("solana", "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"),
			wantChain:    "solana",
			wantTokenKey: "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v",
		},
		{
			name:         "native uppercases the symbol",
			key:          NativeKey("Ethereum", "eth"),
			wantChain:    "ethereum",
			wantTokenKey: "native:ETH",
		},
		{
			name:         "native trims surrounding whitespace",
			key:          NativeKey("ethereum", "  eth  "),
			wantChain:    "ethereum",
			wantTokenKey: "native:ETH",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.key.Chain != tc.wantChain {
				t.Errorf("Chain = %q, want %q", tc.key.Chain, tc.wantChain)
			}
			if tc.key.TokenKey != tc.wantTokenKey {
				t.Errorf("TokenKey = %q, want %q", tc.key.TokenKey, tc.wantTokenKey)
			}
		})
	}
}

func TestNormalizePlatformAddress(t *testing.T) {
	tests := []struct{ in, want string }{
		{"0xABC", "0xabc"},
		{"  0xABC  ", "0xabc"},
		{"NotHex", "NotHex"},
		{"  NotHex  ", "NotHex"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := NormalizePlatformAddress(tc.in); got != tc.want {
			t.Errorf("NormalizePlatformAddress(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRemainingTTLAndFresh(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	ttl := 30 * time.Minute

	tests := []struct {
		name      string
		fetchedAt time.Time
		wantTTL   time.Duration
		wantFresh bool
	}{
		{"just fetched", now, ttl, true},
		{"partly aged", now.Add(-10 * time.Minute), 20 * time.Minute, true},
		{"exactly at the boundary", now.Add(-ttl), 0, false},
		{"beyond the boundary", now.Add(-ttl - time.Second), 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RemainingTTL(tc.fetchedAt, now, ttl); got != tc.wantTTL {
				t.Errorf("RemainingTTL = %v, want %v", got, tc.wantTTL)
			}
			if got := Fresh(tc.fetchedAt, now, ttl); got != tc.wantFresh {
				t.Errorf("Fresh = %v, want %v", got, tc.wantFresh)
			}
		})
	}
}

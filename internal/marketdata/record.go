package marketdata

import (
	"strings"
	"time"
)

// Key identifies a token's market record independently of any wallet.
type Key struct {
	Chain    string
	TokenKey string
}

// ContractKey normalizes a chain and platform contract address into a key.
func ContractKey(chain, address string) Key {
	return Key{
		Chain:    strings.ToLower(strings.TrimSpace(chain)),
		TokenKey: normalizePlatformAddress(address),
	}
}

// NativeKey normalizes a chain and native-token symbol into a key.
func NativeKey(chain, symbol string) Key {
	return Key{
		Chain:    strings.ToLower(strings.TrimSpace(chain)),
		TokenKey: "native:" + strings.ToUpper(strings.TrimSpace(symbol)),
	}
}

// Record is the shared CoinGecko market data stored by persistence layers.
type Record struct {
	Key
	CoinGeckoID         string     `json:"coingeckoID"`
	PriceUSD            *string    `json:"priceUSD"`
	Change24HPercent    *float64   `json:"change24hPercent"`
	MarketCapUSD        *float64   `json:"marketCapUSD"`
	MarketDataUpdatedAt *time.Time `json:"marketDataUpdatedAt"`
	FetchedAt           time.Time  `json:"fetchedAt"`
}

// CacheWrite pairs a market record with its caller-selected Redis TTL.
type CacheWrite struct {
	Record Record
	TTL    time.Duration
}

// RemainingTTL returns the positive portion of ttl left from now, or zero at
// and beyond the freshness boundary.
func (r Record) RemainingTTL(now time.Time, ttl time.Duration) time.Duration {
	remaining := ttl - now.Sub(r.FetchedAt)
	if remaining <= 0 {
		return 0
	}
	return remaining
}

// Fresh reports whether the record is strictly within the configured TTL.
func (r Record) Fresh(now time.Time, ttl time.Duration) bool {
	return r.RemainingTTL(now, ttl) > 0
}

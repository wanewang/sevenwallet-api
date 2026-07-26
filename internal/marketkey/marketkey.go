// Package marketkey holds the market-record vocabulary shared by every
// provider: how a token is keyed, and how a record's freshness is measured.
// It depends on no provider package so that provider packages need not depend
// on each other.
package marketkey

import (
	"strings"
	"time"
)

// Key identifies a token's market record independently of any wallet.
type Key struct {
	Chain    string `json:"chain"`
	TokenKey string `json:"tokenKey"`
}

// ContractKey normalizes a chain and platform contract address into a key.
func ContractKey(chain, address string) Key {
	return Key{
		Chain:    strings.ToLower(strings.TrimSpace(chain)),
		TokenKey: NormalizePlatformAddress(address),
	}
}

// NativeKey normalizes a chain and native-token symbol into a key.
func NativeKey(chain, symbol string) Key {
	return Key{
		Chain:    strings.ToLower(strings.TrimSpace(chain)),
		TokenKey: "native:" + strings.ToUpper(strings.TrimSpace(symbol)),
	}
}

// NormalizePlatformAddress lowercases hex addresses and leaves other token
// identifiers as-is. Cache and database keys derive from its output, so any
// change here strands every stored record.
func NormalizePlatformAddress(address string) string {
	address = strings.TrimSpace(address)
	if strings.HasPrefix(strings.ToLower(address), "0x") {
		return strings.ToLower(address)
	}
	return address
}

// RemainingTTL returns the positive portion of ttl left from now, or zero at
// and beyond the freshness boundary. Provider record types keep their own
// FetchedAt field and delegate here, so the arithmetic exists once.
func RemainingTTL(fetchedAt, now time.Time, ttl time.Duration) time.Duration {
	remaining := ttl - now.Sub(fetchedAt)
	if remaining <= 0 {
		return 0
	}
	return remaining
}

// Fresh reports whether a record fetched at fetchedAt is strictly within ttl.
func Fresh(fetchedAt, now time.Time, ttl time.Duration) bool {
	return RemainingTTL(fetchedAt, now, ttl) > 0
}

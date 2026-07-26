package cmcmarket

import (
	"time"

	"wallet-api/internal/marketdata"
)

// Record is one shared CoinMarketCap market record.
type Record struct {
	marketdata.Key
	CoinMarketCapID int64     `json:"coinMarketCapID"`
	PriceUSD        *string   `json:"priceUSD"`
	Change24H       *float64  `json:"change24hPercent"`
	FetchedAt       time.Time `json:"fetchedAt"`
}

// CacheWrite applies a caller-selected remaining Redis TTL.
type CacheWrite struct {
	Record Record
	TTL    time.Duration
}

func (r Record) RemainingTTL(now time.Time, ttl time.Duration) time.Duration {
	remaining := ttl - now.Sub(r.FetchedAt)
	if remaining <= 0 {
		return 0
	}
	return remaining
}

func (r Record) Fresh(now time.Time, ttl time.Duration) bool {
	return r.RemainingTTL(now, ttl) > 0
}

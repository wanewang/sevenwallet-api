package cmcmarket

import (
	"time"

	"wallet-api/internal/marketkey"
)

// Record is one shared CoinMarketCap market record.
type Record struct {
	marketkey.Key
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
	return marketkey.RemainingTTL(r.FetchedAt, now, ttl)
}

func (r Record) Fresh(now time.Time, ttl time.Duration) bool {
	return marketkey.Fresh(r.FetchedAt, now, ttl)
}

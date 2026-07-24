package coingecko

import (
	"encoding/json"
	"fmt"
)

type Coin struct {
	ID        string            `json:"id"`
	Symbol    string            `json:"symbol"`
	Name      string            `json:"name"`
	Platforms map[string]string `json:"platforms"`
}

type SimplePrice struct {
	USD           *json.Number `json:"usd"`
	USDMarketCap  *json.Number `json:"usd_market_cap"`
	USD24HChange  *json.Number `json:"usd_24h_change"`
	LastUpdatedAt *int64       `json:"last_updated_at"`
}

type StatusError struct{ StatusCode int }

func (e *StatusError) Error() string {
	return fmt.Sprintf("coingecko returned status %d", e.StatusCode)
}

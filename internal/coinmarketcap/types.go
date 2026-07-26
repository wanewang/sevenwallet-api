package coinmarketcap

import (
	"encoding/json"
	"fmt"
)

// Platform identifies the chain and contract for a CoinMarketCap token.
type Platform struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Symbol       string `json:"symbol"`
	Slug         string `json:"slug"`
	TokenAddress string `json:"token_address"`
}

// Coin is one cryptocurrency-map entry.
type Coin struct {
	ID       int64     `json:"id"`
	Name     string    `json:"name"`
	Symbol   string    `json:"symbol"`
	Slug     string    `json:"slug"`
	Rank     *int64    `json:"rank"`
	IsActive int       `json:"is_active"`
	Platform *Platform `json:"platform"`
}

// SimplePrice contains the market fields requested from /v1/simple/price.
type SimplePrice struct {
	ID               int64        `json:"id"`
	Price            *json.Number `json:"price"`
	PercentChange24H *json.Number `json:"percent_change_24h"`
}

type apiStatus struct {
	ErrorCode    json.RawMessage `json:"error_code"`
	ErrorMessage *string         `json:"error_message"`
}

// StatusError reports an HTTP or CoinMarketCap envelope failure.
type StatusError struct {
	StatusCode int
	ErrorCode  int64
	Message    string
}

func (e *StatusError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("coinmarketcap returned status %d", e.StatusCode)
	}
	if e.Message != "" {
		return fmt.Sprintf("coinmarketcap returned error %d: %s", e.ErrorCode, e.Message)
	}
	return fmt.Sprintf("coinmarketcap returned error %d", e.ErrorCode)
}

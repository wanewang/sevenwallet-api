// Package lifi is a thin client for the LI.FI token-list API.
package lifi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ListToken is one entry from the LI.FI token list.
type ListToken struct {
	Address  string `json:"address"`
	Symbol   string `json:"symbol"`
	Name     string `json:"name"`
	Decimals int    `json:"decimals"`
	CoinKey  string `json:"coinKey"`
	LogoURI  string `json:"logoURI"`
	PriceUSD string `json:"priceUSD"`
}

// Client calls the LI.FI tokens endpoint.
type Client struct {
	tokensURL  string
	httpClient *http.Client
}

// New builds a Client for the given tokens URL (e.g. https://li.quest/v1/tokens).
func New(tokensURL string) *Client {
	return &Client{tokensURL: tokensURL, httpClient: &http.Client{Timeout: 15 * time.Second}}
}

// chainIDs maps a LI.FI chain key to the numeric chain id used as the response map key.
var chainIDs = map[string]string{"ETH": "1"}

type tokensResponse struct {
	Tokens map[string][]ListToken `json:"tokens"`
}

// GetTokens fetches the token list for the given chain (e.g. "ETH").
func (c *Client) GetTokens(ctx context.Context, chain string) ([]ListToken, error) {
	u := fmt.Sprintf("%s?chain=%s", c.tokensURL, url.QueryEscape(chain))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("lifi request failed: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil, fmt.Errorf("lifi returned status %d", res.StatusCode)
	}
	var resp tokensResponse
	if err := json.NewDecoder(res.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode lifi response: %w", err)
	}
	// Prefer the mapped chain id; fall back to flattening all returned chains.
	if id, ok := chainIDs[strings.ToUpper(chain)]; ok {
		if tokens := resp.Tokens[id]; len(tokens) > 0 {
			return tokens, nil
		}
	}
	var all []ListToken
	for _, v := range resp.Tokens {
		all = append(all, v...)
	}
	return all, nil
}

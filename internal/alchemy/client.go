package alchemy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client calls Alchemy's HTTP APIs.
type Client struct {
	apiKey     string
	network    string
	httpClient *http.Client
	tokensURL  string // Portfolio API: tokens-by-address
	rpcURL     string // JSON-RPC endpoint (getAssetTransfers)
}

// New builds a Client with production Alchemy URLs.
func New(apiKey, network string) *Client {
	return &Client{
		apiKey:     apiKey,
		network:    network,
		httpClient: &http.Client{Timeout: 15 * time.Second},
		tokensURL:  fmt.Sprintf("https://api.g.alchemy.com/data/v1/%s/assets/tokens/by-address", apiKey),
		rpcURL:     fmt.Sprintf("https://%s.g.alchemy.com/v2/%s", network, apiKey),
	}
}

type tokensRequest struct {
	Addresses           []addressNetworks `json:"addresses"`
	WithMetadata        bool              `json:"withMetadata"`
	WithPrices          bool              `json:"withPrices"`
	IncludeNativeTokens bool              `json:"includeNativeTokens"`
	IncludeErc20Tokens  bool              `json:"includeErc20Tokens"`
}

type addressNetworks struct {
	Address  string   `json:"address"`
	Networks []string `json:"networks"`
}

type tokensResponse struct {
	Data struct {
		Tokens []struct {
			TokenAddress  *string `json:"tokenAddress"`
			TokenBalance  string  `json:"tokenBalance"`
			TokenMetadata struct {
				Decimals int    `json:"decimals"`
				Name     string `json:"name"`
				Symbol   string `json:"symbol"`
			} `json:"tokenMetadata"`
			TokenPrices []struct {
				Currency      string `json:"currency"`
				Value         string `json:"value"`
				LastUpdatedAt string `json:"lastUpdatedAt"`
			} `json:"tokenPrices"`
			Error *string `json:"error"`
		} `json:"tokens"`
	} `json:"data"`
}

// GetTokens fetches native + ERC-20 holdings (with metadata and prices) for one address.
func (c *Client) GetTokens(ctx context.Context, address, network string) ([]Token, error) {
	reqBody := tokensRequest{
		Addresses:           []addressNetworks{{Address: address, Networks: []string{network}}},
		WithMetadata:        true,
		WithPrices:          true,
		IncludeNativeTokens: true,
		IncludeErc20Tokens:  true,
	}
	var resp tokensResponse
	if err := c.postJSON(ctx, c.tokensURL, reqBody, &resp); err != nil {
		return nil, err
	}
	tokens := make([]Token, 0, len(resp.Data.Tokens))
	for _, raw := range resp.Data.Tokens {
		if raw.Error != nil && *raw.Error != "" {
			continue // skip tokens the upstream could not resolve
		}
		t := Token{
			TokenAddress: raw.TokenAddress,
			Symbol:       raw.TokenMetadata.Symbol,
			Name:         raw.TokenMetadata.Name,
			Decimals:     raw.TokenMetadata.Decimals,
			RawBalance:   raw.TokenBalance,
		}
		if len(raw.TokenPrices) > 0 {
			p := raw.TokenPrices[0]
			t.Price = &Price{Currency: p.Currency, Value: p.Value, LastUpdatedAt: p.LastUpdatedAt}
		}
		tokens = append(tokens, t)
	}
	return tokens, nil
}

// postJSON marshals body, POSTs it to url, and decodes the response into out.
func (c *Client) postJSON(ctx context.Context, url string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("alchemy request failed: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		// Drain the body so the connection can be reused by the transport.
		_, _ = io.Copy(io.Discard, res.Body)
		return fmt.Errorf("alchemy returned status %d", res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("decode alchemy response: %w", err)
	}
	return nil
}

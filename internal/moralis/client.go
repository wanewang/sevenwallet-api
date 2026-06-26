// Package moralis is a thin client for the Moralis EVM token-metadata API.
package moralis

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Metadata is the subset of Moralis token metadata this service uses.
type Metadata struct {
	Symbol           string
	Name             string
	Logo             string
	Decimals         int
	PossibleSpam     bool
	VerifiedContract bool
}

// Client calls the Moralis ERC-20 metadata endpoint.
type Client struct {
	apiKey     string
	chain      string
	baseURL    string
	httpClient *http.Client
}

// New builds a Client for the given API key and chain (e.g. "eth").
func New(apiKey, chain string) *Client {
	return &Client{
		apiKey:     apiKey,
		chain:      chain,
		baseURL:    "https://deep-index.moralis.io/api/v2.2",
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

type rawMetadata struct {
	Symbol           string `json:"symbol"`
	Name             string `json:"name"`
	Logo             string `json:"logo"`
	Decimals         string `json:"decimals"`
	PossibleSpam     bool   `json:"possible_spam"`
	VerifiedContract bool   `json:"verified_contract"`
}

// GetTokenMetadata fetches metadata for a single ERC-20 contract.
func (c *Client) GetTokenMetadata(ctx context.Context, address string) (Metadata, error) {
	base, err := url.Parse(c.baseURL + "/erc20/metadata")
	if err != nil {
		return Metadata{}, fmt.Errorf("parse moralis url: %w", err)
	}
	q := base.Query()
	q.Set("chain", c.chain)
	q.Set("addresses[]", address)
	base.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return Metadata{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-API-Key", c.apiKey)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return Metadata{}, fmt.Errorf("moralis request failed: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, res.Body)
		return Metadata{}, fmt.Errorf("moralis returned status %d", res.StatusCode)
	}

	var raw []rawMetadata
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		return Metadata{}, fmt.Errorf("decode moralis response: %w", err)
	}
	if len(raw) == 0 {
		return Metadata{}, fmt.Errorf("moralis returned no metadata for %s", address)
	}
	r := raw[0]
	decimals, _ := strconv.Atoi(r.Decimals) // invalid/empty → 0
	return Metadata{
		Symbol:           r.Symbol,
		Name:             r.Name,
		Logo:             r.Logo,
		Decimals:         decimals,
		PossibleSpam:     r.PossibleSpam,
		VerifiedContract: r.VerifiedContract,
	}, nil
}

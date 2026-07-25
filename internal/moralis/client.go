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

	"wallet-api/internal/providerlog"
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
	logf       func(string, ...any)
}

// Option configures a Moralis Client.
type Option func(*Client)

// WithLogf enables provider diagnostics using logf. A nil hook is a no-op.
func WithLogf(logf func(string, ...any)) Option {
	return func(c *Client) { c.logf = logf }
}

// New builds a Client for the given API key and chain (e.g. "eth").
func New(apiKey, chain string, opts ...Option) *Client {
	c := &Client{
		apiKey:     apiKey,
		chain:      chain,
		baseURL:    "https://deep-index.moralis.io/api/v2.2",
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

func (c *Client) diagnosticf(format string, args ...any) {
	if c != nil && c.logf != nil {
		c.logf(format, args...)
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
		wrapped := fmt.Errorf("parse moralis url: %w", err)
		c.diagnosticError("request", wrapped)
		return Metadata{}, wrapped
	}
	q := base.Query()
	q.Set("chain", c.chain)
	q.Set("addresses[]", address)
	base.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		c.diagnosticError("request", err)
		return Metadata{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-API-Key", c.apiKey)

	c.diagnosticf("provider_api provider=moralis operation=get_token_metadata connecting method=%s chain=%q address=%q", http.MethodGet, c.chain, address)
	res, err := c.httpClient.Do(req)
	if err != nil {
		wrapped := fmt.Errorf("moralis request failed: %w", err)
		c.diagnosticError("transport", wrapped)
		return Metadata{}, wrapped
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, res.Body)
		c.diagnosticf("provider_api provider=moralis operation=get_token_metadata failure status=%d", res.StatusCode)
		return Metadata{}, fmt.Errorf("moralis returned status %d", res.StatusCode)
	}

	var raw []rawMetadata
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		wrapped := fmt.Errorf("decode moralis response: %w", err)
		c.diagnosticError("decode", wrapped)
		return Metadata{}, wrapped
	}
	if len(raw) == 0 {
		err := fmt.Errorf("moralis returned no metadata for %s", address)
		c.diagnosticError("result", err)
		return Metadata{}, err
	}
	r := raw[0]
	decimals, _ := strconv.Atoi(r.Decimals) // invalid/empty → 0
	metadata := Metadata{
		Symbol:           r.Symbol,
		Name:             r.Name,
		Logo:             r.Logo,
		Decimals:         decimals,
		PossibleSpam:     r.PossibleSpam,
		VerifiedContract: r.VerifiedContract,
	}
	c.diagnosticf("provider_api provider=moralis operation=get_token_metadata result=%s", providerlog.JSON(metadata, c.apiKey))
	return metadata, nil
}

func (c *Client) diagnosticError(stage string, err error) {
	c.diagnosticf(
		"provider_api provider=moralis operation=get_token_metadata failure stage=%s error=%q",
		stage,
		providerlog.Redact(err.Error(), c.apiKey),
	)
}

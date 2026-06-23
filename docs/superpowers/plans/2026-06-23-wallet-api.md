# Wallet API Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a read-only, non-custodial Ethereum wallet API in Go exposing a token portfolio endpoint (native ETH + ERC-20 with prices) and a transaction history endpoint, backed by Alchemy and cached in Postgres.

**Architecture:** Layered Go service — `api → wallet → {alchemy, store}`. The `alchemy` package wraps Alchemy HTTP APIs, `store` provides Postgres persistence (a structured `wallet_tokens` snapshot table plus a JSON `tx_cache`), and the `wallet` service orchestrates cache-first reads and normalizes data into domain types. Standard-library `net/http` serves the endpoints.

**Tech Stack:** Go 1.22+, standard-library `net/http`, `github.com/jackc/pgx/v5` (Postgres), Postgres 16 via docker-compose. No web framework, no router dependency.

## Global Constraints

- **Go version floor:** 1.22 (uses `net/http` method+pattern routing like `GET /v1/addresses/{address}/tokens`).
- **Module path:** `wallet-api`.
- **Read-only:** never handle private keys, sign, or broadcast.
- **EVM/Ethereum only.** Default network `eth-mainnet`.
- **Large numeric values are JSON strings** (raw balances, scaled balances) — never floats.
- **No live network calls in tests.** Alchemy is tested via `httptest`. Postgres tests are integration tests guarded by `WALLET_TEST_DATABASE_URL` and skipped when unset.
- **Addresses normalized to lowercase** before storage/lookup.
- **Config via env:** `ALCHEMY_API_KEY` (required), `DATABASE_URL` (required), `ALCHEMY_NETWORK` (default `eth-mainnet`), `CACHE_TTL_SECONDS` (default `300`), `PORT` (default `8080`).

### Implementation note — transactions pagination (prototype scope)

Alchemy `getAssetTransfers` matches `fromAddress` OR `toAddress` per call, not both. To cover both directions, the client makes two calls (outgoing + incoming), merges, dedupes by transfer identity, and sorts by block number descending. For this prototype the endpoint returns a single merged page of the most recent `limit` transfers; `nextPageKey` is always empty and the `?pageKey=` query param is accepted but reserved for future deep pagination. This matches the spec's note that multi-page history is intentionally not cached.

## File Structure

```
go.mod
docker-compose.yml                  → local Postgres 16
cmd/server/main.go                  → wire config + store + alchemy + service; start HTTP server
internal/config/config.go           → load + validate env config
internal/config/config_test.go
internal/wallet/types.go            → domain types (Token, TokenPortfolio, Transfer, TransactionPage, Price) + interfaces + sentinel errors
internal/wallet/normalize.go        → address normalization + balance scaling (big.Int)
internal/wallet/normalize_test.go
internal/wallet/service.go          → cache-first orchestration
internal/wallet/service_test.go     → tests against in-memory fakes
internal/wallet/fakes_test.go       → in-memory AlchemyClient/TokenStore/TxCache fakes
internal/alchemy/types.go           → raw Alchemy result structs
internal/alchemy/client.go          → GetTokens (Portfolio API) + GetTransfers (getAssetTransfers)
internal/alchemy/client_test.go     → httptest-backed tests
internal/store/schema.sql           → embedded DDL
internal/store/store.go             → pgx pool, Migrate, token + tx persistence
internal/store/store_test.go        → integration tests (skipped without WALLET_TEST_DATABASE_URL)
internal/api/router.go              → routes + WalletService interface
internal/api/handlers.go            → handlers, address validation, JSON/error helpers
internal/api/handlers_test.go       → tests against a fake WalletService
```

---

### Task 1: Module init + config package

**Files:**
- Create: `go.mod`
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `config.Config{ AlchemyAPIKey, AlchemyNetwork, DatabaseURL string; CacheTTL time.Duration; Port string }`, `config.Load() (Config, error)`.

- [ ] **Step 1: Initialize the module**

Run:
```bash
go mod init wallet-api
go mod edit -go=1.22
```

- [ ] **Step 2: Write the failing test**

Create `internal/config/config_test.go`:
```go
package config

import (
	"testing"
	"time"
)

func TestLoadFromAppliesDefaults(t *testing.T) {
	env := map[string]string{
		"ALCHEMY_API_KEY": "key123",
		"DATABASE_URL":    "postgres://localhost:5432/wallet",
	}
	cfg, err := loadFrom(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AlchemyNetwork != "eth-mainnet" {
		t.Errorf("network = %q, want eth-mainnet", cfg.AlchemyNetwork)
	}
	if cfg.CacheTTL != 300*time.Second {
		t.Errorf("ttl = %v, want 300s", cfg.CacheTTL)
	}
	if cfg.Port != "8080" {
		t.Errorf("port = %q, want 8080", cfg.Port)
	}
}

func TestLoadFromHonoursOverrides(t *testing.T) {
	env := map[string]string{
		"ALCHEMY_API_KEY":   "key123",
		"DATABASE_URL":      "postgres://db",
		"ALCHEMY_NETWORK":   "eth-sepolia",
		"CACHE_TTL_SECONDS": "30",
		"PORT":              "9000",
	}
	cfg, err := loadFrom(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AlchemyNetwork != "eth-sepolia" || cfg.CacheTTL != 30*time.Second || cfg.Port != "9000" {
		t.Errorf("overrides not applied: %+v", cfg)
	}
}

func TestLoadFromRequiresKeyAndDB(t *testing.T) {
	if _, err := loadFrom(func(string) string { return "" }); err == nil {
		t.Fatal("expected error when ALCHEMY_API_KEY/DATABASE_URL missing")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/config/`
Expected: FAIL — `undefined: loadFrom` / `undefined: Config`.

- [ ] **Step 4: Write minimal implementation**

Create `internal/config/config.go`:
```go
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds all runtime configuration, sourced from environment variables.
type Config struct {
	AlchemyAPIKey  string
	AlchemyNetwork string
	DatabaseURL    string
	CacheTTL       time.Duration
	Port           string
}

// Load reads configuration from the process environment.
func Load() (Config, error) {
	return loadFrom(os.Getenv)
}

// loadFrom reads configuration using the supplied getenv function (testable).
func loadFrom(getenv func(string) string) (Config, error) {
	cfg := Config{
		AlchemyAPIKey:  getenv("ALCHEMY_API_KEY"),
		AlchemyNetwork: getenv("ALCHEMY_NETWORK"),
		DatabaseURL:    getenv("DATABASE_URL"),
		Port:           getenv("PORT"),
	}
	if cfg.AlchemyAPIKey == "" {
		return Config{}, fmt.Errorf("ALCHEMY_API_KEY is required")
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.AlchemyNetwork == "" {
		cfg.AlchemyNetwork = "eth-mainnet"
	}
	if cfg.Port == "" {
		cfg.Port = "8080"
	}
	ttl := 300
	if raw := getenv("CACHE_TTL_SECONDS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("CACHE_TTL_SECONDS must be a positive integer, got %q", raw)
		}
		ttl = n
	}
	cfg.CacheTTL = time.Duration(ttl) * time.Second
	return cfg, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/config/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go.mod internal/config/
git commit -m "feat: add config package with env loading"
```

---

### Task 2: Balance normalization + address helper

**Files:**
- Create: `internal/wallet/normalize.go`
- Test: `internal/wallet/normalize_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `wallet.NormalizeAddress(s string) string` — trims + lowercases.
  - `wallet.ScaleBalance(raw string, decimals int) (rawDecimal string, scaled string, err error)` — parses a decimal or `0x`-hex integer string and returns its decimal form plus the value scaled by `10^decimals`, both as strings (no float loss).

- [ ] **Step 1: Write the failing test**

Create `internal/wallet/normalize_test.go`:
```go
package wallet

import "testing"

func TestNormalizeAddress(t *testing.T) {
	if got := NormalizeAddress("  0xAbC123  "); got != "0xabc123" {
		t.Errorf("got %q, want 0xabc123", got)
	}
}

func TestScaleBalance(t *testing.T) {
	cases := []struct {
		raw     string
		dec     int
		rawDec  string
		scaled  string
	}{
		{"0x16345785d8a0000", 18, "100000000000000000", "0.1"},
		{"1500000000000000000", 18, "1500000000000000000", "1.5"},
		{"12500000", 6, "12500000", "12.5"},
		{"1000000", 6, "1000000", "1"},
		{"0x0", 18, "0", "0"},
		{"", 18, "0", "0"},
	}
	for _, c := range cases {
		rawDec, scaled, err := ScaleBalance(c.raw, c.dec)
		if err != nil {
			t.Fatalf("ScaleBalance(%q,%d) error: %v", c.raw, c.dec, err)
		}
		if rawDec != c.rawDec || scaled != c.scaled {
			t.Errorf("ScaleBalance(%q,%d) = (%q,%q), want (%q,%q)", c.raw, c.dec, rawDec, scaled, c.rawDec, c.scaled)
		}
	}
}

func TestScaleBalanceInvalid(t *testing.T) {
	if _, _, err := ScaleBalance("not-a-number", 18); err == nil {
		t.Fatal("expected error for invalid input")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/wallet/ -run TestScaleBalance`
Expected: FAIL — `undefined: ScaleBalance` / `undefined: NormalizeAddress`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/wallet/normalize.go`:
```go
package wallet

import (
	"fmt"
	"math/big"
	"strings"
)

// NormalizeAddress trims whitespace and lowercases an address for storage/lookup.
func NormalizeAddress(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// ScaleBalance parses raw (a decimal or 0x-hex unsigned integer string) and
// returns its decimal-string form and the value divided by 10^decimals as a
// decimal string. Both results avoid floating point to preserve precision.
func ScaleBalance(raw string, decimals int) (string, string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		s = "0"
	}
	// base 0 auto-detects the 0x prefix; otherwise parses base 10.
	n, ok := new(big.Int).SetString(s, 0)
	if !ok {
		return "", "", fmt.Errorf("invalid balance %q", raw)
	}
	rawDecimal := n.String()
	if decimals <= 0 {
		return rawDecimal, rawDecimal, nil
	}
	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	q := new(big.Int)
	r := new(big.Int)
	q.DivMod(n, divisor, r)
	if r.Sign() == 0 {
		return rawDecimal, q.String(), nil
	}
	// Left-pad the remainder to `decimals` digits, then trim trailing zeros.
	frac := fmt.Sprintf("%0*s", decimals, r.String())
	frac = strings.TrimRight(frac, "0")
	return rawDecimal, q.String() + "." + frac, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/wallet/ -run "TestScaleBalance|TestNormalizeAddress"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/wallet/normalize.go internal/wallet/normalize_test.go
git commit -m "feat: add address + balance normalization helpers"
```

---

### Task 3: Alchemy client — token portfolio

**Files:**
- Create: `internal/alchemy/types.go`
- Create: `internal/alchemy/client.go`
- Test: `internal/alchemy/client_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `alchemy.Price{ Currency, Value, LastUpdatedAt string }`
  - `alchemy.Token{ TokenAddress *string; Symbol, Name string; Decimals int; RawBalance string; Price *Price }`
  - `alchemy.Client` with `alchemy.New(apiKey, network string) *Client`
  - `(*Client).GetTokens(ctx context.Context, address, network string) ([]Token, error)`

- [ ] **Step 1: Write the failing test**

Create `internal/alchemy/client_test.go`:
```go
package alchemy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetTokensParsesPortfolioResponse(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_, _ = io.WriteString(w, `{
		  "data": {
		    "tokens": [
		      {"address":"0xabc","network":"eth-mainnet","tokenAddress":null,
		       "tokenBalance":"1500000000000000000",
		       "tokenMetadata":{"decimals":18,"name":"Ethereum","symbol":"ETH"},
		       "tokenPrices":[{"currency":"usd","value":"3200.50","lastUpdatedAt":"2026-06-23T00:00:00Z"}],
		       "error":null},
		      {"address":"0xabc","network":"eth-mainnet","tokenAddress":"0xA0b8...",
		       "tokenBalance":"12500000",
		       "tokenMetadata":{"decimals":6,"name":"USD Coin","symbol":"USDC"},
		       "tokenPrices":[],"error":null}
		    ],
		    "pageKey": null
		  }
		}`)
	}))
	defer srv.Close()

	c := New("key123", "eth-mainnet")
	c.tokensURL = srv.URL

	tokens, err := c.GetTokens(context.Background(), "0xABC", "eth-mainnet")
	if err != nil {
		t.Fatalf("GetTokens error: %v", err)
	}
	// Request body assertions.
	if gotBody["withMetadata"] != true || gotBody["withPrices"] != true ||
		gotBody["includeNativeTokens"] != true || gotBody["includeErc20Tokens"] != true {
		t.Errorf("request flags wrong: %v", gotBody)
	}
	if len(tokens) != 2 {
		t.Fatalf("got %d tokens, want 2", len(tokens))
	}
	if tokens[0].TokenAddress != nil {
		t.Errorf("native token should have nil TokenAddress")
	}
	if tokens[0].Symbol != "ETH" || tokens[0].Decimals != 18 || tokens[0].RawBalance != "1500000000000000000" {
		t.Errorf("native token mis-parsed: %+v", tokens[0])
	}
	if tokens[0].Price == nil || tokens[0].Price.Value != "3200.50" {
		t.Errorf("native price mis-parsed: %+v", tokens[0].Price)
	}
	if tokens[1].TokenAddress == nil || tokens[1].Symbol != "USDC" {
		t.Errorf("erc20 mis-parsed: %+v", tokens[1])
	}
	if tokens[1].Price != nil {
		t.Errorf("empty tokenPrices should yield nil Price, got %+v", tokens[1].Price)
	}
}

func TestGetTokensSendsAddressAndNetwork(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"eth-mainnet"`) {
			t.Errorf("network not in body: %s", body)
		}
		_, _ = io.WriteString(w, `{"data":{"tokens":[],"pageKey":null}}`)
	}))
	defer srv.Close()
	c := New("key123", "eth-mainnet")
	c.tokensURL = srv.URL
	if _, err := c.GetTokens(context.Background(), "0xABC", "eth-mainnet"); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/alchemy/ -run TestGetTokens`
Expected: FAIL — `undefined: New` / `undefined: Client`.

- [ ] **Step 3: Write the types**

Create `internal/alchemy/types.go`:
```go
package alchemy

// Price is a single currency price for a token.
type Price struct {
	Currency      string
	Value         string
	LastUpdatedAt string
}

// Token is a raw token holding returned by the Portfolio API.
// TokenAddress is nil for the chain native token (e.g. ETH).
type Token struct {
	TokenAddress *string
	Symbol       string
	Name         string
	Decimals     int
	RawBalance   string
	Price        *Price
}

// Transfer is a single asset transfer returned by getAssetTransfers.
type Transfer struct {
	Hash     string
	From     string
	To       string
	Asset    string
	Value    string
	BlockNum string
	Category string
}

// TransfersResult is a page of transfers.
type TransfersResult struct {
	Transfers []Transfer
	PageKey   string
}
```

- [ ] **Step 4: Write the client (token portfolio)**

Create `internal/alchemy/client.go`:
```go
package alchemy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
		return fmt.Errorf("alchemy returned status %d", res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("decode alchemy response: %w", err)
	}
	return nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/alchemy/ -run TestGetTokens`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/alchemy/
git commit -m "feat: add alchemy client GetTokens (portfolio API)"
```

---

### Task 4: Alchemy client — asset transfers

**Files:**
- Modify: `internal/alchemy/client.go` (add `GetTransfers` + helpers)
- Test: `internal/alchemy/client_test.go` (add cases)

**Interfaces:**
- Consumes: `alchemy.Client`, `alchemy.Transfer`, `alchemy.TransfersResult` (Task 3).
- Produces: `(*Client).GetTransfers(ctx context.Context, address string, limit int, pageKey string) (TransfersResult, error)` — two calls (outgoing + incoming), merged, deduped, sorted by block number desc, truncated to `limit`. `PageKey` is always `""` in this prototype.

- [ ] **Step 1: Write the failing test**

Append to `internal/alchemy/client_test.go`:
```go
func TestGetTransfersMergesBothDirections(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// Respond differently for the outgoing (fromAddress) vs incoming (toAddress) call.
		if strings.Contains(string(body), `"fromAddress"`) {
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"transfers":[
			  {"hash":"0xout","from":"0xabc","to":"0xdef","asset":"ETH","value":0.5,"blockNum":"0x20","category":"external"}
			],"pageKey":null}}`)
			return
		}
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"transfers":[
		  {"hash":"0xin","from":"0x111","to":"0xabc","asset":"USDC","value":10,"blockNum":"0x30","category":"erc20"}
		],"pageKey":null}}`)
	}))
	defer srv.Close()

	c := New("key123", "eth-mainnet")
	c.rpcURL = srv.URL

	res, err := c.GetTransfers(context.Background(), "0xabc", 25, "")
	if err != nil {
		t.Fatalf("GetTransfers error: %v", err)
	}
	if len(res.Transfers) != 2 {
		t.Fatalf("got %d transfers, want 2", len(res.Transfers))
	}
	// Sorted by block number descending: 0x30 (incoming) before 0x20 (outgoing).
	if res.Transfers[0].Hash != "0xin" || res.Transfers[1].Hash != "0xout" {
		t.Errorf("wrong order: %+v", res.Transfers)
	}
	if res.Transfers[0].Value != "10" {
		t.Errorf("value formatting wrong: %q", res.Transfers[0].Value)
	}
}

func TestGetTransfersTruncatesToLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"transfers":[
		  {"hash":"0xa","from":"0xabc","to":"0x1","asset":"ETH","value":1,"blockNum":"0x10","category":"external"},
		  {"hash":"0xb","from":"0xabc","to":"0x2","asset":"ETH","value":2,"blockNum":"0x11","category":"external"}
		],"pageKey":null}}`)
	}))
	defer srv.Close()
	c := New("key123", "eth-mainnet")
	c.rpcURL = srv.URL
	res, err := c.GetTransfers(context.Background(), "0xabc", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Transfers) != 1 {
		t.Fatalf("got %d, want 1 (limit)", len(res.Transfers))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/alchemy/ -run TestGetTransfers`
Expected: FAIL — `undefined: (*Client).GetTransfers`.

- [ ] **Step 3: Add the implementation**

Append to `internal/alchemy/client.go`:
```go
import (
	"sort"
	"strconv"
)
// NOTE: merge these imports into the existing import block; do not add a second block.

type rpcRequest struct {
	ID      int           `json:"id"`
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []any         `json:"params"`
}

type transfersResponse struct {
	Result struct {
		Transfers []struct {
			Hash     string   `json:"hash"`
			From     string   `json:"from"`
			To       string   `json:"to"`
			Asset    string   `json:"asset"`
			Value    *float64 `json:"value"`
			BlockNum string   `json:"blockNum"`
			Category string   `json:"category"`
		} `json:"transfers"`
		PageKey string `json:"pageKey"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// GetTransfers returns the most recent `limit` transfers (both directions) for address.
func (c *Client) GetTransfers(ctx context.Context, address string, limit int, pageKey string) (TransfersResult, error) {
	outgoing, err := c.fetchTransfers(ctx, "fromAddress", address, limit)
	if err != nil {
		return TransfersResult{}, err
	}
	incoming, err := c.fetchTransfers(ctx, "toAddress", address, limit)
	if err != nil {
		return TransfersResult{}, err
	}
	merged := dedupeTransfers(append(outgoing, incoming...))
	sort.SliceStable(merged, func(i, j int) bool {
		return blockNumValue(merged[i].BlockNum) > blockNumValue(merged[j].BlockNum)
	})
	if limit > 0 && len(merged) > limit {
		merged = merged[:limit]
	}
	return TransfersResult{Transfers: merged}, nil
}

func (c *Client) fetchTransfers(ctx context.Context, direction, address string, limit int) ([]Transfer, error) {
	params := map[string]any{
		"fromBlock":    "0x0",
		"toBlock":      "latest",
		"category":     []string{"external", "erc20"},
		"withMetadata": false,
		"order":        "desc",
		"maxCount":     fmt.Sprintf("0x%x", limit),
		direction:      address,
	}
	body := rpcRequest{ID: 1, JSONRPC: "2.0", Method: "alchemy_getAssetTransfers", Params: []any{params}}
	var resp transfersResponse
	if err := c.postJSON(ctx, c.rpcURL, body, &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("alchemy rpc error: %s", resp.Error.Message)
	}
	out := make([]Transfer, 0, len(resp.Result.Transfers))
	for _, t := range resp.Result.Transfers {
		value := ""
		if t.Value != nil {
			value = strconv.FormatFloat(*t.Value, 'f', -1, 64)
		}
		out = append(out, Transfer{
			Hash: t.Hash, From: t.From, To: t.To, Asset: t.Asset,
			Value: value, BlockNum: t.BlockNum, Category: t.Category,
		})
	}
	return out, nil
}

func dedupeTransfers(in []Transfer) []Transfer {
	seen := make(map[string]struct{}, len(in))
	out := make([]Transfer, 0, len(in))
	for _, t := range in {
		key := t.Hash + "|" + t.From + "|" + t.To + "|" + t.Asset + "|" + t.Value
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, t)
	}
	return out
}

// blockNumValue parses a 0x-hex block number; unparseable values sort last.
func blockNumValue(s string) uint64 {
	n, err := strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 64)
	if err != nil {
		return 0
	}
	return n
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/alchemy/`
Expected: PASS (all alchemy tests).

- [ ] **Step 5: Commit**

```bash
git add internal/alchemy/
git commit -m "feat: add alchemy GetTransfers with bidirectional merge"
```

---

### Task 5: Domain types, interfaces, and in-memory fakes

**Files:**
- Create: `internal/wallet/types.go`
- Create: `internal/wallet/fakes_test.go`

**Interfaces:**
- Consumes: `alchemy.Token`, `alchemy.TransfersResult` (Tasks 3-4).
- Produces (domain types + ports the service depends on):
  - `wallet.Price{ Currency, Value, LastUpdatedAt string }`
  - `wallet.Token{ TokenAddress *string; Symbol, Name string; Decimals int; RawBalance, Balance string; IsNative bool; Price *Price }`
  - `wallet.TokenPortfolio{ Address, Network string; FetchedAt time.Time; Tokens []Token }`
  - `wallet.Transfer{ Hash, From, To, Asset, Value, BlockNum, Category string }`
  - `wallet.TransactionPage{ Address string; Transfers []Transfer; NextPageKey string }`
  - `wallet.AlchemyClient` interface: `GetTokens(ctx, address, network string) ([]alchemy.Token, error)`, `GetTransfers(ctx, address string, limit int, pageKey string) (alchemy.TransfersResult, error)`
  - `wallet.TokenStore` interface: `GetFreshTokens(ctx, address, network string, ttl time.Duration) (*TokenPortfolio, bool, error)`, `SaveTokens(ctx, p *TokenPortfolio) error`
  - `wallet.TxCache` interface: `GetFreshTransactions(ctx, address, params string, ttl time.Duration) (*TransactionPage, bool, error)`, `SaveTransactions(ctx, address, params string, page *TransactionPage) error`
  - Sentinel errors `wallet.ErrUpstream`, `wallet.ErrStore`.

- [ ] **Step 1: Write the types and interfaces**

Create `internal/wallet/types.go`:
```go
package wallet

import (
	"context"
	"errors"
	"time"

	"wallet-api/internal/alchemy"
)

// Sentinel errors let the API layer map failures to HTTP status codes.
var (
	ErrUpstream = errors.New("upstream provider error")
	ErrStore    = errors.New("storage error")
)

// Price is a single currency price for a token.
type Price struct {
	Currency      string `json:"currency"`
	Value         string `json:"value"`
	LastUpdatedAt string `json:"lastUpdatedAt"`
}

// Token is a normalized token holding returned to API clients.
type Token struct {
	TokenAddress *string `json:"tokenAddress"`
	Symbol       string  `json:"symbol"`
	Name         string  `json:"name"`
	Decimals     int     `json:"decimals"`
	RawBalance   string  `json:"rawBalance"`
	Balance      string  `json:"balance"`
	IsNative     bool    `json:"isNative"`
	Price        *Price  `json:"price"`
}

// TokenPortfolio is the current token snapshot for an address.
type TokenPortfolio struct {
	Address   string    `json:"address"`
	Network   string    `json:"network"`
	FetchedAt time.Time `json:"fetchedAt"`
	Tokens    []Token   `json:"tokens"`
}

// Transfer is a single asset transfer returned to API clients.
type Transfer struct {
	Hash     string `json:"hash"`
	From     string `json:"from"`
	To       string `json:"to"`
	Asset    string `json:"asset"`
	Value    string `json:"value"`
	BlockNum string `json:"blockNum"`
	Category string `json:"category"`
}

// TransactionPage is a page of transfers for an address.
type TransactionPage struct {
	Address     string     `json:"address"`
	Transfers   []Transfer `json:"transfers"`
	NextPageKey string     `json:"nextPageKey,omitempty"`
}

// AlchemyClient is the subset of the Alchemy client the service depends on.
type AlchemyClient interface {
	GetTokens(ctx context.Context, address, network string) ([]alchemy.Token, error)
	GetTransfers(ctx context.Context, address string, limit int, pageKey string) (alchemy.TransfersResult, error)
}

// TokenStore persists the newest token snapshot per address.
type TokenStore interface {
	GetFreshTokens(ctx context.Context, address, network string, ttl time.Duration) (*TokenPortfolio, bool, error)
	SaveTokens(ctx context.Context, p *TokenPortfolio) error
}

// TxCache persists transaction-history pages as JSON.
type TxCache interface {
	GetFreshTransactions(ctx context.Context, address, params string, ttl time.Duration) (*TransactionPage, bool, error)
	SaveTransactions(ctx context.Context, address, params string, page *TransactionPage) error
}
```

- [ ] **Step 2: Write the in-memory fakes (test support)**

Create `internal/wallet/fakes_test.go`:
```go
package wallet

import (
	"context"
	"time"

	"wallet-api/internal/alchemy"
)

// fakeAlchemy returns canned data and records call counts.
type fakeAlchemy struct {
	tokens     []alchemy.Token
	transfers  alchemy.TransfersResult
	tokenCalls int
	txCalls    int
	err        error
}

func (f *fakeAlchemy) GetTokens(ctx context.Context, address, network string) ([]alchemy.Token, error) {
	f.tokenCalls++
	return f.tokens, f.err
}

func (f *fakeAlchemy) GetTransfers(ctx context.Context, address string, limit int, pageKey string) (alchemy.TransfersResult, error) {
	f.txCalls++
	return f.transfers, f.err
}

// fakeTokenStore is an in-memory TokenStore.
type fakeTokenStore struct {
	saved      *TokenPortfolio
	fresh      bool
	saveCalls  int
	getErr     error
	saveErr    error
}

func (s *fakeTokenStore) GetFreshTokens(ctx context.Context, address, network string, ttl time.Duration) (*TokenPortfolio, bool, error) {
	if s.getErr != nil {
		return nil, false, s.getErr
	}
	if s.fresh && s.saved != nil {
		return s.saved, true, nil
	}
	return nil, false, nil
}

func (s *fakeTokenStore) SaveTokens(ctx context.Context, p *TokenPortfolio) error {
	s.saveCalls++
	if s.saveErr != nil {
		return s.saveErr
	}
	s.saved = p
	return nil
}

// fakeTxCache is an in-memory TxCache.
type fakeTxCache struct {
	saved     *TransactionPage
	fresh     bool
	saveCalls int
}

func (c *fakeTxCache) GetFreshTransactions(ctx context.Context, address, params string, ttl time.Duration) (*TransactionPage, bool, error) {
	if c.fresh && c.saved != nil {
		return c.saved, true, nil
	}
	return nil, false, nil
}

func (c *fakeTxCache) SaveTransactions(ctx context.Context, address, params string, page *TransactionPage) error {
	c.saveCalls++
	c.saved = page
	return nil
}
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./internal/wallet/ && go vet ./internal/wallet/`
Expected: no output (success). Fakes are unused until Task 6 — that's fine; they live in a `_test.go` file.

- [ ] **Step 4: Commit**

```bash
git add internal/wallet/types.go internal/wallet/fakes_test.go
git commit -m "feat: add wallet domain types, ports, and test fakes"
```

---

### Task 6: Wallet service — GetTokens (cache-first)

**Files:**
- Create: `internal/wallet/service.go`
- Test: `internal/wallet/service_test.go`

**Interfaces:**
- Consumes: all `wallet` types/ports (Task 5), `ScaleBalance`/`NormalizeAddress` (Task 2).
- Produces:
  - `wallet.NewService(a AlchemyClient, ts TokenStore, tc TxCache, network string, ttl time.Duration) *Service`
  - `(*Service).GetTokens(ctx context.Context, address string) (*TokenPortfolio, error)`
  - The `Service` struct also carries an injectable `now func() time.Time` clock (defaults to `time.Now`).

- [ ] **Step 1: Write the failing test**

Create `internal/wallet/service_test.go`:
```go
package wallet

import (
	"context"
	"testing"
	"time"

	"wallet-api/internal/alchemy"
)

func usdc(addr string) *string { return &addr }

func TestGetTokensCacheMissFetchesAndSaves(t *testing.T) {
	fa := &fakeAlchemy{tokens: []alchemy.Token{
		{TokenAddress: nil, Symbol: "ETH", Name: "Ethereum", Decimals: 18, RawBalance: "1500000000000000000",
			Price: &alchemy.Price{Currency: "usd", Value: "3200.50", LastUpdatedAt: "2026-06-23T00:00:00Z"}},
		{TokenAddress: usdc("0xA0B8"), Symbol: "USDC", Name: "USD Coin", Decimals: 6, RawBalance: "12500000"},
	}}
	ts := &fakeTokenStore{}
	svc := NewService(fa, ts, &fakeTxCache{}, "eth-mainnet", time.Minute)

	p, err := svc.GetTokens(context.Background(), "0xABC")
	if err != nil {
		t.Fatalf("GetTokens error: %v", err)
	}
	if fa.tokenCalls != 1 || ts.saveCalls != 1 {
		t.Errorf("expected 1 fetch + 1 save, got fetch=%d save=%d", fa.tokenCalls, ts.saveCalls)
	}
	if p.Address != "0xabc" || p.Network != "eth-mainnet" {
		t.Errorf("portfolio header wrong: %+v", p)
	}
	if len(p.Tokens) != 2 {
		t.Fatalf("got %d tokens, want 2", len(p.Tokens))
	}
	if !p.Tokens[0].IsNative || p.Tokens[0].Balance != "1.5" || p.Tokens[0].RawBalance != "1500000000000000000" {
		t.Errorf("native token normalization wrong: %+v", p.Tokens[0])
	}
	if p.Tokens[1].IsNative || p.Tokens[1].Balance != "12.5" {
		t.Errorf("erc20 normalization wrong: %+v", p.Tokens[1])
	}
}

func TestGetTokensCacheHitSkipsAlchemy(t *testing.T) {
	cached := &TokenPortfolio{Address: "0xabc", Network: "eth-mainnet", Tokens: []Token{{Symbol: "ETH"}}}
	fa := &fakeAlchemy{}
	ts := &fakeTokenStore{saved: cached, fresh: true}
	svc := NewService(fa, ts, &fakeTxCache{}, "eth-mainnet", time.Minute)

	p, err := svc.GetTokens(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if fa.tokenCalls != 0 {
		t.Errorf("cache hit should not call alchemy, got %d calls", fa.tokenCalls)
	}
	if p != cached {
		t.Errorf("expected cached portfolio returned")
	}
}

func TestGetTokensWrapsUpstreamError(t *testing.T) {
	fa := &fakeAlchemy{err: context.DeadlineExceeded}
	svc := NewService(fa, &fakeTokenStore{}, &fakeTxCache{}, "eth-mainnet", time.Minute)
	_, err := svc.GetTokens(context.Background(), "0xABC")
	if err == nil || !errorsIs(err, ErrUpstream) {
		t.Errorf("expected ErrUpstream, got %v", err)
	}
}
```

Add this small helper at the bottom of `service_test.go` (keeps the test file self-contained):
```go
import "errors"

func errorsIs(err, target error) bool { return errors.Is(err, target) }
```
> Note: move the `errors` import into the existing import block at the top of the file rather than adding a second `import` statement.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/wallet/ -run TestGetTokens`
Expected: FAIL — `undefined: NewService`.

- [ ] **Step 3: Write the implementation**

Create `internal/wallet/service.go`:
```go
package wallet

import (
	"context"
	"fmt"
	"time"

	"wallet-api/internal/alchemy"
)

// Service orchestrates cache-first reads over Alchemy + Postgres.
type Service struct {
	alchemy AlchemyClient
	tokens  TokenStore
	txs     TxCache
	network string
	ttl     time.Duration
	now     func() time.Time
}

// NewService builds a Service with a real-time clock.
func NewService(a AlchemyClient, ts TokenStore, tc TxCache, network string, ttl time.Duration) *Service {
	return &Service{alchemy: a, tokens: ts, txs: tc, network: network, ttl: ttl, now: time.Now}
}

// GetTokens returns the address's token portfolio, served from the DB snapshot
// when fresh and otherwise fetched from Alchemy and written through to the DB.
func (s *Service) GetTokens(ctx context.Context, address string) (*TokenPortfolio, error) {
	addr := NormalizeAddress(address)
	if p, ok, err := s.tokens.GetFreshTokens(ctx, addr, s.network, s.ttl); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStore, err)
	} else if ok {
		return p, nil
	}
	raw, err := s.alchemy.GetTokens(ctx, addr, s.network)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUpstream, err)
	}
	tokens, err := normalizeTokens(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUpstream, err)
	}
	p := &TokenPortfolio{
		Address:   addr,
		Network:   s.network,
		FetchedAt: s.now().UTC(),
		Tokens:    tokens,
	}
	if err := s.tokens.SaveTokens(ctx, p); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStore, err)
	}
	return p, nil
}

// normalizeTokens converts raw Alchemy tokens into domain tokens with scaled balances.
func normalizeTokens(raw []alchemy.Token) ([]Token, error) {
	out := make([]Token, 0, len(raw))
	for _, r := range raw {
		rawDec, scaled, err := ScaleBalance(r.RawBalance, r.Decimals)
		if err != nil {
			return nil, err
		}
		t := Token{
			TokenAddress: r.TokenAddress,
			Symbol:       r.Symbol,
			Name:         r.Name,
			Decimals:     r.Decimals,
			RawBalance:   rawDec,
			Balance:      scaled,
			IsNative:     r.TokenAddress == nil,
		}
		if r.Price != nil {
			t.Price = &Price{Currency: r.Price.Currency, Value: r.Price.Value, LastUpdatedAt: r.Price.LastUpdatedAt}
		}
		out = append(out, t)
	}
	return out, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/wallet/ -run TestGetTokens`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/wallet/service.go internal/wallet/service_test.go
git commit -m "feat: add wallet service GetTokens with cache-first logic"
```

---

### Task 7: Wallet service — GetTransactions (cache-first + bypass)

**Files:**
- Modify: `internal/wallet/service.go` (add `GetTransactions` + transfer mapping)
- Test: `internal/wallet/service_test.go` (add cases)

**Interfaces:**
- Consumes: `Service`, `TxCache`, `alchemy.TransfersResult`.
- Produces: `(*Service).GetTransactions(ctx context.Context, address string, limit int, pageKey string) (*TransactionPage, error)`.
- Behavior: when `pageKey == ""`, read the cache (key `params = "limit=<n>"`); on miss, fetch + save. When `pageKey != ""`, bypass the cache entirely (fetch, do not save).

- [ ] **Step 1: Write the failing test**

Append to `internal/wallet/service_test.go`:
```go
func TestGetTransactionsCacheMissFetchesAndSaves(t *testing.T) {
	fa := &fakeAlchemy{transfers: alchemy.TransfersResult{Transfers: []alchemy.Transfer{
		{Hash: "0x1", From: "0xabc", To: "0xdef", Asset: "ETH", Value: "0.5", BlockNum: "0x20", Category: "external"},
	}}}
	tc := &fakeTxCache{}
	svc := NewService(fa, &fakeTokenStore{}, tc, "eth-mainnet", time.Minute)

	page, err := svc.GetTransactions(context.Background(), "0xABC", 25, "")
	if err != nil {
		t.Fatal(err)
	}
	if fa.txCalls != 1 || tc.saveCalls != 1 {
		t.Errorf("expected 1 fetch + 1 save, got fetch=%d save=%d", fa.txCalls, tc.saveCalls)
	}
	if page.Address != "0xabc" || len(page.Transfers) != 1 || page.Transfers[0].Hash != "0x1" {
		t.Errorf("page wrong: %+v", page)
	}
}

func TestGetTransactionsCacheHit(t *testing.T) {
	cached := &TransactionPage{Address: "0xabc", Transfers: []Transfer{{Hash: "0xcached"}}}
	fa := &fakeAlchemy{}
	tc := &fakeTxCache{saved: cached, fresh: true}
	svc := NewService(fa, &fakeTokenStore{}, tc, "eth-mainnet", time.Minute)

	page, err := svc.GetTransactions(context.Background(), "0xABC", 25, "")
	if err != nil {
		t.Fatal(err)
	}
	if fa.txCalls != 0 || page != cached {
		t.Errorf("expected cache hit, got fetch=%d", fa.txCalls)
	}
}

func TestGetTransactionsPageKeyBypassesCache(t *testing.T) {
	fa := &fakeAlchemy{transfers: alchemy.TransfersResult{Transfers: []alchemy.Transfer{{Hash: "0x2"}}}}
	tc := &fakeTxCache{saved: &TransactionPage{Transfers: []Transfer{{Hash: "0xcached"}}}, fresh: true}
	svc := NewService(fa, &fakeTokenStore{}, tc, "eth-mainnet", time.Minute)

	page, err := svc.GetTransactions(context.Background(), "0xABC", 25, "PAGEKEY123")
	if err != nil {
		t.Fatal(err)
	}
	if fa.txCalls != 1 {
		t.Errorf("pageKey should force a fetch, got %d", fa.txCalls)
	}
	if tc.saveCalls != 0 {
		t.Errorf("pageKey fetches must not be cached, got %d saves", tc.saveCalls)
	}
	if len(page.Transfers) != 1 || page.Transfers[0].Hash != "0x2" {
		t.Errorf("expected fresh page, got %+v", page)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/wallet/ -run TestGetTransactions`
Expected: FAIL — `undefined: (*Service).GetTransactions`.

- [ ] **Step 3: Add the implementation**

Append to `internal/wallet/service.go`:
```go
// GetTransactions returns a page of transfer history for address. The first page
// (no pageKey) is served cache-first and written through; pageKey requests bypass
// the cache.
func (s *Service) GetTransactions(ctx context.Context, address string, limit int, pageKey string) (*TransactionPage, error) {
	addr := NormalizeAddress(address)
	params := fmt.Sprintf("limit=%d", limit)

	if pageKey == "" {
		if p, ok, err := s.txs.GetFreshTransactions(ctx, addr, params, s.ttl); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrStore, err)
		} else if ok {
			return p, nil
		}
	}

	res, err := s.alchemy.GetTransfers(ctx, addr, limit, pageKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUpstream, err)
	}
	page := &TransactionPage{
		Address:     addr,
		Transfers:   mapTransfers(res.Transfers),
		NextPageKey: res.PageKey,
	}
	if pageKey == "" {
		if err := s.txs.SaveTransactions(ctx, addr, params, page); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrStore, err)
		}
	}
	return page, nil
}

func mapTransfers(in []alchemy.Transfer) []Transfer {
	out := make([]Transfer, 0, len(in))
	for _, t := range in {
		out = append(out, Transfer{
			Hash: t.Hash, From: t.From, To: t.To, Asset: t.Asset,
			Value: t.Value, BlockNum: t.BlockNum, Category: t.Category,
		})
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/wallet/`
Expected: PASS (all wallet tests).

- [ ] **Step 5: Commit**

```bash
git add internal/wallet/service.go internal/wallet/service_test.go
git commit -m "feat: add wallet service GetTransactions with cache bypass"
```

---

### Task 8: Postgres store + schema + docker-compose

**Files:**
- Create: `docker-compose.yml`
- Create: `internal/store/schema.sql`
- Create: `internal/store/store.go`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Consumes: `wallet.TokenPortfolio`, `wallet.TransactionPage`, `wallet.Token` (Task 5).
- Produces:
  - `store.New(ctx context.Context, dsn string) (*Postgres, error)`
  - `(*Postgres).Migrate(ctx context.Context) error`
  - `(*Postgres).Close()`
  - `*Postgres` implements `wallet.TokenStore` and `wallet.TxCache` (method set: `GetFreshTokens`, `SaveTokens`, `GetFreshTransactions`, `SaveTransactions`).

- [ ] **Step 1: Add the Postgres dependency**

Run:
```bash
go get github.com/jackc/pgx/v5@latest
```

- [ ] **Step 2: Write docker-compose**

Create `docker-compose.yml`:
```yaml
services:
  postgres:
    image: postgres:16
    environment:
      POSTGRES_USER: wallet
      POSTGRES_PASSWORD: wallet
      POSTGRES_DB: wallet
    ports:
      - "5432:5432"
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U wallet"]
      interval: 2s
      timeout: 3s
      retries: 10
```

- [ ] **Step 3: Write the schema**

Create `internal/store/schema.sql`:
```sql
CREATE TABLE IF NOT EXISTS wallet_tokens (
    address          TEXT NOT NULL,
    network          TEXT NOT NULL,
    token_key        TEXT NOT NULL,
    token_address    TEXT,
    is_native        BOOLEAN NOT NULL,
    symbol           TEXT,
    name             TEXT,
    decimals         INTEGER,
    raw_balance      TEXT NOT NULL,
    balance          TEXT NOT NULL,
    price_currency   TEXT,
    price_value      TEXT,
    price_updated_at TEXT,
    fetched_at       TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (address, network, token_key)
);

CREATE TABLE IF NOT EXISTS token_fetch_meta (
    address    TEXT NOT NULL,
    network    TEXT NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (address, network)
);

CREATE TABLE IF NOT EXISTS tx_cache (
    address    TEXT NOT NULL,
    params     TEXT NOT NULL DEFAULT '',
    payload    JSONB NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (address, params)
);
```

- [ ] **Step 4: Write the failing integration test**

Create `internal/store/store_test.go`:
```go
package store

import (
	"context"
	"os"
	"testing"
	"time"

	"wallet-api/internal/wallet"
)

func newTestStore(t *testing.T) *Postgres {
	t.Helper()
	dsn := os.Getenv("WALLET_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set WALLET_TEST_DATABASE_URL to run store integration tests")
	}
	ctx := context.Background()
	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// Clean slate.
	_, _ = s.pool.Exec(ctx, "TRUNCATE wallet_tokens, token_fetch_meta, tx_cache")
	t.Cleanup(s.Close)
	return s
}

func usdc(a string) *string { return &a }

func TestSaveAndGetFreshTokens(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := &wallet.TokenPortfolio{
		Address: "0xabc", Network: "eth-mainnet", FetchedAt: time.Now().UTC(),
		Tokens: []wallet.Token{
			{TokenAddress: nil, Symbol: "ETH", Name: "Ethereum", Decimals: 18, RawBalance: "15", Balance: "1.5", IsNative: true,
				Price: &wallet.Price{Currency: "usd", Value: "3200.50", LastUpdatedAt: "2026-06-23T00:00:00Z"}},
			{TokenAddress: usdc("0xA0B8"), Symbol: "USDC", Decimals: 6, RawBalance: "12500000", Balance: "12.5"},
		},
	}
	if err := s.SaveTokens(ctx, p); err != nil {
		t.Fatalf("SaveTokens: %v", err)
	}
	got, ok, err := s.GetFreshTokens(ctx, "0xabc", "eth-mainnet", time.Minute)
	if err != nil || !ok {
		t.Fatalf("GetFreshTokens ok=%v err=%v", ok, err)
	}
	if len(got.Tokens) != 2 {
		t.Fatalf("got %d tokens, want 2", len(got.Tokens))
	}
}

func TestGetFreshTokensExpired(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := &wallet.TokenPortfolio{Address: "0xabc", Network: "eth-mainnet", FetchedAt: time.Now().Add(-time.Hour).UTC()}
	if err := s.SaveTokens(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetFreshTokens(ctx, "0xabc", "eth-mainnet", time.Minute); ok {
		t.Error("expected stale snapshot to be reported not-fresh")
	}
}

func TestSaveTokensReplacesSnapshot(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	first := &wallet.TokenPortfolio{Address: "0xabc", Network: "eth-mainnet", FetchedAt: time.Now().UTC(),
		Tokens: []wallet.Token{{Symbol: "OLD", Decimals: 18, RawBalance: "1", Balance: "1", IsNative: true}}}
	if err := s.SaveTokens(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := &wallet.TokenPortfolio{Address: "0xabc", Network: "eth-mainnet", FetchedAt: time.Now().UTC(),
		Tokens: []wallet.Token{{TokenAddress: usdc("0xnew"), Symbol: "NEW", Decimals: 6, RawBalance: "2", Balance: "2"}}}
	if err := s.SaveTokens(ctx, second); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.GetFreshTokens(ctx, "0xabc", "eth-mainnet", time.Minute)
	if len(got.Tokens) != 1 || got.Tokens[0].Symbol != "NEW" {
		t.Errorf("snapshot not replaced: %+v", got.Tokens)
	}
}

func TestSaveAndGetFreshTransactions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	page := &wallet.TransactionPage{Address: "0xabc", Transfers: []wallet.Transfer{{Hash: "0x1", Asset: "ETH", Value: "0.5"}}}
	if err := s.SaveTransactions(ctx, "0xabc", "limit=25", page); err != nil {
		t.Fatalf("SaveTransactions: %v", err)
	}
	got, ok, err := s.GetFreshTransactions(ctx, "0xabc", "limit=25", time.Minute)
	if err != nil || !ok {
		t.Fatalf("GetFreshTransactions ok=%v err=%v", ok, err)
	}
	if len(got.Transfers) != 1 || got.Transfers[0].Hash != "0x1" {
		t.Errorf("round-trip wrong: %+v", got)
	}
}
```

- [ ] **Step 5: Run test to verify it fails**

Run: `go test ./internal/store/`
Expected: FAIL to compile — `undefined: New` / `undefined: Postgres`. (If `WALLET_TEST_DATABASE_URL` is unset, tests would skip — but compilation must pass first, so the failure here is the build error.)

- [ ] **Step 6: Write the implementation**

Create `internal/store/store.go`:
```go
package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"wallet-api/internal/wallet"
)

//go:embed schema.sql
var schemaSQL string

// Postgres implements wallet.TokenStore and wallet.TxCache over a pgx pool.
type Postgres struct {
	pool *pgxpool.Pool
}

// New opens a connection pool and verifies connectivity.
func New(ctx context.Context, dsn string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

// Migrate applies the embedded schema (idempotent).
func (s *Postgres) Migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	return nil
}

// Close releases the pool.
func (s *Postgres) Close() { s.pool.Close() }

func tokenKey(t wallet.Token) string {
	if t.IsNative || t.TokenAddress == nil {
		return "native"
	}
	return *t.TokenAddress
}

// SaveTokens replaces the token snapshot for (address, network) in one transaction.
func (s *Postgres) SaveTokens(ctx context.Context, p *wallet.TokenPortfolio) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM wallet_tokens WHERE address=$1 AND network=$2`, p.Address, p.Network); err != nil {
		return err
	}
	for _, t := range p.Tokens {
		var curr, val, updated *string
		if t.Price != nil {
			curr, val, updated = &t.Price.Currency, &t.Price.Value, &t.Price.LastUpdatedAt
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO wallet_tokens
			(address, network, token_key, token_address, is_native, symbol, name, decimals,
			 raw_balance, balance, price_currency, price_value, price_updated_at, fetched_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
			p.Address, p.Network, tokenKey(t), t.TokenAddress, t.IsNative, t.Symbol, t.Name, t.Decimals,
			t.RawBalance, t.Balance, curr, val, updated, p.FetchedAt)
		if err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO token_fetch_meta (address, network, fetched_at)
		VALUES ($1,$2,$3)
		ON CONFLICT (address, network) DO UPDATE SET fetched_at=EXCLUDED.fetched_at`,
		p.Address, p.Network, p.FetchedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GetFreshTokens returns the snapshot if its fetch time is within ttl.
func (s *Postgres) GetFreshTokens(ctx context.Context, address, network string, ttl time.Duration) (*wallet.TokenPortfolio, bool, error) {
	var fetchedAt time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT fetched_at FROM token_fetch_meta
		WHERE address=$1 AND network=$2 AND fetched_at > now() - $3::interval`,
		address, network, intervalArg(ttl)).Scan(&fetchedAt)
	if err == pgx.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT token_address, is_native, symbol, name, decimals, raw_balance, balance,
		       price_currency, price_value, price_updated_at
		FROM wallet_tokens WHERE address=$1 AND network=$2`, address, network)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	p := &wallet.TokenPortfolio{Address: address, Network: network, FetchedAt: fetchedAt}
	for rows.Next() {
		var t wallet.Token
		var curr, val, updated *string
		if err := rows.Scan(&t.TokenAddress, &t.IsNative, &t.Symbol, &t.Name, &t.Decimals,
			&t.RawBalance, &t.Balance, &curr, &val, &updated); err != nil {
			return nil, false, err
		}
		if curr != nil {
			t.Price = &wallet.Price{Currency: *curr, Value: derefStr(val), LastUpdatedAt: derefStr(updated)}
		}
		p.Tokens = append(p.Tokens, t)
	}
	return p, true, rows.Err()
}

// SaveTransactions upserts a transaction page as JSON.
func (s *Postgres) SaveTransactions(ctx context.Context, address, params string, page *wallet.TransactionPage) error {
	payload, err := json.Marshal(page)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO tx_cache (address, params, payload, fetched_at)
		VALUES ($1,$2,$3, now())
		ON CONFLICT (address, params) DO UPDATE SET payload=EXCLUDED.payload, fetched_at=now()`,
		address, params, payload)
	return err
}

// GetFreshTransactions returns the cached page if within ttl.
func (s *Postgres) GetFreshTransactions(ctx context.Context, address, params string, ttl time.Duration) (*wallet.TransactionPage, bool, error) {
	var payload []byte
	err := s.pool.QueryRow(ctx, `
		SELECT payload FROM tx_cache
		WHERE address=$1 AND params=$2 AND fetched_at > now() - $3::interval`,
		address, params, intervalArg(ttl)).Scan(&payload)
	if err == pgx.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var page wallet.TransactionPage
	if err := json.Unmarshal(payload, &page); err != nil {
		return nil, false, err
	}
	return &page, true, nil
}

func intervalArg(ttl time.Duration) string {
	return fmt.Sprintf("%d seconds", int64(ttl.Seconds()))
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
```

> The implementation needs the `time` package. Add `"time"` to the import block (alongside the existing imports) — do not create a second import statement.

- [ ] **Step 7: Run go vet + build, then start Postgres and run integration tests**

Run:
```bash
go build ./internal/store/
go vet ./internal/store/
docker compose up -d
WALLET_TEST_DATABASE_URL="postgres://wallet:wallet@localhost:5432/wallet" go test ./internal/store/ -v
```
Expected: build succeeds; integration tests PASS (not skipped). If Docker is unavailable, the tests SKIP — note this and proceed; do not mark the task complete on a skip without saying so.

- [ ] **Step 8: Commit**

```bash
git add docker-compose.yml internal/store/ go.mod go.sum
git commit -m "feat: add postgres store, schema, and docker-compose"
```

---

### Task 9: HTTP API — router, handlers, validation

**Files:**
- Create: `internal/api/router.go`
- Create: `internal/api/handlers.go`
- Test: `internal/api/handlers_test.go`

**Interfaces:**
- Consumes: `wallet.TokenPortfolio`, `wallet.TransactionPage`, `wallet.ErrUpstream`, `wallet.ErrStore`.
- Produces:
  - `api.WalletService` interface: `GetTokens(ctx, address string) (*wallet.TokenPortfolio, error)`, `GetTransactions(ctx, address string, limit int, pageKey string) (*wallet.TransactionPage, error)` (satisfied by `*wallet.Service`).
  - `api.NewRouter(svc WalletService) http.Handler`
  - `api.ValidAddress(s string) bool`

- [ ] **Step 1: Write the failing test**

Create `internal/api/handlers_test.go`:
```go
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"wallet-api/internal/wallet"
)

type stubService struct {
	portfolio *wallet.TokenPortfolio
	page      *wallet.TransactionPage
	err       error
	lastLimit int
	lastPage  string
}

func (s *stubService) GetTokens(ctx context.Context, address string) (*wallet.TokenPortfolio, error) {
	return s.portfolio, s.err
}
func (s *stubService) GetTransactions(ctx context.Context, address string, limit int, pageKey string) (*wallet.TransactionPage, error) {
	s.lastLimit, s.lastPage = limit, pageKey
	return s.page, s.err
}

func doGet(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

const validAddr = "0x1234567890abcdef1234567890abcdef12345678"

func TestTokensEndpointOK(t *testing.T) {
	svc := &stubService{portfolio: &wallet.TokenPortfolio{Address: validAddr, Network: "eth-mainnet"}}
	rec := doGet(NewRouter(svc), "/v1/addresses/"+validAddr+"/tokens")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var got wallet.TokenPortfolio
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if got.Address != validAddr {
		t.Errorf("address = %q", got.Address)
	}
}

func TestTokensEndpointRejectsBadAddress(t *testing.T) {
	svc := &stubService{}
	rec := doGet(NewRouter(svc), "/v1/addresses/not-an-address/tokens")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestTransactionsParsesQueryParams(t *testing.T) {
	svc := &stubService{page: &wallet.TransactionPage{Address: validAddr}}
	rec := doGet(NewRouter(svc), "/v1/addresses/"+validAddr+"/transactions?limit=5&pageKey=abc")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if svc.lastLimit != 5 || svc.lastPage != "abc" {
		t.Errorf("params not parsed: limit=%d pageKey=%q", svc.lastLimit, svc.lastPage)
	}
}

func TestTransactionsDefaultLimit(t *testing.T) {
	svc := &stubService{page: &wallet.TransactionPage{Address: validAddr}}
	doGet(NewRouter(svc), "/v1/addresses/"+validAddr+"/transactions")
	if svc.lastLimit != 25 {
		t.Errorf("default limit = %d, want 25", svc.lastLimit)
	}
}

func TestUpstreamErrorMapsTo502(t *testing.T) {
	svc := &stubService{err: wallet.ErrUpstream}
	rec := doGet(NewRouter(svc), "/v1/addresses/"+validAddr+"/tokens")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}

func TestStoreErrorMapsTo503(t *testing.T) {
	svc := &stubService{err: wallet.ErrStore}
	rec := doGet(NewRouter(svc), "/v1/addresses/"+validAddr+"/tokens")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestValidAddress(t *testing.T) {
	if !ValidAddress(validAddr) {
		t.Error("valid address rejected")
	}
	for _, bad := range []string{"", "0x123", "1234567890abcdef1234567890abcdef12345678", "0xZZZ4567890abcdef1234567890abcdef12345678"} {
		if ValidAddress(bad) {
			t.Errorf("invalid address accepted: %q", bad)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api/`
Expected: FAIL — `undefined: NewRouter` / `undefined: ValidAddress`.

- [ ] **Step 3: Write the router**

Create `internal/api/router.go`:
```go
package api

import (
	"context"
	"net/http"

	"wallet-api/internal/wallet"
)

// WalletService is the behavior the HTTP layer needs from the domain service.
type WalletService interface {
	GetTokens(ctx context.Context, address string) (*wallet.TokenPortfolio, error)
	GetTransactions(ctx context.Context, address string, limit int, pageKey string) (*wallet.TransactionPage, error)
}

// NewRouter wires the read-only wallet endpoints.
func NewRouter(svc WalletService) http.Handler {
	h := &handlers{svc: svc}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/addresses/{address}/tokens", h.getTokens)
	mux.HandleFunc("GET /v1/addresses/{address}/transactions", h.getTransactions)
	return mux
}
```

- [ ] **Step 4: Write the handlers**

Create `internal/api/handlers.go`:
```go
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"

	"wallet-api/internal/wallet"
)

const (
	defaultLimit = 25
	maxLimit     = 100
)

var addressRE = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

// ValidAddress reports whether s is a 0x-prefixed 20-byte hex address.
func ValidAddress(s string) bool { return addressRE.MatchString(s) }

type handlers struct {
	svc WalletService
}

func (h *handlers) getTokens(w http.ResponseWriter, r *http.Request) {
	address := r.PathValue("address")
	if !ValidAddress(address) {
		writeError(w, http.StatusBadRequest, "invalid address")
		return
	}
	p, err := h.svc.GetTokens(r.Context(), address)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *handlers) getTransactions(w http.ResponseWriter, r *http.Request) {
	address := r.PathValue("address")
	if !ValidAddress(address) {
		writeError(w, http.StatusBadRequest, "invalid address")
		return
	}
	limit := parseLimit(r.URL.Query().Get("limit"))
	pageKey := r.URL.Query().Get("pageKey")
	page, err := h.svc.GetTransactions(r.Context(), address, limit, pageKey)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func parseLimit(raw string) int {
	if raw == "" {
		return defaultLimit
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultLimit
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, wallet.ErrUpstream):
		writeError(w, http.StatusBadGateway, "upstream provider error")
	case errors.Is(err, wallet.ErrStore):
		writeError(w, http.StatusServiceUnavailable, "storage unavailable")
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/api/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/api/
git commit -m "feat: add HTTP router, handlers, and address validation"
```

---

### Task 10: Wire everything in main + full build

**Files:**
- Create: `cmd/server/main.go`

**Interfaces:**
- Consumes: `config.Load`, `store.New`/`Migrate`, `alchemy.New`, `wallet.NewService`, `api.NewRouter`.
- Produces: a runnable `wallet-api` server binary.

- [ ] **Step 1: Write main**

Create `cmd/server/main.go`:
```go
package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"wallet-api/internal/alchemy"
	"wallet-api/internal/api"
	"wallet-api/internal/config"
	"wallet-api/internal/store"
	"wallet-api/internal/wallet"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pg, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer pg.Close()
	if err := pg.Migrate(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	ac := alchemy.New(cfg.AlchemyAPIKey, cfg.AlchemyNetwork)
	svc := wallet.NewService(ac, pg, pg, cfg.AlchemyNetwork, cfg.CacheTTL)
	router := api.NewRouter(svc)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("wallet-api listening on :%s (network=%s, ttl=%s)", cfg.Port, cfg.AlchemyNetwork, cfg.CacheTTL)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server: %v", err)
	}
}
```

- [ ] **Step 2: Build and vet the whole module**

Run:
```bash
go build ./...
go vet ./...
```
Expected: no output (success).

- [ ] **Step 3: Run the full unit test suite**

Run: `go test ./...`
Expected: PASS for `config`, `wallet`, `alchemy`, `api`; `store` PASSES (if Postgres is up via `WALLET_TEST_DATABASE_URL`) or SKIPS otherwise.

- [ ] **Step 4: Manual smoke test (optional, needs real Alchemy key + Postgres)**

Run:
```bash
docker compose up -d
export ALCHEMY_API_KEY=<your-key>
export DATABASE_URL="postgres://wallet:wallet@localhost:5432/wallet"
go run ./cmd/server &
curl -s localhost:8080/v1/addresses/0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045/tokens | head -c 400
curl -s "localhost:8080/v1/addresses/0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045/transactions?limit=3" | head -c 400
```
Expected: JSON token portfolio and transaction list. A second `tokens` call within the TTL is served from Postgres (no Alchemy call).

- [ ] **Step 5: Commit**

```bash
git add cmd/server/main.go
git commit -m "feat: wire config, store, alchemy, service, and HTTP server"
```

---

## Self-Review

**Spec coverage:**
- Read-only EVM wallet, Go, non-custodial → Tasks 1-10, no key handling anywhere. ✓
- `tokens` endpoint via Portfolio API with the four flags → Task 3 (request body) + Task 9 (route). ✓
- `transactions` endpoint via `getAssetTransfers` → Task 4 + Task 9. ✓
- Structured `wallet_tokens` newest-snapshot table + `token_fetch_meta` freshness marker → Task 8. ✓
- `tx_cache` JSON cache for transactions → Task 8. ✓
- Cache-first read-through, write-through on miss, configurable TTL → Tasks 6-7 (logic), Task 8 (storage), Task 1 (`CACHE_TTL_SECONDS`). ✓
- pageKey bypass for transactions → Task 7. ✓
- Snapshot replacement (delete + insert in one tx) so dropped tokens disappear → Task 8 `SaveTokens` + `TestSaveTokensReplacesSnapshot`. ✓
- Empty-wallet freshness via `token_fetch_meta` → Task 8 `GetFreshTokens` reads the meta row first. ✓
- String-typed large numbers → Task 2 `ScaleBalance`, domain types use `string`. ✓
- Error mapping 400/502/503 → Task 9 `writeServiceError` + validation. ✓
- Config env vars + defaults → Task 1. ✓
- Tests with no live network calls; Postgres integration guarded/skippable → Tasks 3-9. ✓
- docker-compose for local Postgres → Task 8. ✓

**Placeholder scan:** No TBD/TODO; every code step contains complete code. ✓

**Type consistency:** `TokenPortfolio`, `Token`, `Transfer`, `TransactionPage`, `Price` defined once in Task 5 and used unchanged in Tasks 6-9. Store method names (`GetFreshTokens`, `SaveTokens`, `GetFreshTransactions`, `SaveTransactions`) match the `wallet.TokenStore`/`wallet.TxCache` interfaces from Task 5. `AlchemyClient` interface (Task 5) matches the concrete `alchemy.Client` methods (Tasks 3-4). `WalletService` (Task 9) matches `*wallet.Service` methods (Tasks 6-7). ✓

**Deviations surfaced:** transactions pagination simplified to a single merged page (`nextPageKey` always empty) — documented in Global Constraints and consistent with the spec's "multi-page history intentionally not cached" note. Schema embedded at `internal/store/schema.sql` rather than `migrations/0001_init.sql` (avoids `go:embed` parent-path limitation) — same DDL, applied on boot as specified.

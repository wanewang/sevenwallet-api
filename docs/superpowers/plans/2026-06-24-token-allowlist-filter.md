# Token Allowlist Filter & Enrichment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Filter wallet read responses to an hourly-refreshed LI.FI token allowlist and enrich recognized tokens with LI.FI metadata.

**Architecture:** A background refresher fetches the LI.FI ETH token list every hour, persists it to Postgres (durable) and Redis (shared warm cache), and swaps it into an in-process snapshot (atomic pointer) that serves O(1) lookups. The wallet service reads the snapshot per request and applies an allowlist filter + enrichment to `/tokens` and a best-effort symbol filter to `/transactions`. Approach B from the spec: in-memory hot path, Redis+Postgres as the durable warm-up tier.

**Tech Stack:** Go 1.25, pgx/v5 (Postgres), go-redis/v9 (Redis), standard `net/http`.

## Global Constraints

- Go version floor: `go 1.25.0` (from `go.mod`).
- Public repo: commit messages may keep the `Co-Authored-By:` trailer and the `🤖 Generated with Claude Code` line, but MUST NOT include any `Claude-Session:` / `https://claude.ai/code/session_...` URL (from `CLAUDE.md`).
- LI.FI source: `GET https://li.quest/v1/tokens?chain=ETH`; response envelope is `{"tokens": {"<chainId>": [ {address, symbol, name, decimals, coinKey, logoURI, priceUSD}, ... ]}}` keyed by chain id (ETH = `"1"`).
- Storage shape: one JSON blob per chain in both tiers (no per-token rows).
- Filtering/enrichment happens at read time on every call (caches keep storing raw Alchemy data).
- Addresses are matched lowercased; symbols matched uppercased.
- Integration tests that need infra skip when their env var is unset (`WALLET_TEST_DATABASE_URL`, `WALLET_TEST_REDIS_URL`), mirroring the existing `store_test.go` pattern.

## File Structure

**New:**
- `internal/lifi/client.go` — LI.FI HTTP client + `ListToken` type.
- `internal/lifi/client_test.go` — `httptest` parse test.
- `internal/tokenlist/snapshot.go` — `Snapshot` (lookup maps) + `Holder` (atomic pointer, implements the allowlist methods).
- `internal/tokenlist/snapshot_test.go` — snapshot/holder unit tests.
- `internal/tokenlist/refresher.go` — `Refresher` (`Bootstrap`, `Run`, `refresh`, `persist`) + dep interfaces.
- `internal/tokenlist/refresher_test.go` — bootstrap-ladder + refresh swap/retain tests with fakes.
- `internal/rediscache/cache.go` — Redis wrapper with `SaveTokenList`/`LoadTokenList`.
- `internal/rediscache/cache_test.go` — round-trip integration test (skips without `WALLET_TEST_REDIS_URL`).

**Modified:**
- `internal/config/config.go` + `config_test.go` — add `REDIS_URL` (required), `LIFI_TOKENS_URL`, `LIFI_CHAIN`, `LIFI_REFRESH_SECONDS`.
- `internal/store/schema.sql` — add `lifi_token_lists` table.
- `internal/store/store.go` + `store_test.go` — add `SaveTokenList`/`LoadTokenList`.
- `internal/wallet/types.go` — add `Allowlist` interface + `Token.LogoURI/CoinKey/PriceUSD`.
- `internal/wallet/service.go` — `allow` dependency; filter+enrich in `GetTokens`; filter in `GetTransactions`.
- `internal/wallet/fakes_test.go` + `service_test.go` — fake allowlist; updated call sites/assertions.
- `cmd/server/main.go` — wire Redis, LI.FI client, holder, refresher.
- `docker-compose.yml` — add `redis` service.
- `go.mod` / `go.sum` — add `github.com/redis/go-redis/v9`.
- `README.md` — env vars, layout, run instructions.

---

## Task 1: Config — LI.FI + Redis settings

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `config.Config` gains fields `RedisURL string`, `LifiTokensURL string`, `LifiChain string`, `LifiRefresh time.Duration`.

- [ ] **Step 1: Update existing tests + add new ones (failing)**

In `internal/config/config_test.go`, every `loadFrom` call must now include `REDIS_URL` (newly required). Replace the file body's three tests with:

```go
func TestLoadFromAppliesDefaults(t *testing.T) {
	env := map[string]string{
		"ALCHEMY_API_KEY": "key123",
		"DATABASE_URL":    "postgres://localhost:5432/wallet",
		"REDIS_URL":       "redis://localhost:6379/0",
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
	if cfg.LifiTokensURL != "https://li.quest/v1/tokens" {
		t.Errorf("lifi url = %q, want default", cfg.LifiTokensURL)
	}
	if cfg.LifiChain != "ETH" {
		t.Errorf("lifi chain = %q, want ETH", cfg.LifiChain)
	}
	if cfg.LifiRefresh != 3600*time.Second {
		t.Errorf("lifi refresh = %v, want 3600s", cfg.LifiRefresh)
	}
}

func TestLoadFromHonoursOverrides(t *testing.T) {
	env := map[string]string{
		"ALCHEMY_API_KEY":      "key123",
		"DATABASE_URL":         "postgres://db",
		"REDIS_URL":            "redis://cache:6379/1",
		"ALCHEMY_NETWORK":      "eth-sepolia",
		"CACHE_TTL_SECONDS":    "30",
		"PORT":                 "9000",
		"LIFI_TOKENS_URL":      "http://localhost:9999/v1/tokens",
		"LIFI_CHAIN":           "DAI",
		"LIFI_REFRESH_SECONDS": "60",
	}
	cfg, err := loadFrom(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AlchemyNetwork != "eth-sepolia" || cfg.CacheTTL != 30*time.Second || cfg.Port != "9000" {
		t.Errorf("overrides not applied: %+v", cfg)
	}
	if cfg.RedisURL != "redis://cache:6379/1" {
		t.Errorf("redis url = %q", cfg.RedisURL)
	}
	if cfg.LifiTokensURL != "http://localhost:9999/v1/tokens" || cfg.LifiChain != "DAI" || cfg.LifiRefresh != 60*time.Second {
		t.Errorf("lifi overrides not applied: %+v", cfg)
	}
}

func TestLoadFromRequiresKeyAndDB(t *testing.T) {
	if _, err := loadFrom(func(string) string { return "" }); err == nil {
		t.Fatal("expected error when ALCHEMY_API_KEY/DATABASE_URL missing")
	}
	env := map[string]string{"ALCHEMY_API_KEY": "key123"}
	if _, err := loadFrom(func(k string) string { return env[k] }); err == nil {
		t.Fatal("expected error when DATABASE_URL missing")
	}
}

func TestLoadFromRequiresRedisURL(t *testing.T) {
	env := map[string]string{
		"ALCHEMY_API_KEY": "key123",
		"DATABASE_URL":    "postgres://db",
	}
	if _, err := loadFrom(func(k string) string { return env[k] }); err == nil {
		t.Fatal("expected error when REDIS_URL missing")
	}
}

func TestLoadFromRejectsBadRefresh(t *testing.T) {
	env := map[string]string{
		"ALCHEMY_API_KEY":      "key123",
		"DATABASE_URL":         "postgres://db",
		"REDIS_URL":            "redis://localhost:6379",
		"LIFI_REFRESH_SECONDS": "0",
	}
	if _, err := loadFrom(func(k string) string { return env[k] }); err == nil {
		t.Fatal("expected error for non-positive LIFI_REFRESH_SECONDS")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ -run TestLoadFrom -v`
Expected: FAIL (compile error: `cfg.LifiTokensURL` undefined / `RedisURL` undefined).

- [ ] **Step 3: Add the fields and loading logic**

In `internal/config/config.go`, add to the `Config` struct (after `Port string`):

```go
	RedisURL      string
	LifiTokensURL string
	LifiChain     string
	LifiRefresh   time.Duration
```

In `loadFrom`, add `RedisURL: getenv("REDIS_URL")` to the initial `cfg := Config{...}` literal, then after the existing `DATABASE_URL` check insert:

```go
	if cfg.RedisURL == "" {
		return Config{}, fmt.Errorf("REDIS_URL is required")
	}
```

After the `Port` default block, before computing `CacheTTL`, add:

```go
	cfg.LifiTokensURL = getenv("LIFI_TOKENS_URL")
	if cfg.LifiTokensURL == "" {
		cfg.LifiTokensURL = "https://li.quest/v1/tokens"
	}
	cfg.LifiChain = getenv("LIFI_CHAIN")
	if cfg.LifiChain == "" {
		cfg.LifiChain = "ETH"
	}
	refresh := 3600
	if raw := getenv("LIFI_REFRESH_SECONDS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("LIFI_REFRESH_SECONDS must be a positive integer, got %q", raw)
		}
		refresh = n
	}
	cfg.LifiRefresh = time.Duration(refresh) * time.Second
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS (all config tests).

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add REDIS_URL and LIFI_* settings

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 2: LI.FI client

**Files:**
- Create: `internal/lifi/client.go`
- Test: `internal/lifi/client_test.go`

**Interfaces:**
- Consumes: nothing (stdlib only).
- Produces:
  - `type ListToken struct { Address, Symbol, Name string; Decimals int; CoinKey, LogoURI, PriceUSD string }` (JSON tags `address`, `symbol`, `name`, `decimals`, `coinKey`, `logoURI`, `priceUSD`).
  - `func New(tokensURL string) *Client`
  - `func (c *Client) GetTokens(ctx context.Context, chain string) ([]ListToken, error)`

- [ ] **Step 1: Write the failing test**

Create `internal/lifi/client_test.go`:

```go
package lifi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetTokensParsesEnvelope(t *testing.T) {
	const body = `{"tokens":{"1":[
		{"address":"0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48","symbol":"USDC","name":"USD Coin","decimals":6,"coinKey":"USDC","logoURI":"https://logo/usdc.png","priceUSD":"1.0001"},
		{"address":"0x0000000000000000000000000000000000000000","symbol":"ETH","name":"Ethereum","decimals":18,"coinKey":"ETH","logoURI":"https://logo/eth.png","priceUSD":"3200.50"}
	]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("chain"); got != "ETH" {
			t.Errorf("chain query = %q, want ETH", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := New(srv.URL)
	tokens, err := c.GetTokens(context.Background(), "ETH")
	if err != nil {
		t.Fatalf("GetTokens: %v", err)
	}
	if len(tokens) != 2 {
		t.Fatalf("got %d tokens, want 2", len(tokens))
	}
	if tokens[0].Symbol != "USDC" || tokens[0].Decimals != 6 || tokens[0].LogoURI != "https://logo/usdc.png" || tokens[0].PriceUSD != "1.0001" || tokens[0].CoinKey != "USDC" {
		t.Errorf("USDC mapping wrong: %+v", tokens[0])
	}
}

func TestGetTokensNon2xxErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	if _, err := New(srv.URL).GetTokens(context.Background(), "ETH"); err == nil {
		t.Fatal("expected error on non-2xx status")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lifi/ -v`
Expected: FAIL (compile error: package has no `New`/`GetTokens`).

- [ ] **Step 3: Write the implementation**

Create `internal/lifi/client.go`:

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/lifi/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/lifi/
git commit -m "feat(lifi): add LI.FI token-list client

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 3: Token-list snapshot + holder

**Files:**
- Create: `internal/tokenlist/snapshot.go`
- Test: `internal/tokenlist/snapshot_test.go`

**Interfaces:**
- Consumes: `lifi.ListToken` (Task 2).
- Produces:
  - `func NewSnapshot(chain string, tokens []lifi.ListToken, fetchedAt time.Time) *Snapshot`
  - `func (s *Snapshot) LookupByAddress(addr string) (lifi.ListToken, bool)`
  - `func (s *Snapshot) HasSymbol(sym string) bool`
  - `func (s *Snapshot) Count() int`, `func (s *Snapshot) FetchedAt() time.Time`
  - `type Holder struct{...}` with `Current() *Snapshot`, `Set(*Snapshot)`, and the same `LookupByAddress`/`HasSymbol` methods delegating to the current snapshot (so `*Holder` satisfies `wallet.Allowlist`).

- [ ] **Step 1: Write the failing test**

Create `internal/tokenlist/snapshot_test.go`:

```go
package tokenlist

import (
	"testing"
	"time"

	"wallet-api/internal/lifi"
)

func sampleTokens() []lifi.ListToken {
	return []lifi.ListToken{
		{Address: "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", Symbol: "USDC", Name: "USD Coin", Decimals: 6, LogoURI: "u", PriceUSD: "1.00"},
		{Address: "0xdAC17F958D2ee523a2206206994597C13D831ec7", Symbol: "USDT", Name: "Tether", Decimals: 6},
	}
}

func TestSnapshotLookupByAddressCaseInsensitive(t *testing.T) {
	s := NewSnapshot("ETH", sampleTokens(), time.Now())
	got, ok := s.LookupByAddress("0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48")
	if !ok {
		t.Fatal("expected USDC lookup hit (lowercased)")
	}
	if got.Symbol != "USDC" || got.Decimals != 6 {
		t.Errorf("wrong token: %+v", got)
	}
	if _, ok := s.LookupByAddress("0xdeadbeef"); ok {
		t.Error("unknown address should miss")
	}
}

func TestSnapshotHasSymbolCaseInsensitive(t *testing.T) {
	s := NewSnapshot("ETH", sampleTokens(), time.Now())
	if !s.HasSymbol("usdc") || !s.HasSymbol("USDT") {
		t.Error("expected known symbols to match case-insensitively")
	}
	if s.HasSymbol("SCAM") {
		t.Error("unknown symbol should not match")
	}
}

func TestHolderAtomicSetAndCurrent(t *testing.T) {
	var h Holder
	if h.Current() != nil {
		t.Fatal("zero-value holder Current() should be nil")
	}
	if _, ok := h.LookupByAddress("0xanything"); ok {
		t.Error("nil snapshot lookup should miss, not panic")
	}
	if h.HasSymbol("ETH") {
		t.Error("nil snapshot HasSymbol should be false")
	}
	h.Set(NewSnapshot("ETH", sampleTokens(), time.Now()))
	if h.Current() == nil || h.Current().Count() != 2 {
		t.Fatalf("after Set, Count = %v, want 2", h.Current())
	}
	if _, ok := h.LookupByAddress("0xdAC17F958D2ee523a2206206994597C13D831ec7"); !ok {
		t.Error("holder should delegate lookup to current snapshot")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tokenlist/ -v`
Expected: FAIL (compile error: no `NewSnapshot`/`Holder`).

- [ ] **Step 3: Write the implementation**

Create `internal/tokenlist/snapshot.go`:

```go
// Package tokenlist holds the LI.FI allowlist as an in-process snapshot and
// refreshes it from LI.FI on a schedule, persisting to Redis and Postgres.
package tokenlist

import (
	"strings"
	"sync/atomic"
	"time"

	"wallet-api/internal/lifi"
)

// Snapshot is an immutable, indexed view of the token list.
type Snapshot struct {
	chain     string
	fetchedAt time.Time
	byAddress map[string]lifi.ListToken
	symbols   map[string]struct{}
}

// NewSnapshot indexes tokens by lowercased address and uppercased symbol.
func NewSnapshot(chain string, tokens []lifi.ListToken, fetchedAt time.Time) *Snapshot {
	byAddr := make(map[string]lifi.ListToken, len(tokens))
	syms := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		if t.Address != "" {
			byAddr[strings.ToLower(t.Address)] = t
		}
		if t.Symbol != "" {
			syms[strings.ToUpper(t.Symbol)] = struct{}{}
		}
	}
	return &Snapshot{chain: chain, fetchedAt: fetchedAt, byAddress: byAddr, symbols: syms}
}

// LookupByAddress returns the list token for a (case-insensitive) address.
func (s *Snapshot) LookupByAddress(addr string) (lifi.ListToken, bool) {
	t, ok := s.byAddress[strings.ToLower(strings.TrimSpace(addr))]
	return t, ok
}

// HasSymbol reports whether a (case-insensitive) symbol is in the list.
func (s *Snapshot) HasSymbol(sym string) bool {
	_, ok := s.symbols[strings.ToUpper(strings.TrimSpace(sym))]
	return ok
}

// Count returns the number of indexed addresses.
func (s *Snapshot) Count() int { return len(s.byAddress) }

// FetchedAt returns when the snapshot was fetched.
func (s *Snapshot) FetchedAt() time.Time { return s.fetchedAt }

// Holder stores the current snapshot for lock-free reads and atomic swaps.
// A nil current snapshot makes all lookups miss (never panics).
type Holder struct {
	ptr atomic.Pointer[Snapshot]
}

// Current returns the current snapshot (nil before the first Set).
func (h *Holder) Current() *Snapshot { return h.ptr.Load() }

// Set atomically swaps in a new snapshot.
func (h *Holder) Set(s *Snapshot) { h.ptr.Store(s) }

// LookupByAddress delegates to the current snapshot.
func (h *Holder) LookupByAddress(addr string) (lifi.ListToken, bool) {
	s := h.Current()
	if s == nil {
		return lifi.ListToken{}, false
	}
	return s.LookupByAddress(addr)
}

// HasSymbol delegates to the current snapshot.
func (h *Holder) HasSymbol(sym string) bool {
	s := h.Current()
	if s == nil {
		return false
	}
	return s.HasSymbol(sym)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tokenlist/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tokenlist/snapshot.go internal/tokenlist/snapshot_test.go
git commit -m "feat(tokenlist): add allowlist snapshot and atomic holder

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 4: Postgres token-list persistence

**Files:**
- Modify: `internal/store/schema.sql`
- Modify: `internal/store/store.go`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Consumes: `lifi.ListToken` (Task 2).
- Produces (methods on `*store.Postgres`):
  - `func (s *Postgres) SaveTokenList(ctx context.Context, chain string, tokens []lifi.ListToken, fetchedAt time.Time) error`
  - `func (s *Postgres) LoadTokenList(ctx context.Context, chain string) ([]lifi.ListToken, time.Time, bool, error)`

- [ ] **Step 1: Add the schema table**

Append to `internal/store/schema.sql`:

```sql

CREATE TABLE IF NOT EXISTS lifi_token_lists (
    chain      TEXT PRIMARY KEY,
    payload    JSONB       NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL
);
```

- [ ] **Step 2: Write the failing test**

Add to `internal/store/store_test.go`. First, extend the `TRUNCATE` in `newTestStore` so the new table is reset:

Change the line:
```go
	_, _ = s.pool.Exec(ctx, "TRUNCATE wallet_tokens, token_fetch_meta, tx_cache")
```
to:
```go
	_, _ = s.pool.Exec(ctx, "TRUNCATE wallet_tokens, token_fetch_meta, tx_cache, lifi_token_lists")
```

Then append the test (and its import) — add `"wallet-api/internal/lifi"` to the import block:

```go
func TestSaveAndLoadTokenList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, _, ok, err := s.LoadTokenList(ctx, "ETH"); err != nil || ok {
		t.Fatalf("empty load: ok=%v err=%v", ok, err)
	}

	fetched := time.Now().UTC().Truncate(time.Second)
	tokens := []lifi.ListToken{
		{Address: "0xA0B8", Symbol: "USDC", Name: "USD Coin", Decimals: 6, CoinKey: "USDC", LogoURI: "u", PriceUSD: "1.00"},
		{Address: "0xdAC1", Symbol: "USDT", Name: "Tether", Decimals: 6},
	}
	if err := s.SaveTokenList(ctx, "ETH", tokens, fetched); err != nil {
		t.Fatalf("SaveTokenList: %v", err)
	}
	got, gotAt, ok, err := s.LoadTokenList(ctx, "ETH")
	if err != nil || !ok {
		t.Fatalf("LoadTokenList ok=%v err=%v", ok, err)
	}
	if len(got) != 2 || got[0].Symbol != "USDC" || got[0].Decimals != 6 || got[0].PriceUSD != "1.00" {
		t.Errorf("round-trip wrong: %+v", got)
	}
	if !gotAt.Equal(fetched) {
		t.Errorf("fetchedAt = %v, want %v", gotAt, fetched)
	}

	// Upsert replaces the blob for the same chain.
	if err := s.SaveTokenList(ctx, "ETH", tokens[:1], fetched); err != nil {
		t.Fatalf("SaveTokenList upsert: %v", err)
	}
	got, _, _, _ = s.LoadTokenList(ctx, "ETH")
	if len(got) != 1 {
		t.Errorf("upsert did not replace: got %d tokens, want 1", len(got))
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `WALLET_TEST_DATABASE_URL=postgres://wallet:wallet@localhost:5433/wallet go test ./internal/store/ -run TestSaveAndLoadTokenList -v`
(Start Postgres first with `docker compose up -d` if needed.)
Expected: FAIL (compile error: no `SaveTokenList`).

- [ ] **Step 4: Write the implementation**

In `internal/store/store.go`, add `"wallet-api/internal/lifi"` to the import block, then append:

```go
// SaveTokenList upserts the full LI.FI token list for a chain as one JSONB blob.
func (s *Postgres) SaveTokenList(ctx context.Context, chain string, tokens []lifi.ListToken, fetchedAt time.Time) error {
	payload, err := json.Marshal(tokens)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO lifi_token_lists (chain, payload, fetched_at)
		VALUES ($1,$2,$3)
		ON CONFLICT (chain) DO UPDATE SET payload=EXCLUDED.payload, fetched_at=EXCLUDED.fetched_at`,
		chain, payload, fetchedAt)
	return err
}

// LoadTokenList returns the stored token list for a chain, if present.
func (s *Postgres) LoadTokenList(ctx context.Context, chain string) ([]lifi.ListToken, time.Time, bool, error) {
	var payload []byte
	var fetchedAt time.Time
	err := s.pool.QueryRow(ctx, `SELECT payload, fetched_at FROM lifi_token_lists WHERE chain=$1`, chain).
		Scan(&payload, &fetchedAt)
	if err == pgx.ErrNoRows {
		return nil, time.Time{}, false, nil
	}
	if err != nil {
		return nil, time.Time{}, false, err
	}
	var tokens []lifi.ListToken
	if err := json.Unmarshal(payload, &tokens); err != nil {
		return nil, time.Time{}, false, err
	}
	return tokens, fetchedAt, true, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `WALLET_TEST_DATABASE_URL=postgres://wallet:wallet@localhost:5433/wallet go test ./internal/store/ -run TestSaveAndLoadTokenList -v`
Expected: PASS. Also run `go build ./...` to confirm the package compiles.

- [ ] **Step 6: Commit**

```bash
git add internal/store/schema.sql internal/store/store.go internal/store/store_test.go
git commit -m "feat(store): persist LI.FI token list as JSONB blob per chain

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 5: Redis token-list cache

**Files:**
- Create: `internal/rediscache/cache.go`
- Test: `internal/rediscache/cache_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: `lifi.ListToken` (Task 2).
- Produces (methods on `*rediscache.Cache`):
  - `func New(redisURL string, ttl time.Duration) (*Cache, error)`
  - `func (c *Cache) Ping(ctx context.Context) error`
  - `func (c *Cache) Close() error`
  - `func (c *Cache) SaveTokenList(ctx context.Context, chain string, tokens []lifi.ListToken, fetchedAt time.Time) error`
  - `func (c *Cache) LoadTokenList(ctx context.Context, chain string) ([]lifi.ListToken, time.Time, bool, error)`

- [ ] **Step 1: Add the go-redis dependency**

Run:
```bash
go get github.com/redis/go-redis/v9@v9.7.3
```
Expected: `go.mod` gains `github.com/redis/go-redis/v9 v9.7.3` and `go.sum` updates.

- [ ] **Step 2: Write the failing test**

Create `internal/rediscache/cache_test.go`:

```go
package rediscache

import (
	"context"
	"os"
	"testing"
	"time"

	"wallet-api/internal/lifi"
)

func newTestCache(t *testing.T) *Cache {
	t.Helper()
	url := os.Getenv("WALLET_TEST_REDIS_URL")
	if url == "" {
		t.Skip("set WALLET_TEST_REDIS_URL to run redis integration tests")
	}
	c, err := New(url, time.Hour)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	_, _ = c.client.Del(ctx, key("ETH")).Result()
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestRedisSaveAndLoadTokenList(t *testing.T) {
	c := newTestCache(t)
	ctx := context.Background()

	if _, _, ok, err := c.LoadTokenList(ctx, "ETH"); err != nil || ok {
		t.Fatalf("empty load: ok=%v err=%v", ok, err)
	}

	fetched := time.Now().UTC().Truncate(time.Second)
	tokens := []lifi.ListToken{{Address: "0xA0B8", Symbol: "USDC", Decimals: 6, PriceUSD: "1.00"}}
	if err := c.SaveTokenList(ctx, "ETH", tokens, fetched); err != nil {
		t.Fatalf("SaveTokenList: %v", err)
	}
	got, gotAt, ok, err := c.LoadTokenList(ctx, "ETH")
	if err != nil || !ok {
		t.Fatalf("LoadTokenList ok=%v err=%v", ok, err)
	}
	if len(got) != 1 || got[0].Symbol != "USDC" || got[0].PriceUSD != "1.00" {
		t.Errorf("round-trip wrong: %+v", got)
	}
	if !gotAt.Equal(fetched) {
		t.Errorf("fetchedAt = %v, want %v", gotAt, fetched)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/rediscache/ -v`
Expected: FAIL (compile error: no `New`/`key`/`Cache`).

- [ ] **Step 4: Write the implementation**

Create `internal/rediscache/cache.go`:

```go
// Package rediscache stores the LI.FI token list in Redis as one JSON blob per chain.
package rediscache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"wallet-api/internal/lifi"
)

// Cache is a Redis-backed token-list store.
type Cache struct {
	client *redis.Client
	ttl    time.Duration
}

// New parses a redis URL (redis://host:port/db) and builds a Cache.
func New(redisURL string, ttl time.Duration) (*Cache, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	return &Cache{client: redis.NewClient(opt), ttl: ttl}, nil
}

// Ping verifies connectivity.
func (c *Cache) Ping(ctx context.Context) error { return c.client.Ping(ctx).Err() }

// Close releases the client.
func (c *Cache) Close() error { return c.client.Close() }

func key(chain string) string { return "lifi:tokens:" + chain }

type payload struct {
	FetchedAt time.Time        `json:"fetchedAt"`
	Tokens    []lifi.ListToken `json:"tokens"`
}

// SaveTokenList writes the list with the configured safety TTL.
func (c *Cache) SaveTokenList(ctx context.Context, chain string, tokens []lifi.ListToken, fetchedAt time.Time) error {
	b, err := json.Marshal(payload{FetchedAt: fetchedAt, Tokens: tokens})
	if err != nil {
		return err
	}
	return c.client.Set(ctx, key(chain), b, c.ttl).Err()
}

// LoadTokenList returns the cached list, if present.
func (c *Cache) LoadTokenList(ctx context.Context, chain string) ([]lifi.ListToken, time.Time, bool, error) {
	b, err := c.client.Get(ctx, key(chain)).Bytes()
	if err == redis.Nil {
		return nil, time.Time{}, false, nil
	}
	if err != nil {
		return nil, time.Time{}, false, err
	}
	var p payload
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, time.Time{}, false, err
	}
	return p.Tokens, p.FetchedAt, true, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/rediscache/ -v` (skips without `WALLET_TEST_REDIS_URL`; with a local Redis: `WALLET_TEST_REDIS_URL=redis://localhost:6379/0 go test ./internal/rediscache/ -v`).
Expected: PASS or SKIP. Also run `go build ./...`.

- [ ] **Step 6: Commit**

```bash
git add internal/rediscache/ go.mod go.sum
git commit -m "feat(rediscache): add Redis token-list cache

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 6: Refresher (bootstrap + run)

**Files:**
- Create: `internal/tokenlist/refresher.go`
- Test: `internal/tokenlist/refresher_test.go`

**Interfaces:**
- Consumes: `lifi.ListToken` (Task 2), `*Holder`/`NewSnapshot` (Task 3).
- Produces:
  - `type LifiClient interface { GetTokens(ctx context.Context, chain string) ([]lifi.ListToken, error) }`
  - `type Store interface { SaveTokenList(...); LoadTokenList(...) }` (signatures matching Tasks 4 & 5).
  - `func NewRefresher(client LifiClient, redis, pg Store, holder *Holder, chain string, interval time.Duration) *Refresher`
  - `func (r *Refresher) Bootstrap(ctx context.Context) error`
  - `func (r *Refresher) Run(ctx context.Context)`
  - `func (r *Refresher) refresh(ctx context.Context)` (unexported; tested in-package)

- [ ] **Step 1: Write the failing test**

Create `internal/tokenlist/refresher_test.go`:

```go
package tokenlist

import (
	"context"
	"errors"
	"testing"
	"time"

	"wallet-api/internal/lifi"
)

type fakeLifi struct {
	tokens []lifi.ListToken
	err    error
	calls  int
}

func (f *fakeLifi) GetTokens(ctx context.Context, chain string) ([]lifi.ListToken, error) {
	f.calls++
	return f.tokens, f.err
}

type fakeStore struct {
	tokens    []lifi.ListToken
	fetchedAt time.Time
	present   bool
	saveCalls int
	saveErr   error
	loadErr   error
}

func (s *fakeStore) SaveTokenList(ctx context.Context, chain string, tokens []lifi.ListToken, fetchedAt time.Time) error {
	s.saveCalls++
	if s.saveErr != nil {
		return s.saveErr
	}
	s.tokens, s.fetchedAt, s.present = tokens, fetchedAt, true
	return nil
}

func (s *fakeStore) LoadTokenList(ctx context.Context, chain string) ([]lifi.ListToken, time.Time, bool, error) {
	if s.loadErr != nil {
		return nil, time.Time{}, false, s.loadErr
	}
	if !s.present {
		return nil, time.Time{}, false, nil
	}
	return s.tokens, s.fetchedAt, true, nil
}

var oneToken = []lifi.ListToken{{Address: "0xA0B8", Symbol: "USDC", Decimals: 6}}

func newRefresherForTest(l LifiClient, redis, pg Store, h *Holder) *Refresher {
	r := NewRefresher(l, redis, pg, h, "ETH", time.Hour)
	r.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	return r
}

func TestBootstrapFetchSuccessPersistsAndSets(t *testing.T) {
	l := &fakeLifi{tokens: oneToken}
	redis, pg := &fakeStore{}, &fakeStore{}
	var h Holder
	r := newRefresherForTest(l, redis, pg, &h)

	if err := r.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if redis.saveCalls != 1 || pg.saveCalls != 1 {
		t.Errorf("expected both stores written, redis=%d pg=%d", redis.saveCalls, pg.saveCalls)
	}
	if h.Current() == nil || h.Current().Count() != 1 {
		t.Error("snapshot not set from fetch")
	}
}

func TestBootstrapFallsBackToRedisThenPostgres(t *testing.T) {
	// Fetch fails, Redis has the list.
	l := &fakeLifi{err: errors.New("lifi down")}
	redis := &fakeStore{tokens: oneToken, fetchedAt: time.Now(), present: true}
	pg := &fakeStore{}
	var h Holder
	r := newRefresherForTest(l, redis, pg, &h)
	if err := r.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap (redis fallback): %v", err)
	}
	if h.Current() == nil || h.Current().Count() != 1 {
		t.Error("snapshot not set from redis fallback")
	}

	// Fetch fails, Redis empty, Postgres has the list.
	redis2, pg2 := &fakeStore{}, &fakeStore{tokens: oneToken, fetchedAt: time.Now(), present: true}
	var h2 Holder
	r2 := newRefresherForTest(&fakeLifi{err: errors.New("down")}, redis2, pg2, &h2)
	if err := r2.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap (pg fallback): %v", err)
	}
	if h2.Current() == nil || h2.Current().Count() != 1 {
		t.Error("snapshot not set from postgres fallback")
	}
}

func TestBootstrapErrorsWhenNothingAvailable(t *testing.T) {
	r := newRefresherForTest(&fakeLifi{err: errors.New("down")}, &fakeStore{}, &fakeStore{}, &Holder{})
	if err := r.Bootstrap(context.Background()); err == nil {
		t.Fatal("expected error when no source has a list")
	}
}

func TestRefreshSwapsSnapshotOnSuccess(t *testing.T) {
	l := &fakeLifi{tokens: oneToken}
	redis, pg := &fakeStore{}, &fakeStore{}
	var h Holder
	r := newRefresherForTest(l, redis, pg, &h)
	r.refresh(context.Background())
	if h.Current() == nil || h.Current().Count() != 1 {
		t.Error("refresh did not set snapshot")
	}
}

func TestRefreshRetainsPriorSnapshotOnError(t *testing.T) {
	var h Holder
	h.Set(NewSnapshot("ETH", oneToken, time.Now()))
	prior := h.Current()
	r := newRefresherForTest(&fakeLifi{err: errors.New("down")}, &fakeStore{}, &fakeStore{}, &h)
	r.refresh(context.Background())
	if h.Current() != prior {
		t.Error("failed refresh must keep the prior snapshot")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tokenlist/ -run 'TestBootstrap|TestRefresh' -v`
Expected: FAIL (compile error: no `NewRefresher`/`Refresher`).

- [ ] **Step 3: Write the implementation**

Create `internal/tokenlist/refresher.go`:

```go
package tokenlist

import (
	"context"
	"fmt"
	"log"
	"time"

	"wallet-api/internal/lifi"
)

// LifiClient fetches the token list for a chain.
type LifiClient interface {
	GetTokens(ctx context.Context, chain string) ([]lifi.ListToken, error)
}

// Store persists and loads a token list (satisfied by both Postgres and Redis).
type Store interface {
	SaveTokenList(ctx context.Context, chain string, tokens []lifi.ListToken, fetchedAt time.Time) error
	LoadTokenList(ctx context.Context, chain string) ([]lifi.ListToken, time.Time, bool, error)
}

// Refresher keeps the holder's snapshot current from LI.FI, persisting to Redis and Postgres.
type Refresher struct {
	client   LifiClient
	redis    Store
	pg       Store
	holder   *Holder
	chain    string
	interval time.Duration
	now      func() time.Time
	logf     func(string, ...any)
}

// NewRefresher builds a Refresher with a real clock and the standard logger.
func NewRefresher(client LifiClient, redis, pg Store, holder *Holder, chain string, interval time.Duration) *Refresher {
	return &Refresher{
		client: client, redis: redis, pg: pg, holder: holder,
		chain: chain, interval: interval, now: time.Now, logf: log.Printf,
	}
}

// Bootstrap loads an initial snapshot before serving: fetch LI.FI, else Redis,
// else Postgres. Returns an error only if no source has a list.
func (r *Refresher) Bootstrap(ctx context.Context) error {
	if tokens, err := r.client.GetTokens(ctx, r.chain); err == nil {
		now := r.now()
		r.persist(ctx, tokens, now)
		r.holder.Set(NewSnapshot(r.chain, tokens, now))
		return nil
	} else {
		r.logf("tokenlist: bootstrap fetch failed: %v", err)
	}
	if tokens, fetchedAt, ok, err := r.redis.LoadTokenList(ctx, r.chain); err == nil && ok {
		r.logf("tokenlist: bootstrapped from redis (%d tokens)", len(tokens))
		r.holder.Set(NewSnapshot(r.chain, tokens, fetchedAt))
		return nil
	}
	if tokens, fetchedAt, ok, err := r.pg.LoadTokenList(ctx, r.chain); err == nil && ok {
		r.logf("tokenlist: bootstrapped from postgres (%d tokens)", len(tokens))
		r.holder.Set(NewSnapshot(r.chain, tokens, fetchedAt))
		return nil
	}
	return fmt.Errorf("token list unavailable from lifi, redis, and postgres")
}

// Run refreshes on each tick until ctx is canceled.
func (r *Refresher) Run(ctx context.Context) {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.refresh(ctx)
		}
	}
}

// refresh fetches once and swaps the snapshot; on fetch error it keeps the prior one.
func (r *Refresher) refresh(ctx context.Context) {
	tokens, err := r.client.GetTokens(ctx, r.chain)
	if err != nil {
		r.logf("tokenlist: refresh failed, keeping prior snapshot: %v", err)
		return
	}
	now := r.now()
	r.persist(ctx, tokens, now)
	r.holder.Set(NewSnapshot(r.chain, tokens, now))
}

// persist writes to Postgres then Redis, logging (not failing) on either error.
func (r *Refresher) persist(ctx context.Context, tokens []lifi.ListToken, fetchedAt time.Time) {
	if err := r.pg.SaveTokenList(ctx, r.chain, tokens, fetchedAt); err != nil {
		r.logf("tokenlist: postgres persist failed: %v", err)
	}
	if err := r.redis.SaveTokenList(ctx, r.chain, tokens, fetchedAt); err != nil {
		r.logf("tokenlist: redis persist failed: %v", err)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tokenlist/ -v`
Expected: PASS (snapshot + refresher tests).

- [ ] **Step 5: Commit**

```bash
git add internal/tokenlist/refresher.go internal/tokenlist/refresher_test.go
git commit -m "feat(tokenlist): add refresher with bootstrap ladder and hourly run

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 7: Wallet service filter & enrich

**Files:**
- Modify: `internal/wallet/types.go`
- Modify: `internal/wallet/service.go`
- Modify: `internal/wallet/fakes_test.go`
- Modify: `internal/wallet/service_test.go`

**Interfaces:**
- Consumes: `lifi.ListToken` (Task 2); `*tokenlist.Holder` satisfies the new `wallet.Allowlist` at the wiring layer (Task 8).
- Produces:
  - `type Allowlist interface { LookupByAddress(addr string) (lifi.ListToken, bool); HasSymbol(sym string) bool }`
  - `Token` gains `LogoURI *string`, `CoinKey *string`, `PriceUSD *string`.
  - `func NewService(a AlchemyClient, ts TokenStore, tc TxCache, allow Allowlist, network string, ttl time.Duration) *Service`

- [ ] **Step 1: Update types (interface + Token fields)**

In `internal/wallet/types.go`, add `"wallet-api/internal/lifi"` to the import block. Add the new fields to `Token` (after `Price *Price`):

```go
	LogoURI  *string `json:"logoURI,omitempty"`
	CoinKey  *string `json:"coinKey,omitempty"`
	PriceUSD *string `json:"priceUSD,omitempty"`
```

Add the interface (after the `AlchemyClient` interface block):

```go
// Allowlist is the LI.FI token allowlist the service filters/enriches against.
type Allowlist interface {
	LookupByAddress(addr string) (lifi.ListToken, bool)
	HasSymbol(sym string) bool
}
```

- [ ] **Step 2: Write the failing tests**

Replace `internal/wallet/service_test.go` entirely with the version below. It adds a fake allowlist to every `NewService` call, replaces the two pointer-equality cache-hit assertions with content assertions (filtering now returns a fresh value), and adds filter/enrich/transaction-filter tests:

```go
package wallet

import (
	"context"
	"errors"
	"testing"
	"time"

	"wallet-api/internal/alchemy"
	"wallet-api/internal/lifi"
)

func usdc(addr string) *string { return &addr }

// allowUSDC permits the test USDC address (0xA0B8) and the ETH/USDC symbols.
func allowUSDC() *fakeAllowlist {
	return &fakeAllowlist{
		byAddr: map[string]lifi.ListToken{
			"0xa0b8": {Address: "0xA0B8", Symbol: "USDC", Name: "USD Coin", Decimals: 6, CoinKey: "USDC", LogoURI: "https://logo/usdc.png", PriceUSD: "1.0001"},
		},
		symbols: map[string]bool{"ETH": true, "USDC": true},
	}
}

func TestGetTokensCacheMissFetchesAndSaves(t *testing.T) {
	fa := &fakeAlchemy{tokens: []alchemy.Token{
		{TokenAddress: nil, Symbol: "ETH", Name: "Ethereum", Decimals: 18, RawBalance: "1500000000000000000",
			Price: &alchemy.Price{Currency: "usd", Value: "3200.50", LastUpdatedAt: "2026-06-23T00:00:00Z"}},
		{TokenAddress: usdc("0xA0B8"), Symbol: "USDC", Name: "USD Coin", Decimals: 6, RawBalance: "12500000"},
	}}
	ts := &fakeTokenStore{}
	svc := NewService(fa, ts, &fakeTxCache{}, allowUSDC(), "eth-mainnet", time.Minute)

	p, err := svc.GetTokens(context.Background(), "0xABC")
	if err != nil {
		t.Fatalf("GetTokens error: %v", err)
	}
	if fa.tokenCalls != 1 || ts.saveCalls != 1 {
		t.Errorf("expected 1 fetch + 1 save, got fetch=%d save=%d", fa.tokenCalls, ts.saveCalls)
	}
	// Raw (unfiltered) snapshot is what gets cached.
	if ts.saved == nil || len(ts.saved.Tokens) != 2 {
		t.Errorf("cache should store raw 2-token snapshot, got %+v", ts.saved)
	}
	if p.Address != "0xabc" || p.Network != "eth-mainnet" {
		t.Errorf("portfolio header wrong: %+v", p)
	}
	if len(p.Tokens) != 2 {
		t.Fatalf("got %d tokens, want 2 (ETH + allowlisted USDC)", len(p.Tokens))
	}
	if !p.Tokens[0].IsNative || p.Tokens[0].Balance != "1.5" {
		t.Errorf("native token normalization wrong: %+v", p.Tokens[0])
	}
	// USDC enriched from the allowlist.
	u := p.Tokens[1]
	if u.IsNative || u.Balance != "12.5" {
		t.Errorf("erc20 normalization wrong: %+v", u)
	}
	if u.LogoURI == nil || *u.LogoURI != "https://logo/usdc.png" {
		t.Errorf("USDC LogoURI not enriched: %+v", u.LogoURI)
	}
	if u.PriceUSD == nil || *u.PriceUSD != "1.0001" {
		t.Errorf("USDC PriceUSD not enriched: %+v", u.PriceUSD)
	}
	if u.CoinKey == nil || *u.CoinKey != "USDC" {
		t.Errorf("USDC CoinKey not enriched: %+v", u.CoinKey)
	}
}

func TestGetTokensDropsUnknownTokens(t *testing.T) {
	fa := &fakeAlchemy{tokens: []alchemy.Token{
		{TokenAddress: nil, Symbol: "ETH", Decimals: 18, RawBalance: "1000000000000000000"},
		{TokenAddress: usdc("0xA0B8"), Symbol: "USDC", Decimals: 6, RawBalance: "12500000"},
		{TokenAddress: usdc("0xSPAM"), Symbol: "SCAM", Decimals: 18, RawBalance: "999"},
	}}
	svc := NewService(fa, &fakeTokenStore{}, &fakeTxCache{}, allowUSDC(), "eth-mainnet", time.Minute)
	p, err := svc.GetTokens(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Tokens) != 2 {
		t.Fatalf("expected ETH + USDC only, got %d: %+v", len(p.Tokens), p.Tokens)
	}
	for _, tok := range p.Tokens {
		if tok.Symbol == "SCAM" {
			t.Error("unknown SCAM token should have been dropped")
		}
	}
}

func TestGetTokensRescalesBalanceOnDecimalsOverride(t *testing.T) {
	// Alchemy reports decimals=18 for an address the allowlist says is 6 decimals.
	fa := &fakeAlchemy{tokens: []alchemy.Token{
		{TokenAddress: usdc("0xA0B8"), Symbol: "usdc", Name: "wrong", Decimals: 18, RawBalance: "12500000"},
	}}
	svc := NewService(fa, &fakeTokenStore{}, &fakeTxCache{}, allowUSDC(), "eth-mainnet", time.Minute)
	p, err := svc.GetTokens(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Tokens) != 1 {
		t.Fatalf("got %d tokens, want 1", len(p.Tokens))
	}
	tok := p.Tokens[0]
	if tok.Decimals != 6 {
		t.Errorf("decimals = %d, want 6 (overridden)", tok.Decimals)
	}
	if tok.Balance != "12.5" {
		t.Errorf("balance = %q, want 12.5 (re-scaled with 6 decimals)", tok.Balance)
	}
	if tok.Symbol != "USDC" || tok.Name != "USD Coin" {
		t.Errorf("symbol/name not overridden from allowlist: %+v", tok)
	}
}

func TestGetTokensCacheHitFiltersCachedSnapshot(t *testing.T) {
	cached := &TokenPortfolio{Address: "0xabc", Network: "eth-mainnet", Tokens: []Token{
		{TokenAddress: nil, Symbol: "ETH", Decimals: 18, RawBalance: "0", Balance: "0", IsNative: true},
		{TokenAddress: usdc("0xSPAM"), Symbol: "SCAM", Decimals: 18, RawBalance: "1", Balance: "0"},
	}}
	fa := &fakeAlchemy{}
	ts := &fakeTokenStore{saved: cached, fresh: true}
	svc := NewService(fa, ts, &fakeTxCache{}, allowUSDC(), "eth-mainnet", time.Minute)

	p, err := svc.GetTokens(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if fa.tokenCalls != 0 {
		t.Errorf("cache hit should not call alchemy, got %d calls", fa.tokenCalls)
	}
	if len(p.Tokens) != 1 || !p.Tokens[0].IsNative {
		t.Errorf("cache-hit filtering wrong: want only native ETH, got %+v", p.Tokens)
	}
}

func TestGetTokensWrapsUpstreamError(t *testing.T) {
	fa := &fakeAlchemy{err: context.DeadlineExceeded}
	svc := NewService(fa, &fakeTokenStore{}, &fakeTxCache{}, allowUSDC(), "eth-mainnet", time.Minute)
	_, err := svc.GetTokens(context.Background(), "0xABC")
	if err == nil || !errorsIs(err, ErrUpstream) {
		t.Errorf("expected ErrUpstream, got %v", err)
	}
}

func TestGetTokensWrapsSaveError(t *testing.T) {
	fa := &fakeAlchemy{tokens: []alchemy.Token{
		{TokenAddress: nil, Symbol: "ETH", Decimals: 18, RawBalance: "1500000000000000000"},
	}}
	ts := &fakeTokenStore{saveErr: errors.New("db down")}
	svc := NewService(fa, ts, &fakeTxCache{}, allowUSDC(), "eth-mainnet", time.Minute)

	_, err := svc.GetTokens(context.Background(), "0xABC")
	if err == nil || !errorsIs(err, ErrStore) {
		t.Errorf("expected ErrStore from SaveTokens failure, got %v", err)
	}
}

func errorsIs(err, target error) bool { return errors.Is(err, target) }

func TestGetTransactionsCacheMissFiltersAndSaves(t *testing.T) {
	fa := &fakeAlchemy{transfers: alchemy.TransfersResult{Transfers: []alchemy.Transfer{
		{Hash: "0x1", From: "0xabc", To: "0xdef", Asset: "ETH", Value: "0.5", BlockNum: "0x20", Category: "external"},
		{Hash: "0x2", From: "0xabc", To: "0xdef", Asset: "USDC", Value: "10", BlockNum: "0x21", Category: "erc20"},
		{Hash: "0x3", From: "0xabc", To: "0xdef", Asset: "SCAM", Value: "999", BlockNum: "0x22", Category: "erc20"},
	}}}
	tc := &fakeTxCache{}
	svc := NewService(fa, &fakeTokenStore{}, tc, allowUSDC(), "eth-mainnet", time.Minute)

	page, err := svc.GetTransactions(context.Background(), "0xABC", 25, "")
	if err != nil {
		t.Fatal(err)
	}
	if fa.txCalls != 1 || tc.saveCalls != 1 {
		t.Errorf("expected 1 fetch + 1 save, got fetch=%d save=%d", fa.txCalls, tc.saveCalls)
	}
	if len(page.Transfers) != 2 {
		t.Fatalf("expected ETH+USDC kept, SCAM dropped; got %+v", page.Transfers)
	}
	for _, tr := range page.Transfers {
		if tr.Asset == "SCAM" {
			t.Error("SCAM transfer should be filtered out")
		}
	}
}

func TestGetTransactionsCacheHitFilters(t *testing.T) {
	cached := &TransactionPage{Address: "0xabc", Transfers: []Transfer{
		{Hash: "0xkeep", Asset: "ETH"},
		{Hash: "0xdrop", Asset: "SCAM"},
	}}
	fa := &fakeAlchemy{}
	tc := &fakeTxCache{saved: cached, fresh: true}
	svc := NewService(fa, &fakeTokenStore{}, tc, allowUSDC(), "eth-mainnet", time.Minute)

	page, err := svc.GetTransactions(context.Background(), "0xABC", 25, "")
	if err != nil {
		t.Fatal(err)
	}
	if fa.txCalls != 0 {
		t.Errorf("expected cache hit, got fetch=%d", fa.txCalls)
	}
	if len(page.Transfers) != 1 || page.Transfers[0].Hash != "0xkeep" {
		t.Errorf("cache-hit transfer filtering wrong: %+v", page.Transfers)
	}
}

func TestGetTransactionsPageKeyBypassesCache(t *testing.T) {
	fa := &fakeAlchemy{transfers: alchemy.TransfersResult{Transfers: []alchemy.Transfer{{Hash: "0x2", Asset: "ETH"}}}}
	tc := &fakeTxCache{saved: &TransactionPage{Transfers: []Transfer{{Hash: "0xcached", Asset: "ETH"}}}, fresh: true}
	svc := NewService(fa, &fakeTokenStore{}, tc, allowUSDC(), "eth-mainnet", time.Minute)

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
		t.Errorf("expected fresh filtered page, got %+v", page)
	}
}
```

- [ ] **Step 3: Add the fake allowlist**

Append to `internal/wallet/fakes_test.go`. Add `"strings"` and `"wallet-api/internal/lifi"` to its import block, then:

```go
// fakeAllowlist is an in-memory Allowlist for tests.
type fakeAllowlist struct {
	byAddr  map[string]lifi.ListToken
	symbols map[string]bool
}

func (f *fakeAllowlist) LookupByAddress(addr string) (lifi.ListToken, bool) {
	t, ok := f.byAddr[strings.ToLower(addr)]
	return t, ok
}

func (f *fakeAllowlist) HasSymbol(sym string) bool {
	return f.symbols[strings.ToUpper(sym)]
}
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `go test ./internal/wallet/ -v`
Expected: FAIL (compile error: `NewService` takes 5 args / `Token` has no `LogoURI`).

- [ ] **Step 5: Implement service changes**

In `internal/wallet/service.go`, add `"strings"` and `"wallet-api/internal/lifi"` to the import block. Add `allow Allowlist` to the `Service` struct (after `txs TxCache`):

```go
	allow   Allowlist
```

Update `NewService`:

```go
// NewService builds a Service with a real-time clock.
func NewService(a AlchemyClient, ts TokenStore, tc TxCache, allow Allowlist, network string, ttl time.Duration) *Service {
	return &Service{alchemy: a, tokens: ts, txs: tc, allow: allow, network: network, ttl: ttl, now: time.Now}
}
```

In `GetTokens`, wrap both return paths with the filter. Change the cache-hit return:

```go
	} else if ok {
		return s.filterTokens(p), nil
	}
```

and the final return:

```go
	return s.filterTokens(p), nil
```

In `GetTransactions`, wrap all returned pages. Change the cache-hit return:

```go
		} else if ok {
			return s.filterTransfers(p), nil
		}
```

and the final return:

```go
	return s.filterTransfers(page), nil
```

Append the helpers at the end of `service.go`:

```go
// filterTokens drops non-allowlisted ERC-20s and enriches survivors. Native
// tokens are always kept. A fresh portfolio is returned (the cached/raw one is
// left untouched).
func (s *Service) filterTokens(p *TokenPortfolio) *TokenPortfolio {
	out := &TokenPortfolio{Address: p.Address, Network: p.Network, FetchedAt: p.FetchedAt}
	out.Tokens = make([]Token, 0, len(p.Tokens))
	for _, t := range p.Tokens {
		if t.IsNative || t.TokenAddress == nil {
			out.Tokens = append(out.Tokens, t)
			continue
		}
		lt, ok := s.allow.LookupByAddress(*t.TokenAddress)
		if !ok {
			continue
		}
		out.Tokens = append(out.Tokens, enrichToken(t, lt))
	}
	return out
}

// enrichToken overlays LI.FI metadata onto t. When decimals change, Balance is
// re-derived from RawBalance so the scaled value stays correct.
func enrichToken(t Token, lt lifi.ListToken) Token {
	if lt.Symbol != "" {
		t.Symbol = lt.Symbol
	}
	if lt.Name != "" {
		t.Name = lt.Name
	}
	if lt.LogoURI != "" {
		t.LogoURI = strptr(lt.LogoURI)
	}
	if lt.CoinKey != "" {
		t.CoinKey = strptr(lt.CoinKey)
	}
	if lt.PriceUSD != "" {
		t.PriceUSD = strptr(lt.PriceUSD)
	}
	if lt.Decimals > 0 && lt.Decimals != t.Decimals {
		if _, scaled, err := ScaleBalance(t.RawBalance, lt.Decimals); err == nil {
			t.Balance = scaled
		}
		t.Decimals = lt.Decimals
	}
	return t
}

// filterTransfers keeps native ETH and transfers whose asset symbol is in the
// allowlist; everything else is dropped (best-effort symbol match).
func (s *Service) filterTransfers(page *TransactionPage) *TransactionPage {
	out := &TransactionPage{Address: page.Address, NextPageKey: page.NextPageKey}
	out.Transfers = make([]Transfer, 0, len(page.Transfers))
	for _, t := range page.Transfers {
		if strings.EqualFold(t.Asset, "ETH") || s.allow.HasSymbol(t.Asset) {
			out.Transfers = append(out.Transfers, t)
		}
	}
	return out
}

func strptr(s string) *string { return &s }
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/wallet/ -v`
Expected: PASS (all wallet tests, including new filter/enrich/rescale cases).

- [ ] **Step 7: Commit**

```bash
git add internal/wallet/
git commit -m "feat(wallet): filter responses to LI.FI allowlist and enrich tokens

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 8: Wire it all together

**Files:**
- Modify: `cmd/server/main.go`
- Modify: `docker-compose.yml`
- Modify: `README.md`

**Interfaces:**
- Consumes: everything from Tasks 1–7. `*tokenlist.Holder` satisfies `wallet.Allowlist`; `*store.Postgres` and `*rediscache.Cache` satisfy `tokenlist.Store`; `*lifi.Client` satisfies `tokenlist.LifiClient`.
- Produces: a running server that bootstraps the allowlist before listening and refreshes it hourly.

- [ ] **Step 1: Add Redis to docker-compose**

In `docker-compose.yml`, add under `services:` (sibling of `postgres`):

```yaml
  redis:
    image: redis:7
    ports:
      - "6379:6379"
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 2s
      timeout: 3s
      retries: 10
```

- [ ] **Step 2: Rewrite main.go wiring**

Replace `cmd/server/main.go` with:

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
	"wallet-api/internal/lifi"
	"wallet-api/internal/rediscache"
	"wallet-api/internal/store"
	"wallet-api/internal/tokenlist"
	"wallet-api/internal/wallet"
)

// redisTokenListTTL is the safety TTL for the cached list; longer than the
// refresh interval so a present-but-stale list survives refresher hiccups.
const redisTokenListTTL = 24 * time.Hour

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	setupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pg, err := store.New(setupCtx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer pg.Close()
	if err := pg.Migrate(setupCtx); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	redisCache, err := rediscache.New(cfg.RedisURL, redisTokenListTTL)
	if err != nil {
		log.Fatalf("redis: %v", err)
	}
	defer redisCache.Close()
	if err := redisCache.Ping(setupCtx); err != nil {
		log.Fatalf("redis ping: %v", err)
	}

	lifiClient := lifi.New(cfg.LifiTokensURL)
	holder := &tokenlist.Holder{}
	refresher := tokenlist.NewRefresher(lifiClient, redisCache, pg, holder, cfg.LifiChain, cfg.LifiRefresh)
	if err := refresher.Bootstrap(setupCtx); err != nil {
		log.Fatalf("token list bootstrap: %v", err)
	}
	go refresher.Run(context.Background())
	log.Printf("token list ready: %d tokens (chain=%s, refresh=%s)", holder.Current().Count(), cfg.LifiChain, cfg.LifiRefresh)

	ac := alchemy.New(cfg.AlchemyAPIKey, cfg.AlchemyNetwork)
	svc := wallet.NewService(ac, pg, pg, holder, cfg.AlchemyNetwork, cfg.CacheTTL)
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

- [ ] **Step 3: Build and run the full test suite**

Run:
```bash
go build ./...
go vet ./...
go test ./...
```
Expected: build succeeds; `go vet` clean; tests PASS (store/rediscache tests SKIP without their env vars).

- [ ] **Step 4: Update the README**

In `README.md`, add to the Configuration table (after `CACHE_TTL_SECONDS`):

```
| `REDIS_URL` | yes | — | Redis connection string, e.g. `redis://localhost:6379/0` |
| `LIFI_TOKENS_URL` | no | `https://li.quest/v1/tokens` | LI.FI token-list endpoint |
| `LIFI_CHAIN` | no | `ETH` | LI.FI chain key for the allowlist |
| `LIFI_REFRESH_SECONDS` | no | `3600` | Allowlist refresh interval (positive integer) |
```

In the "Run locally" block, change `docker compose up -d` so the comment reads `# start Postgres (5433) + Redis (6379)`, add `export REDIS_URL=redis://localhost:6379/0` next to the other exports, and add a sentence after the table:

> Responses are filtered to the LI.FI token allowlist: unrecognized ERC-20s are hidden and recognized tokens are enriched with `logoURI`, `coinKey`, and `priceUSD`. The allowlist is fetched at startup and refreshed hourly.

In the Layout block, add these lines:

```
internal/lifi       LI.FI token-list client
internal/tokenlist  allowlist snapshot + hourly refresher
internal/rediscache Redis token-list cache
```

- [ ] **Step 5: Commit**

```bash
git add cmd/server/main.go docker-compose.yml README.md
git commit -m "feat: wire LI.FI allowlist refresher into the server

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Final verification

- [ ] Run the complete suite: `go build ./... && go vet ./... && go test ./...` — all PASS/SKIP.
- [ ] Optional end-to-end with infra:
  ```bash
  docker compose up -d
  export ALCHEMY_API_KEY=... DATABASE_URL=postgres://wallet:wallet@localhost:5433/wallet REDIS_URL=redis://localhost:6379/0
  go run ./cmd/server
  # In another shell:
  curl 'http://localhost:8080/v1/addresses/0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045/tokens'
  ```
  Expected: server logs "token list ready: N tokens"; the `/tokens` response contains only allowlisted tokens, enriched with `logoURI`/`coinKey`/`priceUSD`.
- [ ] Optional infra-backed unit tests:
  ```bash
  WALLET_TEST_DATABASE_URL=postgres://wallet:wallet@localhost:5433/wallet WALLET_TEST_REDIS_URL=redis://localhost:6379/0 go test ./...
  ```

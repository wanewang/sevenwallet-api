# CoinGecko Token Market Data Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add shared, Redis-first CoinGecko price, 24-hour change, market-cap, and update-time enrichment to wallet tokens without making CoinGecko a required dependency.

**Architecture:** Persist CoinGecko's multi-platform coin mapping as normalized PostgreSQL rows and serve request-time ID resolution from an immutable in-memory index. Cache one market record per chain/token in Redis and PostgreSQL, fetch stale or missing IDs in batched CoinGecko calls under one enrichment deadline, and overlay fresh values after existing LI.FI/Moralis filtering.

**Tech Stack:** Go 1.25, `net/http`, `encoding/json`, `pgx/v5`, `go-redis/v9`, PostgreSQL 16, Redis 7, Swaggo/OpenAPI 2.0.

## Global Constraints

- Preserve the user's current edits to `docs/superpowers/specs/2026-07-23-coingecko-token-market-data-design.md`; they are the source of truth for this plan.
- This is a public repository: never add `Codex-Session:` or a `https://Codex.ai/code/session_...` URL to a commit message.
- Do not add a CoinGecko API key or a new Go dependency.
- Send `Accept: application/json` and the configured `User-Agent` on every CoinGecko request.
- Coin-list refresh defaults to `21600` seconds and atomically preserves the last usable snapshot on every failure.
- Market freshness defaults to `1800` seconds and is measured from the original CoinGecko `fetched_at`; promoting a PostgreSQL record to Redis never resets freshness.
- Concurrent market-data writes are monotonic by `fetched_at`: an older fetch must never replace a newer PostgreSQL or Redis record, even when its write completes later.
- Request-time enrichment defaults to one total five-second budget across Redis, PostgreSQL, all price batches, retry waits, and cache writes.
- Each `/simple/price` batch gets one initial attempt plus three retries, with one context-aware one-second delay before each retry.
- Retry network/read/decode errors, HTTP 429, and HTTP 5xx; do not retry other HTTP 4xx responses.
- Fresh CoinGecko USD price overwrites both `Token.Price` and `Token.PriceUSD`; stale CoinGecko price never overwrites them.
- A stale market record may populate only change, market cap, and market timestamp, and only when its stored CoinGecko ID matches the current catalog mapping.
- Redis caches shared market records only; it does not cache the CoinGecko catalog or complete wallet portfolios.
- CoinGecko-specific failures are logged and never introduce a new HTTP error response.
- Follow TDD for every task and keep the repository compiling at each commit boundary.

---

## File Structure

### New files

- `internal/coingecko/types.go` — CoinGecko wire types and retryable HTTP error type.
- `internal/coingecko/client.go` — `/coins/list` and retrying `/simple/price` HTTP client.
- `internal/coingecko/client_test.go` — request construction, parsing, retry, and cancellation tests.
- `internal/marketdata/catalog.go` — normalized mapping rows, immutable indexes, ambiguity handling, and atomic holder.
- `internal/marketdata/catalog_test.go` — platform expansion, native resolution, normalization, and ambiguity tests.
- `internal/marketdata/refresher.go` — non-fatal bootstrap and six-hour catalog refresh loop.
- `internal/marketdata/refresher_test.go` — fetch/store fallback and atomic-swap tests.
- `internal/marketdata/record.go` — shared market identity, record, freshness, and Redis-write types.
- `internal/marketdata/record_test.go` — key normalization and configurable-freshness tests.
- `internal/marketdata/service.go` — Redis-first/PG-second enrichment, batching, deadline, persistence, and merge precedence.
- `internal/marketdata/service_test.go` — cache hierarchy, overwrite/stale rules, batching, deadline, and failure tests.

### Modified files

- `internal/config/config.go` and `config_test.go` — CoinGecko settings and validation.
- `internal/store/schema.sql`, `store.go`, and `store_test.go` — catalog and market persistence.
- `internal/rediscache/cache.go` and `cache_test.go` — market `MGET` and per-record TTL pipeline.
- `internal/wallet/types.go`, `service.go`, `fakes_test.go`, and `service_test.go` — public fields and enrichment hook.
- `internal/api/handlers.go`, `handlers_test.go`, and `openapi_test.go` — API descriptions and schema assertions.
- `cmd/server/main.go` — optional CoinGecko subsystem wiring.
- `README.md` — behavior, configuration, and package layout.
- `internal/apidocs/swagger.json`, `internal/apidocs/swagger.yaml`, and `docs/api/openapi.json` — generated documentation.

---

### Task 1: Add validated CoinGecko configuration

**Files:**
- Modify: `internal/config/config.go:10-108`
- Modify: `internal/config/config_test.go:8-140`

**Interfaces:**
- Consumes: existing `loadFrom(func(string) string) (Config, error)`.
- Produces: `Config.CoinGeckoBaseURL string`, `CoinGeckoUserAgent string`, `CoinGeckoPlatform string`, `CoinGeckoListRefresh time.Duration`, `CoinGeckoMarketTTL time.Duration`, `CoinGeckoEnrichTimeout time.Duration`, and `CoinGeckoNativeIDs []string`.

- [ ] **Step 1: Write failing default and override tests**

Add focused tests while preserving the existing required environment setup:

```go
func coinGeckoTestEnv() map[string]string {
	return map[string]string{
		"ALCHEMY_API_KEY": "key123",
		"DATABASE_URL":    "postgres://db",
		"REDIS_URL":       "redis://localhost:6379/0",
		"MORALIS_API_KEY": "mkey",
	}
}

func TestLoadFromAppliesCoinGeckoDefaults(t *testing.T) {
	env := coinGeckoTestEnv()
	cfg, err := loadFrom(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.CoinGeckoBaseURL != "https://api.coingecko.com/api/v3" {
		t.Errorf("base URL = %q", cfg.CoinGeckoBaseURL)
	}
	if cfg.CoinGeckoUserAgent != "wallet-api/1.0" || cfg.CoinGeckoPlatform != "ethereum" {
		t.Errorf("identity defaults = %+v", cfg)
	}
	if cfg.CoinGeckoListRefresh != 6*time.Hour || cfg.CoinGeckoMarketTTL != 30*time.Minute {
		t.Errorf("duration defaults = %+v", cfg)
	}
	if cfg.CoinGeckoEnrichTimeout != 5*time.Second {
		t.Errorf("enrichment timeout = %v", cfg.CoinGeckoEnrichTimeout)
	}
	if !reflect.DeepEqual(cfg.CoinGeckoNativeIDs, []string{"ethereum"}) {
		t.Errorf("native IDs = %#v", cfg.CoinGeckoNativeIDs)
	}
}

func TestLoadFromHonoursCoinGeckoOverrides(t *testing.T) {
	env := coinGeckoTestEnv()
	env["COINGECKO_BASE_URL"] = "http://coingecko.test/api/v3"
	env["COINGECKO_USER_AGENT"] = "wallet-api-test/2.0"
	env["COINGECKO_PLATFORM"] = "base"
	env["COINGECKO_LIST_REFRESH_SECONDS"] = "60"
	env["COINGECKO_MARKET_TTL_SECONDS"] = "90"
	env["COINGECKO_ENRICH_TIMEOUT_SECONDS"] = "7"
	env["COINGECKO_NATIVE_IDS"] = " ethereum,wrapped-ether,ethereum "
	cfg, err := loadFrom(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.CoinGeckoBaseURL != "http://coingecko.test/api/v3" || cfg.CoinGeckoPlatform != "base" {
		t.Errorf("string overrides = %+v", cfg)
	}
	if cfg.CoinGeckoListRefresh != time.Minute || cfg.CoinGeckoMarketTTL != 90*time.Second || cfg.CoinGeckoEnrichTimeout != 7*time.Second {
		t.Errorf("duration overrides = %+v", cfg)
	}
	if !reflect.DeepEqual(cfg.CoinGeckoNativeIDs, []string{"ethereum", "wrapped-ether"}) {
		t.Errorf("native IDs = %#v", cfg.CoinGeckoNativeIDs)
	}
}
```

Also add table-driven cases that independently supply `"0"`, `"-1"`, and `"abc"` for each duration variable and assert the error names that variable. Add a case for `COINGECKO_NATIVE_IDS=" , "` returning an error.

- [ ] **Step 2: Run the focused tests and verify the red state**

Run:

```bash
go test ./internal/config -run 'TestLoadFrom.*CoinGecko' -v
```

Expected: build failure because the `Config.CoinGecko*` fields do not exist.

- [ ] **Step 3: Implement parsing and defaults**

Add these fields and helpers; import `strings` in `config.go` and `reflect` in its test:

```go
CoinGeckoBaseURL       string
CoinGeckoUserAgent     string
CoinGeckoPlatform      string
CoinGeckoListRefresh   time.Duration
CoinGeckoMarketTTL     time.Duration
CoinGeckoEnrichTimeout time.Duration
CoinGeckoNativeIDs     []string

func positiveSeconds(getenv func(string) string, key string, fallback int) (time.Duration, error) {
	raw := getenv(key)
	if raw == "" {
		return time.Duration(fallback) * time.Second, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", key, raw)
	}
	return time.Duration(n) * time.Second, nil
}

func uniqueCSV(raw string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, part := range strings.Split(raw, ",") {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
```

Apply the exact defaults `https://api.coingecko.com/api/v3`, `wallet-api/1.0`, `ethereum`, `21600`, `1800`, `5`, and native IDs `ethereum`. Parse all three durations through `positiveSeconds`; trim/deduplicate native IDs through `uniqueCSV`; reject an empty result.

- [ ] **Step 4: Run configuration tests**

Run:

```bash
go test ./internal/config -v
```

Expected: all configuration tests pass.

- [ ] **Step 5: Commit the configuration slice**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add CoinGecko market settings"
```

---

### Task 2: Build the CoinGecko HTTP client and retry policy

**Files:**
- Create: `internal/coingecko/types.go`
- Create: `internal/coingecko/client.go`
- Create: `internal/coingecko/client_test.go`

**Interfaces:**
- Consumes: base URL and user agent from Task 1.
- Produces: `coingecko.New`, `Client.ListCoins`, and `Client.GetPrices`.

- [ ] **Step 1: Write failing request and retry tests**

Use `httptest.Server` to assert the list path/query/header and exact four-attempt price behavior:

```go
func TestListCoinsSendsPlatformFlagAndUserAgent(t *testing.T) {
	var gotQuery, gotAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("include_platform")
		gotAgent = r.Header.Get("User-Agent")
		_, _ = io.WriteString(w, `[{"id":"usd-coin","symbol":"usdc","name":"USDC","platforms":{"ethereum":"0xA0B8"}}]`)
	}))
	defer srv.Close()
	c := New(srv.URL, "wallet-api-test/1.0")
	coins, err := c.ListCoins(context.Background())
	if err != nil {
		t.Fatalf("ListCoins: %v", err)
	}
	if gotQuery != "true" || gotAgent != "wallet-api-test/1.0" || len(coins) != 1 {
		t.Fatalf("query=%q agent=%q coins=%+v", gotQuery, gotAgent, coins)
	}
}

func TestGetPricesRetriesRetryableFailures(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 4 {
			http.Error(w, "busy", http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, `{"ethereum":{"usd":3210.45,"usd_market_cap":387000000000,"usd_24h_change":-1.23,"last_updated_at":1784781600}}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "ua")
	var waits []time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	prices, err := c.GetPrices(context.Background(), []string{"ethereum"})
	if err != nil {
		t.Fatalf("GetPrices: %v", err)
	}
	if calls != 4 || !reflect.DeepEqual(waits, []time.Duration{time.Second, time.Second, time.Second}) || prices["ethereum"].USD.String() != "3210.45" {
		t.Fatalf("calls=%d waits=%v prices=%+v", calls, waits, prices)
	}
}
```

Add explicit tests for the exact flags `include_24hr_change`, `include_last_updated_at`, and `include_market_cap`, plus HTTP 500, connection errors, success-body read errors from a custom failing `io.ReadCloser`, decode errors, terminal HTTP 400, canceled retry sleep, and nullable response fields.

- [ ] **Step 2: Run the new package tests and verify they fail**

```bash
go test ./internal/coingecko -v
```

Expected: build failure because the package does not exist.

- [ ] **Step 3: Define wire types and status errors**

Create `types.go`:

```go
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
```

- [ ] **Step 4: Implement request construction and fixed retries**

Create `client.go` with a five-second HTTP timeout, context-aware sleeper, `maxPriceAttempts=4`, and `retryDelay=time.Second`. `ListCoins` must call `/coins/list?include_platform=true`. `GetPrices` must call `/simple/price` with `ids=strings.Join(ids, ",")`, `vs_currencies=usd`, and all three include flags.

Use this retry loop and classification:

```go
func (c *Client) GetPrices(ctx context.Context, ids []string) (map[string]SimplePrice, error) {
	if len(ids) == 0 {
		return map[string]SimplePrice{}, nil
	}
	var lastErr error
	for attempt := 1; attempt <= maxPriceAttempts; attempt++ {
		prices, err := c.getPricesOnce(ctx, ids)
		if err == nil {
			return prices, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt == maxPriceAttempts || !retryable(err) {
			return nil, err
		}
		if err := c.sleep(ctx, retryDelay); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func retryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var statusErr *StatusError
	if errors.As(err, &statusErr) {
		return statusErr.StatusCode == http.StatusTooManyRequests || statusErr.StatusCode >= 500
	}
	return true
}
```

The shared request builder must set `Accept` and `User-Agent`. Drain non-2xx bodies, retry success-body decode failures, and wrap errors with the operation name.

- [ ] **Step 5: Run client and repository tests**

```bash
go test ./internal/coingecko -v
go test ./...
```

Expected: all tests pass; integration packages may skip without test URLs.

- [ ] **Step 6: Commit the CoinGecko client**

```bash
git add internal/coingecko
git commit -m "feat(coingecko): add catalog and price client"
```

---

### Task 3: Build normalized catalog rows and immutable lookup indexes

**Files:**
- Create: `internal/marketdata/catalog.go`
- Create: `internal/marketdata/catalog_test.go`

**Interfaces:**
- Consumes: `[]coingecko.Coin` from Task 2.
- Produces: `CoinMapping`, `BuildMappings`, `NewCatalog`, `Catalog.ResolveContract`, `Catalog.ResolveNative`, and atomic `Holder` accessors.

- [ ] **Step 1: Write failing transformation and lookup tests**

Use this multi-platform/native fixture:

```go
func TestBuildMappingsExpandsPlatformsAndNativeCoins(t *testing.T) {
	fetchedAt := time.Unix(1_700_000_000, 0).UTC()
	coins := []coingecko.Coin{
		{ID: "usd-coin", Name: "USDC", Symbol: "usdc", Platforms: map[string]string{
			"ethereum": "0xA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48",
			"solana":   "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v",
		}},
		{ID: "ethereum", Name: "Ethereum", Symbol: "ETH", Platforms: map[string]string{}},
	}
	got := BuildMappings(coins, fetchedAt)
	want := []CoinMapping{
		{ID: "ethereum", Name: "Ethereum", Symbol: "ETH", Chain: "eth", Address: NativeAddress, FetchedAt: fetchedAt},
		{ID: "usd-coin", Name: "USDC", Symbol: "usdc", Chain: "ethereum", Address: "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48", FetchedAt: fetchedAt},
		{ID: "usd-coin", Name: "USDC", Symbol: "usdc", Chain: "solana", Address: "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v", FetchedAt: fetchedAt},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mappings = %#v, want %#v", got, want)
	}
}
```

Add named tests proving blank platform entries are ignored; a coin with no usable platform becomes native; Ethereum lookup is case-insensitive; Solana remains case-sensitive; duplicate contract IDs are ambiguous; native matching filters by allowed IDs; and a nil holder returns misses without panicking.

- [ ] **Step 2: Run tests and verify the red state**

```bash
go test ./internal/marketdata -run 'TestBuildMappings|TestCatalog|TestHolder' -v
```

Expected: build failure because the market-data catalog does not exist.

- [ ] **Step 3: Implement normalization and deterministic rows**

Create `catalog.go` with:

```go
package marketdata

import (
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"wallet-api/internal/coingecko"
)

const NativeAddress = "native"

type CoinMapping struct {
	ID        string
	Name      string
	Symbol    string
	Chain     string
	Address   string
	FetchedAt time.Time
}

func normalizePlatformAddress(address string) string {
	address = strings.TrimSpace(address)
	if strings.HasPrefix(strings.ToLower(address), "0x") {
		return strings.ToLower(address)
	}
	return address
}
```

`BuildMappings` must trim coin text, lowercase platform keys, skip blank platform/address pairs, create one symbol/native row only when no usable platform row exists, and sort by ID, chain, then address. This produces stable test and PostgreSQL ordering.

- [ ] **Step 4: Implement immutable indexes and ambiguity checks**

Use slices so duplicates remain visible:

```go
type Catalog struct {
	byContract map[string][]CoinMapping
	byNative   map[string][]CoinMapping
}

func contractLookupKey(chain, address string) string {
	return strings.ToLower(strings.TrimSpace(chain)) + "\x00" + normalizePlatformAddress(address)
}

func (c *Catalog) ResolveContract(chain, address string) (string, bool) {
	return oneID(c.byContract[contractLookupKey(chain, address)], nil)
}

func (c *Catalog) ResolveNative(symbol string, allowed map[string]struct{}) (string, bool) {
	return oneID(c.byNative[strings.ToLower(strings.TrimSpace(symbol))], allowed)
}
```

`NewCatalog` indexes native rows by lowercase symbol and other rows by contract key. `oneID` deduplicates identical IDs, applies the optional allowlist, and succeeds only for exactly one unique result. Implement `Holder` with `atomic.Pointer[Catalog]`, `Current`, `Set`, and nil-safe delegate methods.

- [ ] **Step 5: Run unit and race tests**

```bash
go test ./internal/marketdata -run 'TestBuildMappings|TestCatalog|TestHolder' -v
go test -race ./internal/marketdata -run TestHolder -v
```

Expected: all catalog tests pass with no race.

- [ ] **Step 6: Commit the catalog model**

```bash
git add internal/marketdata/catalog.go internal/marketdata/catalog_test.go
git commit -m "feat(marketdata): add CoinGecko catalog index"
```

---

### Task 4: Persist and refresh the catalog atomically

**Files:**
- Modify: `internal/store/schema.sql:40-51`
- Modify: `internal/store/store.go:1-262`
- Modify: `internal/store/store_test.go:15-226`
- Create: `internal/marketdata/refresher.go`
- Create: `internal/marketdata/refresher_test.go`

**Interfaces:**
- Consumes: `Client.ListCoins`, `BuildMappings`, `NewCatalog`, and `Holder`.
- Produces: `Postgres.ReplaceCoinMappings`, `Postgres.LoadCoinMappings`, `NewRefresher`, `Refresher.Bootstrap`, and `Refresher.Run`.

- [ ] **Step 1: Write failing PostgreSQL catalog tests**

Extend `newTestStore` cleanup to include `coingecko_coin_mappings`. Add absent-load, full round-trip, full replacement, and rollback-on-duplicate tests. The round-trip test begins with:

```go
func TestReplaceAndLoadCoinMappings(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	fetchedAt := time.Now().UTC().Truncate(time.Second)
	first := []marketdata.CoinMapping{
		{ID: "ethereum", Name: "Ethereum", Symbol: "eth", Chain: "eth", Address: marketdata.NativeAddress, FetchedAt: fetchedAt},
		{ID: "usd-coin", Name: "USDC", Symbol: "usdc", Chain: "ethereum", Address: "0xa0b8", FetchedAt: fetchedAt},
	}
	if err := s.ReplaceCoinMappings(ctx, first); err != nil {
		t.Fatalf("ReplaceCoinMappings: %v", err)
	}
	got, ok, err := s.LoadCoinMappings(ctx)
	if err != nil || !ok || !reflect.DeepEqual(got, first) {
		t.Fatalf("LoadCoinMappings ok=%v err=%v got=%#v", ok, err, got)
	}
}
```

The rollback test first stores one valid row, then submits the same primary key twice, expects `ReplaceCoinMappings` to fail, and asserts the original row still loads.

- [ ] **Step 2: Write failing refresher tests**

Use these exact boundaries:

```go
type CoinListClient interface {
	ListCoins(context.Context) ([]coingecko.Coin, error)
}

type CatalogStore interface {
	ReplaceCoinMappings(context.Context, []CoinMapping) error
	LoadCoinMappings(context.Context) ([]CoinMapping, bool, error)
}
```

Test fetch success, empty transformed fetch, fetch failure with PostgreSQL fallback, replace failure with fallback, no-source non-fatal bootstrap, successful periodic swap, and failed refresh preserving the exact prior holder pointer.

- [ ] **Step 3: Run focused tests and verify the red state**

```bash
go test ./internal/marketdata -run 'TestRefresher|TestBootstrap' -v
go test ./internal/store -run 'TestReplaceAndLoadCoinMappings|TestReplaceCoinMappingsRollsBack' -v
```

Expected: build failure for missing methods and refresher types.

- [ ] **Step 4: Add schema and transactional bulk replacement**

Append:

```sql
CREATE TABLE IF NOT EXISTS coingecko_coin_mappings (
    id         TEXT        NOT NULL,
    name       TEXT        NOT NULL,
    symbol     TEXT        NOT NULL,
    chain      TEXT        NOT NULL,
    address    TEXT        NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (id, chain, address)
);

CREATE INDEX IF NOT EXISTS coingecko_coin_mappings_lookup_idx
    ON coingecko_coin_mappings (chain, address);
```

Import `wallet-api/internal/marketdata` in `store.go`. `ReplaceCoinMappings` must begin a transaction, defer background rollback, execute transactional `TRUNCATE`, bulk-copy columns `id,name,symbol,chain,address,fetched_at`, and commit only after the copy succeeds. `LoadCoinMappings` issues one ordered query and returns `ok=false` only for zero rows.

- [ ] **Step 5: Implement non-fatal bootstrap and refresh**

Create:

```go
type Refresher struct {
	client   CoinListClient
	store    CatalogStore
	holder   *Holder
	interval time.Duration
	now      func() time.Time
	logf     func(string, ...any)
}

func NewRefresher(client CoinListClient, store CatalogStore, holder *Holder, interval time.Duration) *Refresher {
	return &Refresher{client: client, store: store, holder: holder, interval: interval, now: time.Now, logf: log.Printf}
}
```

`Bootstrap` returns no error. It attempts fetch/build/replace/set; on any failure it loads the last non-empty PostgreSQL snapshot. If neither source works, it logs and leaves the holder empty. Log the selected bootstrap source and row count. The periodic ticker repeats fetch/build/replace/set, logs the successful row count or failure, and never changes the holder before PostgreSQL commit.

- [ ] **Step 6: Run catalog persistence/refresher tests**

```bash
go test ./internal/marketdata -run 'TestRefresher|TestBootstrap' -v
go test ./internal/store -run 'TestReplaceAndLoadCoinMappings|TestReplaceCoinMappingsRollsBack' -v
```

Expected: refresher tests pass; store tests pass with a test DSN or compile and skip without one.

- [ ] **Step 7: Commit the durable catalog**

```bash
git add internal/store/schema.sql internal/store/store.go internal/store/store_test.go internal/marketdata/refresher.go internal/marketdata/refresher_test.go
git commit -m "feat(marketdata): persist and refresh CoinGecko catalog"
```

---

### Task 5: Add shared PostgreSQL and Redis market records

**Files:**
- Create: `internal/marketdata/record.go`
- Create: `internal/marketdata/record_test.go`
- Modify: `internal/store/schema.sql`, `store.go`, and `store_test.go`
- Modify: `internal/rediscache/cache.go` and `cache_test.go`

**Interfaces:**
- Consumes: configurable market TTL.
- Produces: `Key`, `Record`, `CacheWrite`, freshness methods, PostgreSQL batch methods, and Redis batch methods.

- [ ] **Step 1: Write failing model tests**

```go
func TestRecordFreshnessAndRemainingTTL(t *testing.T) {
	now := time.Unix(2_000, 0).UTC()
	record := Record{FetchedAt: now.Add(-20 * time.Minute)}
	if !record.Fresh(now, 30*time.Minute) || record.RemainingTTL(now, 30*time.Minute) != 10*time.Minute {
		t.Fatalf("unexpected fresh record result")
	}
	if record.Fresh(now, 20*time.Minute) || record.RemainingTTL(now, 20*time.Minute) != 0 {
		t.Fatal("exact boundary must be stale")
	}
}

func TestKeysNormalizeDefinedParts(t *testing.T) {
	if got := ContractKey("Ethereum", "0xA0B8"); got != (Key{Chain: "ethereum", TokenKey: "0xa0b8"}) {
		t.Fatalf("contract key = %#v", got)
	}
	if got := NativeKey("Ethereum", "eth"); got != (Key{Chain: "ethereum", TokenKey: "native:ETH"}) {
		t.Fatalf("native key = %#v", got)
	}
}
```

- [ ] **Step 2: Write failing PostgreSQL/Redis integration tests**

For PostgreSQL, first add `coingecko_market_data` to `newTestStore`'s cleanup statement, then round-trip two records with nullable fields, upsert one changed ID/value, and prove batch load returns only requested keys. Add an out-of-order write case that saves a newer record and then an older record for the same key; loading the key must still return the newer record. For Redis, explicitly delete the test market keys during setup/cleanup, save/load two records, and inspect Redis TTL to prove a 10-minute `CacheWrite` does not use the LI.FI list safety TTL. Add the same out-of-order case and assert that an older write cannot replace the newer JSON payload or extend its freshness.

Use this record shape:

```go
price := "1.0001"
change := -0.25
capUSD := 32_000_000_000.0
updatedAt := time.Unix(1_784_781_600, 0).UTC()
record := marketdata.Record{
	Key: marketdata.ContractKey("ethereum", "0xA0B8"), CoinGeckoID: "usd-coin",
	PriceUSD: &price, Change24HPercent: &change, MarketCapUSD: &capUSD,
	MarketDataUpdatedAt: &updatedAt, FetchedAt: time.Now().UTC().Truncate(time.Second),
}
```

- [ ] **Step 3: Run focused tests and verify the red state**

```bash
go test ./internal/marketdata -run 'TestRecord|TestKeys' -v
go test ./internal/store -run TestMarketData -v
go test ./internal/rediscache -run TestMarketData -v
```

Expected: build failure for missing model and store/cache methods.

- [ ] **Step 4: Define the shared market model**

Create:

```go
type Key struct {
	Chain    string
	TokenKey string
}

func ContractKey(chain, address string) Key {
	return Key{Chain: strings.ToLower(strings.TrimSpace(chain)), TokenKey: normalizePlatformAddress(address)}
}

func NativeKey(chain, symbol string) Key {
	return Key{Chain: strings.ToLower(strings.TrimSpace(chain)), TokenKey: "native:" + strings.ToUpper(strings.TrimSpace(symbol))}
}

type Record struct {
	Key
	CoinGeckoID         string     `json:"coingeckoID"`
	PriceUSD            *string    `json:"priceUSD"`
	Change24HPercent    *float64   `json:"change24hPercent"`
	MarketCapUSD        *float64   `json:"marketCapUSD"`
	MarketDataUpdatedAt *time.Time `json:"marketDataUpdatedAt"`
	FetchedAt           time.Time  `json:"fetchedAt"`
}

type CacheWrite struct {
	Record Record
	TTL    time.Duration
}
```

`RemainingTTL` returns zero at or beyond the configured boundary; `Fresh` is `RemainingTTL > 0`.

- [ ] **Step 5: Add PostgreSQL schema and batch methods**

Append the approved `coingecko_market_data` table from the spec. Implement:

```go
func (s *Postgres) LoadMarketData(ctx context.Context, keys []marketdata.Key) (map[marketdata.Key]marketdata.Record, error)
func (s *Postgres) SaveMarketData(ctx context.Context, records []marketdata.Record) error
```

Load with one query joined to `unnest($1::text[], $2::text[]) AS wanted(chain, token_key)`. Scan price as nullable string, change/cap as nullable float64, and update time as nullable `time.Time`. Save all upserts in one `pgx.Batch`; use `ON CONFLICT (chain, token_key) DO UPDATE` with a `WHERE EXCLUDED.fetched_at >= coingecko_market_data.fetched_at` guard so a late older fetch is a no-op; return early for empty input.

- [ ] **Step 6: Add Redis `MGET` and pipelined writes**

Use:

```go
func marketKey(k marketdata.Key) string {
	return "coingecko:market:" + k.Chain + ":" + k.TokenKey
}

func (c *Cache) LoadMarketData(ctx context.Context, keys []marketdata.Key) (map[marketdata.Key]marketdata.Record, error)
func (c *Cache) SaveMarketData(ctx context.Context, writes []marketdata.CacheWrite) error
```

`LoadMarketData` performs one `MGet`, skips nils, logs malformed individual JSON values, and preserves valid hits. `SaveMarketData` pre-marshals positive-TTL writes and runs all writes in one pipeline. Each write must be an atomic timestamp-guarded compare-and-set, not an unconditional `SET`: include a normalized numeric `fetchedAtUnixNano` in the Redis envelope (or equivalent version metadata), and use a small Lua script in the pipeline to update the payload and TTL only when the incoming timestamp is at least as new as the stored timestamp. It must use each `CacheWrite.TTL`, never `Cache.ttl`, and an older rejected write must not extend the existing key's TTL.

Update the package and `Cache` comments so `internal/rediscache` accurately describes LI.FI, Moralis, and CoinGecko caching rather than claiming it stores only the LI.FI token list.

- [ ] **Step 7: Run model and persistence tests**

```bash
go test ./internal/marketdata -run 'TestRecord|TestKeys' -v
go test ./internal/store -run TestMarketData -v
go test ./internal/rediscache -run TestMarketData -v
```

Expected: model tests pass; integration tests pass with configured services or compile and skip.

- [ ] **Step 8: Commit shared market persistence**

```bash
git add internal/marketdata/record.go internal/marketdata/record_test.go internal/store/schema.sql internal/store/store.go internal/store/store_test.go internal/rediscache/cache.go internal/rediscache/cache_test.go
git commit -m "feat(marketdata): add shared PostgreSQL and Redis cache"
```

---

### Task 6: Extend `wallet.Token` and add the post-filter enrichment hook

**Files:**
- Modify: `internal/wallet/types.go:19-105`
- Modify: `internal/wallet/service.go:13-89`
- Modify: `internal/wallet/fakes_test.go`
- Modify: `internal/wallet/service_test.go`
- Modify: `internal/api/handlers_test.go`
- Modify: `cmd/server/main.go:65-69` temporarily to pass nil until Task 8.

**Interfaces:**
- Consumes: existing LI.FI/Moralis filtering and native construction.
- Produces: public market fields and `wallet.MarketEnricher`, implemented in Task 7.

- [ ] **Step 1: Write failing wallet ordering and JSON-shape tests**

Add this fake:

```go
type fakeMarketEnricher struct {
	calls int
	seen  []Token
	out   []Token
}

func (f *fakeMarketEnricher) EnrichTokens(_ context.Context, tokens []Token) []Token {
	f.calls++
	f.seen = append([]Token(nil), tokens...)
	if f.out != nil {
		return append([]Token(nil), f.out...)
	}
	return tokens
}
```

Test both fresh-PG and Alchemy paths with a dropped `SCAM` token; assert `SCAM` never appears in `fake.seen`, the fake is called once, and its returned token slice is used. Add a native test proving `GetNativeTokens` calls it once after LI.FI constructs ETH.

In `handlers_test.go`, assert unavailable fields are present as JSON null:

```go
func TestTokenResponseIncludesNullableMarketFields(t *testing.T) {
	svc := &stubService{portfolio: &wallet.TokenPortfolio{
		Address: validAddr, Network: "eth-mainnet",
		Tokens: []wallet.Token{{Symbol: "ETH", IsNative: true}},
	}}
	rec := doGet(NewRouter(svc), "/v1/addresses/"+validAddr+"/tokens")
	var body struct {
		Tokens []map[string]any `json:"tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"change24hPercent", "marketCapUSD", "marketDataUpdatedAt"} {
		value, ok := body.Tokens[0][key]
		if !ok || value != nil {
			t.Errorf("%s present=%v value=%v", key, ok, value)
		}
	}
}
```

- [ ] **Step 2: Run focused tests and verify the red state**

```bash
go test ./internal/wallet ./internal/api -run 'Test.*Market|TestTokenResponseIncludesNullableMarketFields' -v
```

Expected: build failure for missing fields/interface/constructor argument.

- [ ] **Step 3: Add fields and the consumer-owned interface**

Add to `Token` without `omitempty`:

```go
Change24HPercent    *float64 `json:"change24hPercent"`
MarketCapUSD        *float64 `json:"marketCapUSD"`
MarketDataUpdatedAt *string  `json:"marketDataUpdatedAt"`
```

Add:

```go
type MarketEnricher interface {
	EnrichTokens(ctx context.Context, tokens []Token) []Token
}
```

- [ ] **Step 4: Invoke enrichment only after filtering**

Add `market MarketEnricher` immediately after `validator` in `Service` and `NewService`. Use one helper for cache-hit and cache-miss address paths:

```go
func (s *Service) finishTokens(ctx context.Context, p *TokenPortfolio) *TokenPortfolio {
	out := s.filterTokens(ctx, p)
	if s.market != nil {
		out.Tokens = s.market.EnrichTokens(ctx, out.Tokens)
	}
	return out
}
```

Replace both address-token filter returns with `finishTokens`. Change `GetNativeTokens(_ context.Context)` to take `ctx`, preserve all LI.FI availability checks, and pass the completed one-element slice through the enricher when non-nil.

Update every `NewService` call with a nil enricher unless the test supplies the fake. Pass nil from `cmd/server/main.go` so this commit compiles before real wiring.

- [ ] **Step 5: Run wallet, API, and repository tests**

```bash
go test ./internal/wallet ./internal/api -v
go test ./...
```

Expected: all tests pass and transactions are unchanged.

- [ ] **Step 6: Commit the public contract and hook**

```bash
git add internal/wallet/types.go internal/wallet/service.go internal/wallet/fakes_test.go internal/wallet/service_test.go internal/api/handlers_test.go cmd/server/main.go
git commit -m "feat(wallet): expose and invoke token market enrichment"
```

---

### Task 7: Implement Redis-first enrichment, batching, deadline, and precedence

**Files:**
- Create: `internal/marketdata/service.go`
- Create: `internal/marketdata/service_test.go`

**Interfaces:**
- Consumes: catalog holder, retrying price client, shared cache/store, and wallet fields.
- Produces: `marketdata.NewService` and `Service.EnrichTokens`, satisfying `wallet.MarketEnricher`.

- [ ] **Step 1: Write failing cache-precedence and overwrite tests**

Define fakes for these exact interfaces:

```go
type PriceClient interface {
	GetPrices(context.Context, []string) (map[string]coingecko.SimplePrice, error)
}

type MarketCache interface {
	LoadMarketData(context.Context, []Key) (map[Key]Record, error)
	SaveMarketData(context.Context, []CacheWrite) error
}

type MarketStore interface {
	LoadMarketData(context.Context, []Key) (map[Key]Record, error)
	SaveMarketData(context.Context, []Record) error
}
```

The fakes record keys, ID batches, saved records, and Redis TTLs. Add these named tests with exact outcomes:

- `TestEnrichFreshRedisHitSkipsPostgresAndCoinGecko`: matching fresh Redis data overwrites both prices and sets all new fields; no downstream calls.
- `TestEnrichFreshPostgresHitPromotesRemainingTTL`: a 20-minute-old PG row under a 30-minute TTL writes Redis with exactly 10 minutes and does not fetch.
- `TestEnrichStaleMatchingRecordUsesOnlyNonPriceFallback`: failed price fetch keeps the original LI.FI price but copies stale change/cap/time.
- `TestEnrichStaleMismatchedRecordIsDiscarded`: different stored ID keeps the original price and leaves every new field nil.
- `TestEnrichFetchedPriceOverwritesOriginalImmediately`: successful response changes output even when both persistence writes fail.
- `TestEnrichFreshRecordWithoutUSDDoesNotEraseOriginalPrice`: fresh change/cap/time fields apply while both existing price representations remain unchanged.
- `TestEnrichPartialResponseUsesStaleForOmittedID`: one returned ID becomes fresh; an omitted ID gets only its matching stale non-price values.

Use exact number strings:

```go
usd := json.Number("3210.45")
change := json.Number("-1.23")
capUSD := json.Number("387123456789.45")
updated := int64(1_784_781_600)
prices := map[string]coingecko.SimplePrice{
	"ethereum": {USD: &usd, USD24HChange: &change, USDMarketCap: &capUSD, LastUpdatedAt: &updated},
}
```

- [ ] **Step 2: Write failing batching, native, and deadline tests**

Add named tests proving:

- 101 unique IDs produce sequential batches of 100 and 1;
- repeated cache keys and CoinGecko IDs are deduplicated;
- native ETH resolves to `Key{Chain:"ethereum",TokenKey:"native:ETH"}`;
- ambiguous mapping skips cache and client calls;
- Redis read error falls through to PostgreSQL;
- PostgreSQL read error fetches without stale fallback;
- a blocking client context is canceled by a 20-millisecond test enrichment budget; and
- a successful first batch stays applied/persisted when the deadline expires during a later batch.

- [ ] **Step 3: Run service tests and verify the red state**

```bash
go test ./internal/marketdata -run TestEnrich -v
```

Expected: build failure because `marketdata.Service` does not exist.

- [ ] **Step 4: Define dependencies and constructor**

Create:

```go
const priceBatchSize = 100

type Service struct {
	client        PriceClient
	cache         MarketCache
	store         MarketStore
	catalog       *Holder
	platform      string
	nativeIDs     map[string]struct{}
	ttl           time.Duration
	enrichTimeout time.Duration
	now           func() time.Time
	logf          func(string, ...any)
}

func NewService(client PriceClient, cache MarketCache, store MarketStore, catalog *Holder, platform string, nativeIDs []string, ttl, enrichTimeout time.Duration) *Service {
	allowed := make(map[string]struct{}, len(nativeIDs))
	for _, id := range nativeIDs {
		allowed[id] = struct{}{}
	}
	return &Service{
		client: client, cache: cache, store: store, catalog: catalog,
		platform: strings.ToLower(platform), nativeIDs: allowed,
		ttl: ttl, enrichTimeout: enrichTimeout, now: time.Now, logf: log.Printf,
	}
}

type resolvedToken struct {
	Index int
	Key   Key
	ID    string
}
```

`resolve` checks native before contract, calls the corresponding catalog method, creates `NativeKey`/`ContractKey`, and keeps every token index while deduplicating cache keys.

- [ ] **Step 5: Implement the exact cache and fetch state machine**

`EnrichTokens` clones the input, returns early for empty input or missing catalog, and derives one context at entry. Pass this `enrichCtx` to every Redis, PostgreSQL, CoinGecko, and cache-write call so the configured budget covers the entire pass:

```go
enrichCtx, cancel := context.WithTimeout(ctx, s.enrichTimeout)
defer cancel()
```

Implement these ordered states:

1. Build `expectedID map[Key]string` and `indexesByKey map[Key][]int`; log an unresolved mapping with chain/address or native symbol so missing and ambiguous upstream mappings are observable.
2. One Redis load for every key. Accept only matching-ID records fresh under the configured TTL; apply as fresh and mark complete.
3. One PostgreSQL load for remaining keys. Discard ID mismatches before storing fallback. Apply fresh rows, mark complete, and queue Redis writes with `RemainingTTL`; retain matching stale rows separately.
4. Best-effort write PG promotions to Redis.
5. Group remaining keys by ID, sort IDs deterministically, and split into chunks of at most 100.
6. Before each chunk, stop when `enrichCtx.Err()` is non-nil. Call the retrying price client sequentially and log retry exhaustion once per failed batch with its ID count.
7. For each returned ID, create records for every unresolved key with one batch `fetchedAt := s.now().UTC()`, apply fresh values before persistence, then independently save that batch to PG and Redis.
8. After batches stop or finish, apply stale non-price values only to still-incomplete keys.

Fresh application sets change/cap/timestamp and overwrites `PriceUSD` plus `wallet.Price{Currency:"usd",Value:<exact USD string>,LastUpdatedAt:<RFC3339 or empty>}` only when USD exists. Stale application sets only the three new fields. Convert optional change/cap numbers via `Float64`; invalid optional values are logged and become nil without discarding other fields.

- [ ] **Step 6: Run service and race tests**

```bash
go test ./internal/marketdata -v
go test -race ./internal/marketdata -v
```

Expected: all market-data tests pass with no race.

- [ ] **Step 7: Commit orchestration**

```bash
git add internal/marketdata/service.go internal/marketdata/service_test.go
git commit -m "feat(marketdata): add Redis-first token enrichment"
```

---

### Task 8: Wire optional CoinGecko enrichment into server startup

**Files:**
- Modify: `cmd/server/main.go:3-79`

**Interfaces:**
- Consumes: all feature constructors and existing PostgreSQL/Redis instances.
- Produces: non-fatal catalog bootstrap plus live market enrichment.

- [ ] **Step 1: Add runtime wiring**

Import `internal/coingecko` and `internal/marketdata`. After store/cache setup and before `wallet.NewService`, add:

```go
coinGeckoClient := coingecko.New(cfg.CoinGeckoBaseURL, cfg.CoinGeckoUserAgent)
coinCatalog := &marketdata.Holder{}
coinRefresher := marketdata.NewRefresher(coinGeckoClient, pg, coinCatalog, cfg.CoinGeckoListRefresh)
coinRefresher.Bootstrap(setupCtx)
go coinRefresher.Run(context.Background())

coinMarket := marketdata.NewService(
	coinGeckoClient, redisCache, pg, coinCatalog,
	cfg.CoinGeckoPlatform, cfg.CoinGeckoNativeIDs,
	cfg.CoinGeckoMarketTTL, cfg.CoinGeckoEnrichTimeout,
)
```

Pass `coinMarket` instead of nil to `wallet.NewService`. Log catalog count defensively as zero when `Current()` is nil. Never use `log.Fatalf` for CoinGecko fetch/empty-catalog outcomes.

- [ ] **Step 2: Compile and run all tests**

```bash
go test ./...
go build ./cmd/server
```

Expected: tests pass and the server builds with no new module.

- [ ] **Step 3: Commit runtime wiring**

```bash
git add cmd/server/main.go
git commit -m "feat(server): wire CoinGecko market enrichment"
```

---

### Task 9: Update docs, run integrations, and verify the repository

**Files:**
- Modify: `internal/api/handlers.go:32-63`
- Modify: `internal/api/openapi_test.go`
- Modify: `README.md`
- Regenerate: `internal/apidocs/swagger.json`, `internal/apidocs/swagger.yaml`, and `docs/api/openapi.json`

**Interfaces:**
- Consumes: complete feature from Tasks 1–8.
- Produces: accurate public docs and verified implementation.

- [ ] **Step 1: Write a failing generated-schema assertion**

Extend `TestServeOpenAPISpec` to locate the token definition and assert:

```go
for _, name := range []string{"change24hPercent", "marketCapUSD", "marketDataUpdatedAt"} {
	if _, ok := tokenProperties[name]; !ok {
		t.Errorf("wallet.Token schema missing %s", name)
	}
}
```

Run `go test ./internal/api -run TestServeOpenAPISpec -v`. Expected: failure because committed generated docs predate the fields.

- [ ] **Step 2: Update endpoint annotations and README**

Describe CoinGecko USD price/change/cap enrichment and best-effort cache fallback in both token endpoint annotations. In README, add every `COINGECKO_*` variable/default, six-hour refresh, configurable 30-minute TTL, five-second total budget, fresh-price overwrite, matching-ID stale non-price fallback, and the new packages. Remove the claim that `/v1/native` never makes a request-time provider call.

- [ ] **Step 3: Regenerate and verify OpenAPI**

```bash
make docs
go test ./internal/api -v
```

Expected: generated files change and all API tests pass.

- [ ] **Step 4: Run PostgreSQL and Redis integration tests**

```bash
docker compose up -d
WALLET_TEST_DATABASE_URL=postgres://wallet:wallet@localhost:5433/wallet WALLET_TEST_REDIS_URL=redis://localhost:6379/15 go test ./internal/store ./internal/rediscache -v
```

Expected: all old and new integration tests pass without skips.

- [ ] **Step 5: Run the full verification suite**

```bash
GOCACHE=/private/tmp/wallet-api-go-build go test ./...
GOCACHE=/private/tmp/wallet-api-go-build go vet ./...
GOCACHE=/private/tmp/wallet-api-go-build make docs-check
git diff --check
git status --short
```

Expected: tests/vet/docs-check/diff-check pass. Status contains only intended feature files plus the user's already-modified design spec, this plan, and untracked `.superpowers/` if it still exists.

- [ ] **Step 6: Commit docs and generated artifacts**

Do not stage `.superpowers/` or silently stage the user's design edit:

```bash
git add README.md internal/api/handlers.go internal/api/openapi_test.go internal/apidocs/swagger.json internal/apidocs/swagger.yaml docs/api/openapi.json
git commit -m "docs(api): document CoinGecko token market data"
```

- [ ] **Step 7: Audit history and status**

```bash
git log --oneline -9
git status --short
```

Expected: task commits contain no Codex session URL. Remaining design-spec changes belong to the user; `.superpowers/` remains untracked unless the user separately chooses to ignore or remove it.

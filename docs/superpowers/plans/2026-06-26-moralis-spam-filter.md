# Moralis Spam Filter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give unlisted ERC-20s a second-chance validity gate via Moralis token metadata, returning only non-spam verified tokens (enriched with Moralis metadata) and dropping the rest.

**Architecture:** A new `moralis` HTTP client and a `tokenvalidity.Checker` that resolves each unlisted contract through a three-tier lookup — Redis (1-day hot cache) → Postgres (permanent, weekly re-check) → Moralis. `wallet.Service` gains a `Validator` dependency; `filterTokens` consults it for ERC-20s missing from the LI.FI allowlist, keeping + enriching valid ones and dropping invalid/unknown ones (fail-closed, with a stale-Postgres fallback on Moralis errors).

**Tech Stack:** Go 1.25, `net/http`, pgx/v5 (Postgres), go-redis/v9, standard `testing` + `httptest`.

## Global Constraints

- Public repository: never put `Claude-Session:` links or `https://claude.ai/code/session_...` URLs in commits. `Co-Authored-By:` and the `🤖 Generated with Claude Code` line are fine.
- Validity rule (single source of truth, lives in `tokenvalidity`): `Valid = !possible_spam && verified_contract`.
- Re-check window: **1 week** (default `604800` s). Redis hot-cache TTL: **1 day** (default `86400` s).
- `MORALIS_API_KEY` is **required** (process fails to start without it), like `ALCHEMY_API_KEY`.
- Fail-closed: a Moralis error with no stored verdict drops the token; with a stale Postgres verdict, the stale verdict is used.
- Token addresses are lowercased before use as keys (matches existing tables/caches).
- Moralis returns `decimals` as a **string**; parse to int (invalid/empty → 0). `logo` may be null → empty string.
- Import direction (acyclic): `tokenvalidity` imports `wallet` + `moralis`; `store` and `rediscache` import `tokenvalidity` (for `Record`). `wallet` imports neither.
- Run `make docs-check` is unaffected (no handler/annotation changes) but must still pass at the end.

---

## File Structure

- `internal/moralis/client.go` (create) — Moralis token-metadata HTTP client.
- `internal/moralis/client_test.go` (create) — httptest-based client tests.
- `internal/tokenvalidity/checker.go` (create) — `Record`, interfaces, three-tier `Checker`.
- `internal/tokenvalidity/checker_test.go` (create) — checker tests with fakes + injectable clock.
- `internal/wallet/types.go` (modify) — add `Validator` interface + `Validation` struct.
- `internal/wallet/service.go` (modify) — `validator` field, `ctx`-aware `filterTokens`, `enrichFromValidation`.
- `internal/wallet/fakes_test.go` (modify) — add `fakeValidator`.
- `internal/wallet/service_test.go` (modify) — update `NewService` call sites; add unlisted-valid test.
- `internal/store/schema.sql` (modify) — add `token_metadata` table.
- `internal/store/store.go` (modify) — add `GetTokenMeta` / `SaveTokenMeta`.
- `internal/store/store_test.go` (modify) — add round-trip/upsert tests; extend TRUNCATE.
- `internal/rediscache/cache.go` (modify) — add `LoadTokenMeta` / `SaveTokenMeta`.
- `internal/rediscache/cache_test.go` (modify) — add round-trip test.
- `internal/config/config.go` (modify) — add Moralis config vars.
- `internal/config/config_test.go` (modify) — update env maps; add required/validation tests.
- `cmd/server/main.go` (modify) — wire moralis client + checker into the service.

---

## Task 1: Moralis token-metadata client

**Files:**
- Create: `internal/moralis/client.go`
- Test: `internal/moralis/client_test.go`

**Interfaces:**
- Consumes: nothing internal.
- Produces:
  - `type Metadata struct { Symbol, Name, Logo string; Decimals int; PossibleSpam, VerifiedContract bool }`
  - `func New(apiKey, chain string) *Client`
  - `func (c *Client) GetTokenMetadata(ctx context.Context, address string) (Metadata, error)`

- [ ] **Step 1: Write the failing test**

Create `internal/moralis/client_test.go`:

```go
package moralis

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testClient(srv *httptest.Server) *Client {
	return &Client{apiKey: "k", chain: "eth", baseURL: srv.URL, httpClient: srv.Client()}
}

func TestGetTokenMetadataParsesFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-API-Key"); got != "k" {
			t.Errorf("X-API-Key = %q, want k", got)
		}
		if got := r.URL.Query().Get("chain"); got != "eth" {
			t.Errorf("chain = %q, want eth", got)
		}
		if got := r.URL.Query().Get("addresses[]"); got != "0xABC" {
			t.Errorf("addresses[] = %q, want 0xABC", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"symbol":"PEPE","name":"Pepe","logo":"https://logo/pepe.png","decimals":"18","possible_spam":false,"verified_contract":true}]`))
	}))
	defer srv.Close()

	m, err := testClient(srv).GetTokenMetadata(context.Background(), "0xABC")
	if err != nil {
		t.Fatalf("GetTokenMetadata: %v", err)
	}
	want := Metadata{Symbol: "PEPE", Name: "Pepe", Logo: "https://logo/pepe.png", Decimals: 18, PossibleSpam: false, VerifiedContract: true}
	if m != want {
		t.Errorf("got %+v, want %+v", m, want)
	}
}

func TestGetTokenMetadataEmptyArrayIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	if _, err := testClient(srv).GetTokenMetadata(context.Background(), "0xABC"); err == nil {
		t.Fatal("expected error for empty array")
	}
}

func TestGetTokenMetadataNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	if _, err := testClient(srv).GetTokenMetadata(context.Background(), "0xABC"); err == nil {
		t.Fatal("expected error for 429")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/moralis/`
Expected: FAIL — build error, `Client`/`Metadata`/`New`/`GetTokenMetadata` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/moralis/client.go`:

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/moralis/`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/moralis/
git commit -m "feat(moralis): add ERC-20 token-metadata client"
```

---

## Task 2: `wallet.Validator` dependency + spam-aware filter

**Files:**
- Modify: `internal/wallet/types.go`
- Modify: `internal/wallet/service.go`
- Modify: `internal/wallet/fakes_test.go`
- Modify: `internal/wallet/service_test.go`

**Interfaces:**
- Consumes: nothing from other new tasks (uses a `fakeValidator` in tests).
- Produces:
  - `type Validation struct { Valid bool; Symbol, Name, LogoURI string; Decimals int }`
  - `type Validator interface { Validate(ctx context.Context, address string) (Validation, error) }`
  - `func NewService(a AlchemyClient, ts TokenStore, tc TxCache, allow Allowlist, validator Validator, network string, ttl time.Duration) *Service`

- [ ] **Step 1: Write the failing test**

In `internal/wallet/fakes_test.go`, add (keep existing content):

```go
// fakeValidator returns a canned Validation and records call count.
type fakeValidator struct {
	result Validation
	err    error
	calls  int
}

func (f *fakeValidator) Validate(ctx context.Context, address string) (Validation, error) {
	f.calls++
	return f.result, f.err
}

// denyValidator drops every unlisted token (preserves pre-Moralis behavior).
func denyValidator() *fakeValidator { return &fakeValidator{result: Validation{Valid: false}} }
```

In `internal/wallet/service_test.go`, add this new test:

```go
func TestGetTokensKeepsValidatedUnlistedToken(t *testing.T) {
	fa := &fakeAlchemy{tokens: []alchemy.Token{
		{TokenAddress: usdc("0xFEE7"), Symbol: "pepe", Name: "old", Decimals: 9, RawBalance: "12500000"},
	}}
	v := &fakeValidator{result: Validation{Valid: true, Symbol: "PEPE", Name: "Pepe", LogoURI: "https://logo/pepe.png", Decimals: 18}}
	svc := NewService(fa, &fakeTokenStore{}, &fakeTxCache{}, allowUSDC(), v, "eth-mainnet", time.Minute)

	p, err := svc.GetTokens(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Tokens) != 1 {
		t.Fatalf("got %d tokens, want 1", len(p.Tokens))
	}
	tok := p.Tokens[0]
	if tok.Symbol != "PEPE" || tok.Name != "Pepe" {
		t.Errorf("metadata not overlaid: %+v", tok)
	}
	if tok.LogoURI == nil || *tok.LogoURI != "https://logo/pepe.png" {
		t.Errorf("logo not set: %+v", tok.LogoURI)
	}
	if tok.Decimals != 18 {
		t.Errorf("decimals = %d, want 18", tok.Decimals)
	}
	if v.calls != 1 {
		t.Errorf("validator calls = %d, want 1", v.calls)
	}
}

func TestGetTokensDropsInvalidUnlistedToken(t *testing.T) {
	fa := &fakeAlchemy{tokens: []alchemy.Token{
		{TokenAddress: usdc("0xSPAM"), Symbol: "SCAM", Decimals: 18, RawBalance: "999"},
	}}
	svc := NewService(fa, &fakeTokenStore{}, &fakeTxCache{}, allowUSDC(), denyValidator(), "eth-mainnet", time.Minute)
	p, err := svc.GetTokens(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Tokens) != 0 {
		t.Fatalf("expected invalid token dropped, got %d", len(p.Tokens))
	}
}
```

Update **every** existing `NewService(...)` call in `service_test.go` to insert the validator argument after the allowlist. The existing calls expect unlisted tokens to be dropped, so pass `denyValidator()`:

- `NewService(fa, ts, &fakeTxCache{}, allowUSDC(), denyValidator(), "eth-mainnet", time.Minute)`
- Apply the same edit to all 10 call sites (lines ~32, 76, 96, 123, 139, 151, 168, 197, 214).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/wallet/`
Expected: FAIL — `Validation`/`Validator` undefined and `NewService` arity mismatch.

- [ ] **Step 3: Write minimal implementation**

In `internal/wallet/types.go`, add after the `Allowlist` interface block:

```go
// Validation is the verdict + enrichment metadata for an unlisted ERC-20.
type Validation struct {
	Valid    bool
	Symbol   string
	Name     string
	LogoURI  string
	Decimals int
}

// Validator decides whether an unlisted ERC-20 is a legitimate token and
// supplies enrichment metadata when it is.
type Validator interface {
	Validate(ctx context.Context, address string) (Validation, error)
}
```

In `internal/wallet/service.go`:

Add `validator Validator` to the `Service` struct (after `allow Allowlist`):

```go
type Service struct {
	alchemy   AlchemyClient
	tokens    TokenStore
	txs       TxCache
	allow     Allowlist
	validator Validator
	network   string
	ttl       time.Duration
	now       func() time.Time
}
```

Update `NewService`:

```go
// NewService builds a Service with a real-time clock.
func NewService(a AlchemyClient, ts TokenStore, tc TxCache, allow Allowlist, validator Validator, network string, ttl time.Duration) *Service {
	return &Service{alchemy: a, tokens: ts, txs: tc, allow: allow, validator: validator, network: network, ttl: ttl, now: time.Now}
}
```

Change both `filterTokens` call sites in `GetTokens` from `s.filterTokens(p)` to `s.filterTokens(ctx, p)` (lines ~36 and ~55).

Replace `filterTokens` with the `ctx`-aware version and add `enrichFromValidation`:

```go
// filterTokens keeps native tokens, keeps + enriches LI.FI-listed ERC-20s, and
// for unlisted ERC-20s consults the Validator: valid tokens are kept + enriched
// from Moralis metadata, everything else (invalid or error) is dropped.
func (s *Service) filterTokens(ctx context.Context, p *TokenPortfolio) *TokenPortfolio {
	out := &TokenPortfolio{Address: p.Address, Network: p.Network, FetchedAt: p.FetchedAt}
	out.Tokens = make([]Token, 0, len(p.Tokens))
	for _, t := range p.Tokens {
		if t.IsNative || t.TokenAddress == nil {
			out.Tokens = append(out.Tokens, t)
			continue
		}
		if lt, ok := s.allow.LookupByAddress(*t.TokenAddress); ok {
			out.Tokens = append(out.Tokens, enrichToken(t, lt))
			continue
		}
		v, err := s.validator.Validate(ctx, *t.TokenAddress)
		if err != nil || !v.Valid {
			continue // fail-closed: invalid or unknown tokens are dropped
		}
		out.Tokens = append(out.Tokens, enrichFromValidation(t, v))
	}
	return out
}

// enrichFromValidation overlays Moralis-derived metadata onto t. When decimals
// change, Balance is re-derived from RawBalance so the scaled value stays correct.
func enrichFromValidation(t Token, v Validation) Token {
	if v.Symbol != "" {
		t.Symbol = v.Symbol
	}
	if v.Name != "" {
		t.Name = v.Name
	}
	if v.LogoURI != "" {
		t.LogoURI = strptr(v.LogoURI)
	}
	if v.Decimals > 0 && v.Decimals != t.Decimals {
		if _, scaled, err := ScaleBalance(t.RawBalance, v.Decimals); err == nil {
			t.Balance = scaled
		}
		t.Decimals = v.Decimals
	}
	return t
}
```

The `context` package is already imported in `service.go`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/wallet/`
Expected: PASS (all existing tests + 2 new).

- [ ] **Step 5: Commit**

```bash
git add internal/wallet/
git commit -m "feat(wallet): add Validator gate for unlisted ERC-20s"
```

---

## Task 3: `tokenvalidity.Checker` three-tier lookup

**Files:**
- Create: `internal/tokenvalidity/checker.go`
- Test: `internal/tokenvalidity/checker_test.go`

**Interfaces:**
- Consumes: `moralis.Metadata` (Task 1); `wallet.Validation` (Task 2).
- Produces:
  - `type Record struct { PossibleSpam, Verified bool; Symbol, Name, Logo string; Decimals int; FetchedAt time.Time }`
  - `type MoralisClient interface { GetTokenMetadata(ctx context.Context, address string) (moralis.Metadata, error) }`
  - `type Cache interface { LoadTokenMeta(ctx, chain, address) (Record, bool, error); SaveTokenMeta(ctx, chain, address, Record, ttl) error }`
  - `type Store interface { GetTokenMeta(ctx, chain, address) (Record, bool, error); SaveTokenMeta(ctx, chain, address, Record) error }`
  - `func NewChecker(m MoralisClient, c Cache, s Store, chain string, recheck, redisTTL time.Duration) *Checker`
  - `func (c *Checker) Validate(ctx context.Context, address string) (wallet.Validation, error)`

- [ ] **Step 1: Write the failing test**

Create `internal/tokenvalidity/checker_test.go`:

```go
package tokenvalidity

import (
	"context"
	"errors"
	"testing"
	"time"

	"wallet-api/internal/moralis"
)

type fakeMoralis struct {
	meta  moralis.Metadata
	err   error
	calls int
}

func (f *fakeMoralis) GetTokenMetadata(ctx context.Context, address string) (moralis.Metadata, error) {
	f.calls++
	return f.meta, f.err
}

type fakeCache struct {
	rec    Record
	hit    bool
	saved  int
	loaded int
}

func (f *fakeCache) LoadTokenMeta(ctx context.Context, chain, address string) (Record, bool, error) {
	f.loaded++
	return f.rec, f.hit, nil
}
func (f *fakeCache) SaveTokenMeta(ctx context.Context, chain, address string, r Record, ttl time.Duration) error {
	f.saved++
	f.rec, f.hit = r, true
	return nil
}

type fakeStore struct {
	rec   Record
	hit   bool
	saved int
}

func (f *fakeStore) GetTokenMeta(ctx context.Context, chain, address string) (Record, bool, error) {
	return f.rec, f.hit, nil
}
func (f *fakeStore) SaveTokenMeta(ctx context.Context, chain, address string, r Record) error {
	f.saved++
	f.rec, f.hit = r, true
	return nil
}

var fixedNow = time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

func newChecker(m MoralisClient, c Cache, s Store) *Checker {
	ch := NewChecker(m, c, s, "eth", 7*24*time.Hour, 24*time.Hour)
	ch.now = func() time.Time { return fixedNow }
	return ch
}

func TestValidateRedisHitSkipsStoreAndMoralis(t *testing.T) {
	cache := &fakeCache{hit: true, rec: Record{Verified: true, Symbol: "PEPE", Logo: "L"}}
	m := &fakeMoralis{}
	store := &fakeStore{}
	got, err := newChecker(m, cache, store).Validate(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.Symbol != "PEPE" || got.LogoURI != "L" {
		t.Errorf("validation wrong: %+v", got)
	}
	if m.calls != 0 || store.saved != 0 {
		t.Errorf("redis hit should skip moralis(%d)/store(%d)", m.calls, store.saved)
	}
}

func TestValidateFreshStoreHitRepopulatesRedis(t *testing.T) {
	cache := &fakeCache{}
	store := &fakeStore{hit: true, rec: Record{Verified: true, Symbol: "USDC", FetchedAt: fixedNow.Add(-24 * time.Hour)}}
	m := &fakeMoralis{}
	got, err := newChecker(m, cache, store).Validate(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.Symbol != "USDC" {
		t.Errorf("validation wrong: %+v", got)
	}
	if m.calls != 0 {
		t.Errorf("fresh store hit should skip moralis, calls=%d", m.calls)
	}
	if cache.saved != 1 {
		t.Errorf("expected redis repopulated, saved=%d", cache.saved)
	}
}

func TestValidateStaleStoreTriggersMoralisAndPersists(t *testing.T) {
	cache := &fakeCache{}
	store := &fakeStore{hit: true, rec: Record{Verified: false, FetchedAt: fixedNow.Add(-8 * 24 * time.Hour)}}
	m := &fakeMoralis{meta: moralis.Metadata{Symbol: "PEPE", Logo: "L", Decimals: 18, VerifiedContract: true}}
	got, err := newChecker(m, cache, store).Validate(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.Symbol != "PEPE" {
		t.Errorf("stale should refresh from moralis: %+v", got)
	}
	if m.calls != 1 || store.saved != 1 || cache.saved != 1 {
		t.Errorf("expected moralis+persist, m=%d store=%d cache=%d", m.calls, store.saved, cache.saved)
	}
}

func TestValidateMissAllFetchesAndPersists(t *testing.T) {
	cache := &fakeCache{}
	store := &fakeStore{}
	m := &fakeMoralis{meta: moralis.Metadata{Symbol: "PEPE", VerifiedContract: true, PossibleSpam: false}}
	got, err := newChecker(m, cache, store).Validate(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Valid {
		t.Errorf("expected valid: %+v", got)
	}
	if store.saved != 1 || cache.saved != 1 {
		t.Errorf("expected persisted to store+cache, store=%d cache=%d", store.saved, cache.saved)
	}
}

func TestValidatePossibleSpamIsInvalid(t *testing.T) {
	m := &fakeMoralis{meta: moralis.Metadata{VerifiedContract: true, PossibleSpam: true}}
	got, err := newChecker(m, &fakeCache{}, &fakeStore{}).Validate(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if got.Valid {
		t.Error("possible_spam token must be invalid")
	}
}

func TestValidateUnverifiedIsInvalid(t *testing.T) {
	m := &fakeMoralis{meta: moralis.Metadata{VerifiedContract: false, PossibleSpam: false}}
	got, err := newChecker(m, &fakeCache{}, &fakeStore{}).Validate(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if got.Valid {
		t.Error("unverified token must be invalid")
	}
}

func TestValidateMoralisErrorWithStaleUsesStale(t *testing.T) {
	store := &fakeStore{hit: true, rec: Record{Verified: true, Symbol: "USDC", FetchedAt: fixedNow.Add(-30 * 24 * time.Hour)}}
	m := &fakeMoralis{err: errors.New("boom")}
	got, err := newChecker(m, &fakeCache{}, store).Validate(context.Background(), "0xABC")
	if err != nil {
		t.Fatalf("stale fallback should not error: %v", err)
	}
	if !got.Valid || got.Symbol != "USDC" {
		t.Errorf("expected stale verdict used: %+v", got)
	}
}

func TestValidateMoralisErrorWithoutRecordFailsClosed(t *testing.T) {
	m := &fakeMoralis{err: errors.New("boom")}
	got, err := newChecker(m, &fakeCache{}, &fakeStore{}).Validate(context.Background(), "0xABC")
	if err == nil {
		t.Fatal("expected error when no record and moralis fails")
	}
	if got.Valid {
		t.Error("must be invalid when moralis fails and nothing cached")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tokenvalidity/`
Expected: FAIL — build error, `Checker`/`Record`/`NewChecker` etc. undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/tokenvalidity/checker.go`:

```go
// Package tokenvalidity decides whether an unlisted ERC-20 is a legitimate
// token, using a three-tier cache: Redis (hot) -> Postgres (permanent) -> Moralis.
package tokenvalidity

import (
	"context"
	"time"

	"wallet-api/internal/moralis"
	"wallet-api/internal/wallet"
)

// Record is the cached verdict + metadata for one contract.
type Record struct {
	PossibleSpam bool
	Verified     bool
	Symbol       string
	Name         string
	Logo         string
	Decimals     int
	FetchedAt    time.Time
}

// MoralisClient fetches token metadata for a contract.
type MoralisClient interface {
	GetTokenMetadata(ctx context.Context, address string) (moralis.Metadata, error)
}

// Cache is the Redis hot tier (TTL-bounded). Implemented by rediscache.Cache.
type Cache interface {
	LoadTokenMeta(ctx context.Context, chain, address string) (Record, bool, error)
	SaveTokenMeta(ctx context.Context, chain, address string, r Record, ttl time.Duration) error
}

// Store is the permanent Postgres tier. Implemented by store.Postgres.
type Store interface {
	GetTokenMeta(ctx context.Context, chain, address string) (Record, bool, error)
	SaveTokenMeta(ctx context.Context, chain, address string, r Record) error
}

// Checker resolves contract validity through Redis -> Postgres -> Moralis.
type Checker struct {
	moralis  MoralisClient
	cache    Cache
	store    Store
	chain    string
	recheck  time.Duration
	redisTTL time.Duration
	now      func() time.Time
}

// NewChecker builds a Checker. recheck is the Postgres freshness window;
// redisTTL is the Redis hot-cache TTL.
func NewChecker(m MoralisClient, c Cache, s Store, chain string, recheck, redisTTL time.Duration) *Checker {
	return &Checker{moralis: m, cache: c, store: s, chain: chain, recheck: recheck, redisTTL: redisTTL, now: time.Now}
}

// Validate returns the validity + enrichment metadata for an unlisted ERC-20.
func (c *Checker) Validate(ctx context.Context, address string) (wallet.Validation, error) {
	// Tier 1: Redis hot cache.
	if r, ok, err := c.cache.LoadTokenMeta(ctx, c.chain, address); err == nil && ok {
		return validationFromRecord(r), nil
	}

	// Tier 2: Postgres permanent store.
	var stale *Record
	if r, ok, err := c.store.GetTokenMeta(ctx, c.chain, address); err == nil && ok {
		if c.now().Sub(r.FetchedAt) < c.recheck {
			_ = c.cache.SaveTokenMeta(ctx, c.chain, address, r, c.redisTTL)
			return validationFromRecord(r), nil
		}
		rec := r
		stale = &rec
	}

	// Tier 3: Moralis.
	m, err := c.moralis.GetTokenMetadata(ctx, address)
	if err != nil {
		if stale != nil {
			_ = c.cache.SaveTokenMeta(ctx, c.chain, address, *stale, c.redisTTL)
			return validationFromRecord(*stale), nil
		}
		return wallet.Validation{Valid: false}, err
	}
	r := Record{
		PossibleSpam: m.PossibleSpam,
		Verified:     m.VerifiedContract,
		Symbol:       m.Symbol,
		Name:         m.Name,
		Logo:         m.Logo,
		Decimals:     m.Decimals,
		FetchedAt:    c.now().UTC(),
	}
	_ = c.store.SaveTokenMeta(ctx, c.chain, address, r)
	_ = c.cache.SaveTokenMeta(ctx, c.chain, address, r, c.redisTTL)
	return validationFromRecord(r), nil
}

// validationFromRecord applies the validity rule and maps metadata for enrichment.
func validationFromRecord(r Record) wallet.Validation {
	return wallet.Validation{
		Valid:    !r.PossibleSpam && r.Verified,
		Symbol:   r.Symbol,
		Name:     r.Name,
		LogoURI:  r.Logo,
		Decimals: r.Decimals,
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tokenvalidity/`
Expected: PASS (8 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/tokenvalidity/
git commit -m "feat(tokenvalidity): three-tier Redis/Postgres/Moralis checker"
```

---

## Task 4: Postgres `token_metadata` store

**Files:**
- Modify: `internal/store/schema.sql`
- Modify: `internal/store/store.go`
- Modify: `internal/store/store_test.go`

**Interfaces:**
- Consumes: `tokenvalidity.Record` (Task 3).
- Produces (satisfies `tokenvalidity.Store`):
  - `func (s *Postgres) GetTokenMeta(ctx context.Context, chain, address string) (tokenvalidity.Record, bool, error)`
  - `func (s *Postgres) SaveTokenMeta(ctx context.Context, chain, address string, r tokenvalidity.Record) error`

- [ ] **Step 1: Write the failing test**

In `internal/store/store_test.go`, extend the TRUNCATE in `newTestStore` to include the new table:

```go
	_, _ = s.pool.Exec(ctx, "TRUNCATE wallet_tokens, token_fetch_meta, tx_cache, lifi_token_lists, token_metadata")
```

Add `"wallet-api/internal/tokenvalidity"` to the imports, and add this test:

```go
func TestSaveAndGetTokenMeta(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Second)
	r := tokenvalidity.Record{PossibleSpam: false, Verified: true, Symbol: "PEPE", Name: "Pepe", Logo: "https://logo/pepe.png", Decimals: 18, FetchedAt: at}

	if _, ok, err := s.GetTokenMeta(ctx, "eth", "0xFEE7"); err != nil || ok {
		t.Fatalf("empty get: ok=%v err=%v", ok, err)
	}
	if err := s.SaveTokenMeta(ctx, "eth", "0xFEE7", r); err != nil {
		t.Fatalf("SaveTokenMeta: %v", err)
	}
	got, ok, err := s.GetTokenMeta(ctx, "eth", "0xFEE7")
	if err != nil || !ok {
		t.Fatalf("GetTokenMeta ok=%v err=%v", ok, err)
	}
	if got.Symbol != "PEPE" || got.Logo != "https://logo/pepe.png" || got.Decimals != 18 || !got.Verified || got.PossibleSpam {
		t.Errorf("round-trip wrong: %+v", got)
	}
	if !got.FetchedAt.Equal(at) {
		t.Errorf("fetchedAt = %v, want %v", got.FetchedAt, at)
	}

	// Upsert overwrites.
	r2 := r
	r2.PossibleSpam = true
	r2.Symbol = "SPAM"
	if err := s.SaveTokenMeta(ctx, "eth", "0xFEE7", r2); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got2, _, _ := s.GetTokenMeta(ctx, "eth", "0xFEE7")
	if !got2.PossibleSpam || got2.Symbol != "SPAM" {
		t.Errorf("upsert did not overwrite: %+v", got2)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `WALLET_TEST_DATABASE_URL=... go test ./internal/store/ -run TestSaveAndGetTokenMeta`
Expected: FAIL — `GetTokenMeta`/`SaveTokenMeta` undefined (and `token_metadata` table missing). If no DB is available the test SKIPs; verify compilation instead with `go vet ./internal/store/` (expected: build error until Step 3).

- [ ] **Step 3: Write minimal implementation**

In `internal/store/schema.sql`, append:

```sql

CREATE TABLE IF NOT EXISTS token_metadata (
    chain         TEXT NOT NULL,
    token_address TEXT NOT NULL,
    possible_spam BOOLEAN NOT NULL,
    verified      BOOLEAN NOT NULL,
    symbol        TEXT NOT NULL DEFAULT '',
    name          TEXT NOT NULL DEFAULT '',
    logo          TEXT NOT NULL DEFAULT '',
    decimals      INTEGER NOT NULL DEFAULT 0,
    fetched_at    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (chain, token_address)
);
```

In `internal/store/store.go`, add `"wallet-api/internal/tokenvalidity"` to the import block, and add these methods (e.g. after `LoadTokenList`):

```go
// GetTokenMeta returns the stored Moralis verdict/metadata for a contract, if
// present. No TTL filter — freshness is decided by the caller so a stale row can
// still serve as a fallback.
func (s *Postgres) GetTokenMeta(ctx context.Context, chain, address string) (tokenvalidity.Record, bool, error) {
	address = strings.ToLower(address)
	var r tokenvalidity.Record
	err := s.pool.QueryRow(ctx, `
		SELECT possible_spam, verified, symbol, name, logo, decimals, fetched_at
		FROM token_metadata WHERE chain=$1 AND token_address=$2`, chain, address).
		Scan(&r.PossibleSpam, &r.Verified, &r.Symbol, &r.Name, &r.Logo, &r.Decimals, &r.FetchedAt)
	if err == pgx.ErrNoRows {
		return tokenvalidity.Record{}, false, nil
	}
	if err != nil {
		return tokenvalidity.Record{}, false, err
	}
	return r, true, nil
}

// SaveTokenMeta upserts a Moralis verdict/metadata record (kept permanently).
func (s *Postgres) SaveTokenMeta(ctx context.Context, chain, address string, r tokenvalidity.Record) error {
	address = strings.ToLower(address)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO token_metadata
		(chain, token_address, possible_spam, verified, symbol, name, logo, decimals, fetched_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (chain, token_address) DO UPDATE SET
		  possible_spam=EXCLUDED.possible_spam, verified=EXCLUDED.verified,
		  symbol=EXCLUDED.symbol, name=EXCLUDED.name, logo=EXCLUDED.logo,
		  decimals=EXCLUDED.decimals, fetched_at=EXCLUDED.fetched_at`,
		chain, address, r.PossibleSpam, r.Verified, r.Symbol, r.Name, r.Logo, r.Decimals, r.FetchedAt)
	return err
}
```

- [ ] **Step 4: Run test to verify it passes**

Run (with a test DB): `WALLET_TEST_DATABASE_URL=postgres://... go test ./internal/store/ -run TestSaveAndGetTokenMeta -v`
Expected: PASS. Without a DB: `go build ./internal/store/` and `go vet ./internal/store/` succeed (test SKIPs).

- [ ] **Step 5: Commit**

```bash
git add internal/store/
git commit -m "feat(store): persist Moralis token metadata in token_metadata"
```

---

## Task 5: Redis hot cache for verdicts

**Files:**
- Modify: `internal/rediscache/cache.go`
- Modify: `internal/rediscache/cache_test.go`

**Interfaces:**
- Consumes: `tokenvalidity.Record` (Task 3).
- Produces (satisfies `tokenvalidity.Cache`):
  - `func (c *Cache) LoadTokenMeta(ctx context.Context, chain, address string) (tokenvalidity.Record, bool, error)`
  - `func (c *Cache) SaveTokenMeta(ctx context.Context, chain, address string, r tokenvalidity.Record, ttl time.Duration) error`

- [ ] **Step 1: Write the failing test**

In `internal/rediscache/cache_test.go`, add `"wallet-api/internal/tokenvalidity"` to imports and add:

```go
func TestRedisSaveAndLoadTokenMeta(t *testing.T) {
	c := newTestCache(t)
	ctx := context.Background()
	_, _ = c.client.Del(ctx, metaKey("eth", "0xfee7")).Result()

	if _, ok, err := c.LoadTokenMeta(ctx, "eth", "0xFEE7"); err != nil || ok {
		t.Fatalf("empty load: ok=%v err=%v", ok, err)
	}
	at := time.Now().UTC().Truncate(time.Second)
	r := tokenvalidity.Record{Verified: true, Symbol: "PEPE", Logo: "L", Decimals: 18, FetchedAt: at}
	if err := c.SaveTokenMeta(ctx, "eth", "0xFEE7", r, time.Hour); err != nil {
		t.Fatalf("SaveTokenMeta: %v", err)
	}
	got, ok, err := c.LoadTokenMeta(ctx, "eth", "0xFEE7")
	if err != nil || !ok {
		t.Fatalf("LoadTokenMeta ok=%v err=%v", ok, err)
	}
	if got.Symbol != "PEPE" || got.Logo != "L" || got.Decimals != 18 || !got.Verified {
		t.Errorf("round-trip wrong: %+v", got)
	}
	if !got.FetchedAt.Equal(at) {
		t.Errorf("fetchedAt = %v, want %v", got.FetchedAt, at)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go vet ./internal/rediscache/`
Expected: FAIL — `metaKey`/`LoadTokenMeta`/`SaveTokenMeta` undefined. (Test itself SKIPs without `WALLET_TEST_REDIS_URL`.)

- [ ] **Step 3: Write minimal implementation**

In `internal/rediscache/cache.go`, add `"strings"` to imports and `"wallet-api/internal/tokenvalidity"`, then add:

```go
func metaKey(chain, address string) string {
	return "tokenmeta:" + chain + ":" + strings.ToLower(address)
}

// SaveTokenMeta caches a verdict/metadata record with the given TTL.
func (c *Cache) SaveTokenMeta(ctx context.Context, chain, address string, r tokenvalidity.Record, ttl time.Duration) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return c.client.Set(ctx, metaKey(chain, address), b, ttl).Err()
}

// LoadTokenMeta returns the cached verdict/metadata record, if present.
func (c *Cache) LoadTokenMeta(ctx context.Context, chain, address string) (tokenvalidity.Record, bool, error) {
	b, err := c.client.Get(ctx, metaKey(chain, address)).Bytes()
	if err == redis.Nil {
		return tokenvalidity.Record{}, false, nil
	}
	if err != nil {
		return tokenvalidity.Record{}, false, err
	}
	var r tokenvalidity.Record
	if err := json.Unmarshal(b, &r); err != nil {
		return tokenvalidity.Record{}, false, err
	}
	return r, true, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run (with test Redis): `WALLET_TEST_REDIS_URL=redis://localhost:6379/0 go test ./internal/rediscache/ -run TestRedisSaveAndLoadTokenMeta -v`
Expected: PASS. Without Redis: `go build ./internal/rediscache/` and `go vet ./internal/rediscache/` succeed (test SKIPs).

- [ ] **Step 5: Commit**

```bash
git add internal/rediscache/
git commit -m "feat(rediscache): hot cache for token verdicts"
```

---

## Task 6: Moralis configuration

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

**Interfaces:**
- Produces (new `config.Config` fields): `MoralisAPIKey string`, `MoralisChain string`, `MoralisRecheck time.Duration`, `MoralisRedisTTL time.Duration`.

- [ ] **Step 1: Write the failing test**

In `internal/config/config_test.go`:

Add `MORALIS_API_KEY` to the env maps in **every** existing test that expects success (`TestLoadFromAppliesDefaults`, `TestLoadFromHonoursOverrides`, `TestLoadFromRejectsBadRefresh`, `TestLoadFromRejectsNonIntegerRefresh`) — add the line `"MORALIS_API_KEY": "mkey",` to each `env` map.

In `TestLoadFromAppliesDefaults`, add assertions:

```go
	if cfg.MoralisChain != "eth" {
		t.Errorf("moralis chain = %q, want eth", cfg.MoralisChain)
	}
	if cfg.MoralisRecheck != 604800*time.Second {
		t.Errorf("moralis recheck = %v, want 604800s", cfg.MoralisRecheck)
	}
	if cfg.MoralisRedisTTL != 86400*time.Second {
		t.Errorf("moralis redis ttl = %v, want 86400s", cfg.MoralisRedisTTL)
	}
```

In `TestLoadFromHonoursOverrides`, add to the `env` map:

```go
		"MORALIS_API_KEY":           "mkey",
		"MORALIS_CHAIN":             "polygon",
		"MORALIS_RECHECK_SECONDS":   "120",
		"MORALIS_REDIS_TTL_SECONDS": "60",
```

and assertions:

```go
	if cfg.MoralisChain != "polygon" || cfg.MoralisRecheck != 120*time.Second || cfg.MoralisRedisTTL != 60*time.Second {
		t.Errorf("moralis overrides not applied: %+v", cfg)
	}
```

Add a new test for the required key:

```go
func TestLoadFromRequiresMoralisKey(t *testing.T) {
	env := map[string]string{
		"ALCHEMY_API_KEY": "key123",
		"DATABASE_URL":    "postgres://db",
		"REDIS_URL":       "redis://localhost:6379",
	}
	if _, err := loadFrom(func(k string) string { return env[k] }); err == nil {
		t.Fatal("expected error when MORALIS_API_KEY missing")
	}
}

func TestLoadFromRejectsBadMoralisRecheck(t *testing.T) {
	env := map[string]string{
		"ALCHEMY_API_KEY":         "key123",
		"DATABASE_URL":            "postgres://db",
		"REDIS_URL":               "redis://localhost:6379",
		"MORALIS_API_KEY":         "mkey",
		"MORALIS_RECHECK_SECONDS": "0",
	}
	if _, err := loadFrom(func(k string) string { return env[k] }); err == nil {
		t.Fatal("expected error for non-positive MORALIS_RECHECK_SECONDS")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/`
Expected: FAIL — `MoralisChain`/`MoralisRecheck`/`MoralisRedisTTL`/`MoralisAPIKey` undefined and required-key test failing.

- [ ] **Step 3: Write minimal implementation**

In `internal/config/config.go`, add fields to `Config`:

```go
	MoralisAPIKey   string
	MoralisChain    string
	MoralisRecheck  time.Duration
	MoralisRedisTTL time.Duration
```

In `loadFrom`, after the existing required-var checks, add:

```go
	cfg.MoralisAPIKey = getenv("MORALIS_API_KEY")
	if cfg.MoralisAPIKey == "" {
		return Config{}, fmt.Errorf("MORALIS_API_KEY is required")
	}
	cfg.MoralisChain = getenv("MORALIS_CHAIN")
	if cfg.MoralisChain == "" {
		cfg.MoralisChain = "eth"
	}
	recheck := 604800
	if raw := getenv("MORALIS_RECHECK_SECONDS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("MORALIS_RECHECK_SECONDS must be a positive integer, got %q", raw)
		}
		recheck = n
	}
	cfg.MoralisRecheck = time.Duration(recheck) * time.Second
	redisTTL := 86400
	if raw := getenv("MORALIS_REDIS_TTL_SECONDS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("MORALIS_REDIS_TTL_SECONDS must be a positive integer, got %q", raw)
		}
		redisTTL = n
	}
	cfg.MoralisRedisTTL = time.Duration(redisTTL) * time.Second
```

(Add these before the final `return cfg, nil`. `strconv`, `fmt`, and `time` are already imported.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/
git commit -m "feat(config): add required MORALIS_API_KEY + chain/TTL settings"
```

---

## Task 7: Wire the checker into the server

**Files:**
- Modify: `cmd/server/main.go`

**Interfaces:**
- Consumes: `moralis.New` (Task 1), `tokenvalidity.NewChecker` (Task 3), the new `wallet.NewService` signature (Task 2), the new config fields (Task 6).

- [ ] **Step 1: Add imports and wiring**

In `cmd/server/main.go`, add to the import block:

```go
	"wallet-api/internal/moralis"
	"wallet-api/internal/tokenvalidity"
```

Replace the service-construction block:

```go
	ac := alchemy.New(cfg.AlchemyAPIKey, cfg.AlchemyNetwork)
	svc := wallet.NewService(ac, pg, pg, holder, cfg.AlchemyNetwork, cfg.CacheTTL)
```

with:

```go
	ac := alchemy.New(cfg.AlchemyAPIKey, cfg.AlchemyNetwork)
	moralisClient := moralis.New(cfg.MoralisAPIKey, cfg.MoralisChain)
	validator := tokenvalidity.NewChecker(moralisClient, redisCache, pg, cfg.MoralisChain, cfg.MoralisRecheck, cfg.MoralisRedisTTL)
	svc := wallet.NewService(ac, pg, pg, holder, validator, cfg.AlchemyNetwork, cfg.CacheTTL)
```

- [ ] **Step 2: Verify the whole project builds and all tests pass**

Run: `go build ./... && go vet ./...`
Expected: success, no output.

Run: `go test ./...`
Expected: PASS (integration tests for `store`/`rediscache` SKIP unless `WALLET_TEST_DATABASE_URL` / `WALLET_TEST_REDIS_URL` are set).

- [ ] **Step 3: Verify docs are unaffected**

Run: `make docs-check`
Expected: PASS (no handler/annotation changes were made).

- [ ] **Step 4: Commit**

```bash
git add cmd/server/main.go
git commit -m "feat(server): wire Moralis spam filter into the wallet service"
```

---

## Final verification (after all tasks)

- [ ] `go build ./...` — succeeds.
- [ ] `go vet ./...` — clean.
- [ ] `go test ./...` — all pass (DB/Redis integration tests SKIP without env vars; run them with `WALLET_TEST_DATABASE_URL` and `WALLET_TEST_REDIS_URL` set against a throwaway DB/Redis to confirm).
- [ ] `make docs-check` — passes.
- [ ] Manual sanity: with `MORALIS_API_KEY` unset, the server fails to start with `MORALIS_API_KEY is required`.

## README note (fold into Task 7 commit or a follow-up)

Add the new environment variables to the README's configuration section:
`MORALIS_API_KEY` (required), `MORALIS_CHAIN` (default `eth`),
`MORALIS_RECHECK_SECONDS` (default `604800`), `MORALIS_REDIS_TTL_SECONDS`
(default `86400`).

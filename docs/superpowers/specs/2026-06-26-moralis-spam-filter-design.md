# Moralis spam filter for unlisted tokens — Design

**Date:** 2026-06-26
**Status:** Approved (design); implementation plan to follow

## Summary

Today `wallet.Service.filterTokens` keeps native tokens and ERC-20s present in
the LI.FI allowlist, and **silently drops every other ERC-20**. This change adds
a second-chance gate: an unlisted ERC-20 is checked against the
[Moralis token-metadata API](https://docs.moralis.com/data-api/evm/token/metadata/token-metadata).
If Moralis reports the contract is **not** `possible_spam` **and** is a
`verified_contract`, the token is valid and returned (enriched with Moralis
metadata); otherwise it is invalid and dropped. Verdicts and metadata are cached
in a three-tier lookup — Redis (1-day hot cache) → Postgres (permanent source of
truth, re-checked weekly) → Moralis — so a contract hits Moralis at most once per
week and is usually served from Redis.

## Goals

- Legitimate-but-unlisted ERC-20s are no longer hidden — they pass if Moralis
  vouches for them, and are returned with Moralis metadata (logo/symbol/name/
  decimals) in the same enriched shape as LI.FI-listed tokens.
- Spam / unverified contracts are never returned.
- Cheap in steady state: served from Redis; Moralis is hit at most once per
  contract per week.
- Isolated, testable units with clear boundaries.

## Non-goals

- No change to native-token or LI.FI-allowlisted handling (still kept/enriched
  exactly as today).
- No exposure of validity in the API response — invalid tokens are simply
  omitted (the `Token` JSON shape is unchanged).
- No multi-chain support beyond the single configured chain.
- No extra Moralis price call: `priceUSD`/`coinKey` are left empty for
  unlisted-but-valid tokens (see Decisions). The Alchemy `price` already on the
  token is retained.
- No batching/parallelism of Moralis calls in the MVP (see Future options).

## Background

`wallet.Service.GetTokens` is cache-first: it loads a Postgres snapshot (or
fetches from Alchemy and writes through), then calls `filterTokens` on every read
— both the cache-hit and fresh paths. `filterTokens`
(`internal/wallet/service.go`) currently keeps native tokens, looks up each
ERC-20 in the injected `Allowlist` (LI.FI list via `tokenlist.Holder`); listed →
keep + `enrichToken`, unlisted → drop.

The persisted `wallet_tokens` snapshot contains the **full** normalized token set
(including unlisted junk); filtering is applied at read time. This design
preserves that: the new validity check also runs at read time, and verdicts live
in their own cache tiers independent of `wallet_tokens`.

**Base branch:** main.

## Components

### 1. `internal/moralis` — token-metadata HTTP client

A thin client following the existing `alchemy` / `lifi` pattern (an
`*http.Client` with a timeout, JSON decode, non-2xx → error).

```go
type Client struct { apiKey, chain, baseURL string; httpClient *http.Client }

type Metadata struct {
    Symbol           string
    Name             string
    Logo             string
    Decimals         int
    PossibleSpam     bool
    VerifiedContract bool
}

// GetTokenMetadata fetches metadata for a single ERC-20 contract.
func (c *Client) GetTokenMetadata(ctx context.Context, address string) (Metadata, error)
```

- Request: `GET {baseURL}/erc20/metadata?chain={chain}&addresses[]={address}`,
  header `X-API-Key: {apiKey}`. Default `baseURL` =
  `https://deep-index.moralis.io/api/v2.2`.
- Response is a JSON array; the client reads element `[0]`, mapping `symbol`,
  `name`, `logo`, `decimals` (returned as a string — parsed to int),
  `possible_spam`, `verified_contract`. All other fields ignored. An empty array
  → error (contract not resolvable).

### 2. `internal/tokenvalidity` — three-tier cache-first checker

Composes a Redis hot cache, a Postgres permanent store, and the Moralis client.
It owns the lookup order, the freshness rule, and the validity rule.

```go
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

type MoralisClient interface {
    GetTokenMetadata(ctx context.Context, address string) (moralis.Metadata, error)
}
type Cache interface { // implemented by rediscache.Cache
    LoadTokenMeta(ctx context.Context, chain, address string) (Record, bool, error)
    SaveTokenMeta(ctx context.Context, chain, address string, r Record, ttl time.Duration) error
}
type Store interface { // implemented by store.Postgres
    GetTokenMeta(ctx context.Context, chain, address string) (Record, bool, error)
    SaveTokenMeta(ctx context.Context, chain, address string, r Record) error
}

type Checker struct {
    moralis   MoralisClient
    cache     Cache
    store     Store
    chain     string
    recheck   time.Duration // Postgres freshness window (1 week)
    redisTTL  time.Duration // Redis hot-cache TTL (1 day)
    now       func() time.Time
}

// Validate returns the validity + enrichment metadata for an unlisted ERC-20.
func (c *Checker) Validate(ctx context.Context, address string) (wallet.Validation, error)
```

`tokenvalidity` imports `wallet` and returns `wallet.Validation`, mirroring how
`store` imports `wallet`. The validity rule
`Valid = !PossibleSpam && Verified` lives here.

**Lookup order (`Validate`):**

1. **Redis** `LoadTokenMeta` — hit → build `Validation` from the record, return.
   (Redis entries last 1 day; both valid and invalid verdicts are cached.)
2. **Redis miss → Postgres** `GetTokenMeta`:
   - found and `now - FetchedAt < recheck` (1 week) → write back to Redis
     (`redisTTL`), return.
   - found but **stale** (≥ 1 week) → keep the record as a fallback, go to
     Moralis.
   - not found → go to Moralis.
3. **Moralis** `GetTokenMetadata`:
   - success → build `Record`, `store.SaveTokenMeta` (permanent upsert),
     `cache.SaveTokenMeta` (1 day), return.
   - error → if a stale Postgres record exists, refresh Redis from it and return
     it (resilient); otherwise return `(Validation{Valid:false}, err)` so the
     caller drops the token (fail-closed).

Cache/Postgres **read** errors are treated as a miss (fall through to the next
tier). Postgres/Redis **write** errors are best-effort and ignored (the verdict
is still returned).

### 3. `wallet.Service` — new `Validator` dependency

```go
// Validator decides whether an unlisted ERC-20 is legitimate and supplies
// enrichment metadata when it is.
type Validator interface {
    Validate(ctx context.Context, address string) (Validation, error)
}
type Validation struct {
    Valid    bool
    Symbol   string
    Name     string
    LogoURI  string
    Decimals int
}
```

Injected via `NewService` (new last parameter). `filterTokens` becomes
`ctx`-aware:

```
for each token:
  native             → keep
  in LI.FI allowlist → keep + enrichToken            (unchanged)
  unlisted ERC-20    → v, err := validator.Validate(ctx, addr)
                         v.Valid && err == nil → keep + enrichFromValidation(t, v)
                         otherwise             → drop  (fail-closed)
```

`enrichFromValidation` overlays `Symbol`, `Name`, `LogoURI`, and `Decimals` onto
the token (re-deriving `Balance` from `RawBalance` when decimals change, exactly
like `enrichToken`). `CoinKey` and `PriceUSD` are left empty (no Moralis source);
the token keeps its Alchemy `Price`.

`filterTokens` gains a `ctx` parameter; both call sites in `GetTokens` pass it.

### 4. Postgres permanent store (`token_metadata`)

Added to `internal/store/schema.sql` (idempotent, applied by the existing
`Migrate`). Rows are **never deleted** — kept forever and refreshed in place
(upsert) when a weekly re-check fetches fresh data.

```sql
CREATE TABLE IF NOT EXISTS token_metadata (
    chain         TEXT NOT NULL,
    token_address TEXT NOT NULL,
    possible_spam BOOLEAN NOT NULL,
    verified      BOOLEAN NOT NULL,
    symbol        TEXT,
    name          TEXT,
    logo          TEXT,
    decimals      INTEGER,
    fetched_at    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (chain, token_address)
);
```

Two new `store.Postgres` methods implement `tokenvalidity.Store`:

- `GetTokenMeta(ctx, chain, address)` — `SELECT ... WHERE chain=$1 AND
  token_address=$2` (no TTL filter; freshness is decided in the Checker so a
  stale row can still serve as a Moralis-failure fallback). `token_address`
  lowercased like other tables.
- `SaveTokenMeta(ctx, chain, address, record)` — upsert
  `ON CONFLICT (chain, token_address) DO UPDATE`.

### 5. Redis hot cache (`rediscache`)

Two new methods on `rediscache.Cache` implement `tokenvalidity.Cache`, keyed
`tokenmeta:{chain}:{address}`, with the TTL passed explicitly per call (1 day),
independent of the existing token-list `ttl`:

- `LoadTokenMeta(ctx, chain, address) (Record, bool, error)`
- `SaveTokenMeta(ctx, chain, address, record, ttl)`

Both valid and invalid verdicts are cached so known spam is not re-queried.

### 6. Config (`internal/config`)

- `MORALIS_API_KEY` — **required** (like `ALCHEMY_API_KEY`); `Load` errors if
  empty.
- `MORALIS_CHAIN` — default `eth`.
- `MORALIS_RECHECK_SECONDS` — Postgres freshness window, positive int, default
  `604800` (1 week).
- `MORALIS_REDIS_TTL_SECONDS` — Redis hot-cache TTL, positive int, default
  `86400` (1 day).

`*_SECONDS` vars are validated like the existing ones.

### 7. Wiring (`cmd/server/main.go`)

Build `moralis.New(cfg.MoralisAPIKey, cfg.MoralisChain)`, wrap with
`tokenvalidity.NewChecker(moralisClient, redisCache, pg, cfg.MoralisChain,
cfg.MoralisRecheck, cfg.MoralisRedisTTL)`, and pass the checker as the new
`Validator` argument to `wallet.NewService`.

## Data flow

```
GetTokens → (cache-hit or fresh) → filterTokens(ctx, portfolio)
                                        │
        ┌───────────────┬──────────────┴───────────────────────┐
        ▼               ▼                                       ▼
     native        in LI.FI list                          unlisted ERC-20
      keep          keep + enrich                    validator.Validate(ctx, addr)
                                                              │
                          Redis(1d) ─hit──────────────────────┤
                             │ miss                            │
                          Postgres(permanent) ─found&<1wk──────┤
                             │ missing / ≥1wk stale            │
                          Moralis ─ok→ upsert PG + write Redis ┤
                             │ err & stale PG row → use stale  │
                             │ err & no row → invalid          │
                                                              ▼
                                       Valid → keep + enrichFromValidation
                                       else  → drop
```

## Error handling

- **Redis / Postgres read error** → treated as a cache miss; fall through to the
  next tier. Never fails the request.
- **Moralis error** → if a (stale) Postgres record exists, use it (resilient to
  Moralis outages while honoring "keep forever"); otherwise drop the token
  (fail-closed). One bad token never fails the whole `GetTokens` request.
- **Empty Moralis array** → treated as an error (same fallback/drop path).
- **Postgres / Redis write error** → best-effort; ignored, the verdict is still
  returned for this request.
- `MORALIS_API_KEY` missing at startup → `config.Load` fails fast, matching
  `ALCHEMY_API_KEY`.

## Testing

- **`moralis`**: `httptest` server returns a canned metadata array — assert
  `symbol/name/logo/decimals` (string→int) and the two booleans parsed; non-2xx
  and empty-array → error.
- **`tokenvalidity`**: fake Moralis + fake Cache + fake Store with an injectable
  clock — Redis hit (no PG/Moralis call); Redis miss → PG fresh; PG stale →
  Moralis → persists PG + Redis; all-miss → Moralis → persists; Moralis error
  with stale PG row → returns stale; Moralis error with no row → `(invalid, err)`;
  `Valid = !possible_spam && verified` truth table; enrichment fields populated.
- **`rediscache`**: `LoadTokenMeta`/`SaveTokenMeta` round-trip and miss.
- **`store`**: `GetTokenMeta`/`SaveTokenMeta` round-trip and upsert-overwrite,
  following the existing `store_test.go` style.
- **`wallet`**: fake `Validator` (valid-with-metadata / invalid / error) —
  unlisted valid token kept **and enriched** with Moralis logo/symbol/name/
  decimals; invalid or error dropped; native always kept; listed token still
  LI.FI-enriched. Update `internal/wallet/fakes_test.go` and `NewService` call
  sites.
- Full `go build ./...`, `go vet ./...`, `go test ./...`, and `make docs-check`
  still pass.

## Decisions

- **Second-chance gate, not a blanket re-check:** Moralis is consulted only for
  ERC-20s missing from the LI.FI allowlist. Listed tokens are trusted and
  enriched as before; native tokens are always kept.
- **Enrich unlisted-but-valid from Moralis metadata:** `logo → LogoURI`,
  `symbol/name/decimals` overlaid so the token matches the enriched LI.FI shape.
- **No extra price call:** Moralis token-metadata has no `priceUSD` and no
  `coinKey`; both are left empty. The Alchemy `Price` already on the token is
  retained, so no price information is lost.
- **Three-tier cache, permanent Postgres:** Redis (1 day) for speed; Postgres
  kept forever as the source of truth, re-checked against Moralis weekly; both
  valid and invalid verdicts cached so spam is not re-queried.
- **Fail-closed, with stale fallback:** on a Moralis error we never return an
  unknown token, but an existing (stale) Postgres verdict is still honored so a
  Moralis outage doesn't hide previously-validated tokens.
- **Internal validity only:** the verdict decides keep/drop and is not surfaced
  in the API response — the `Token` JSON shape is unchanged.
- **`MORALIS_API_KEY` required:** the feature is always on; no silent
  degradation path.

## Future options (out of scope)

- **Batch Moralis lookups:** the metadata endpoint accepts up to 25 `addresses[]`
  per call — fetch all uncached unlisted contracts in one request.
- **Parallel per-token checks** with a bounded worker pool.
- **Moralis price endpoint** (`/erc20/{address}/price`) to populate `priceUSD`.
- Surface validity as an optional response field for clients that want to display
  flagged tokens.
- Periodic background refresh of stale verdicts instead of lazy on-read refresh.

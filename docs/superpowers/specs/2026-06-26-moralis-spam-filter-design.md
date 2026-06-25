# Moralis spam filter for unlisted tokens — Design

**Date:** 2026-06-26
**Status:** Approved (design); implementation plan to follow

## Summary

Today `wallet.Service.filterTokens` keeps native tokens and ERC-20s present in
the LI.FI allowlist, and **silently drops every other ERC-20**. This change adds
a second-chance gate: an unlisted ERC-20 is checked against the
[Moralis token-metadata API](https://docs.moralis.com/data-api/evm/token/metadata/token-metadata).
If Moralis reports the contract is **not** `possible_spam` **and** is a
`verified_contract`, the token is treated as valid and returned; otherwise it is
invalid and dropped. Verdicts are cached in Postgres so the per-token Moralis
call happens at most once per contract per TTL window.

## Goals

- Legitimate-but-unlisted ERC-20s are no longer hidden — they pass if Moralis
  vouches for them.
- Spam / unverified contracts are never returned.
- Cheap in steady state: a contract is fetched from Moralis once, then served
  from a Postgres verdict cache until the verdict goes stale.
- Isolated, testable units with clear boundaries.

## Non-goals

- No change to native-token or LI.FI-allowlisted handling (still kept/enriched
  exactly as today).
- No exposure of validity in the API response — invalid tokens are simply
  omitted (the JSON `Token` shape is unchanged).
- No multi-chain support beyond the single configured chain.
- No batching/parallelism of Moralis calls in the MVP (see Future options).

## Background

`wallet.Service.GetTokens` is cache-first: it loads a Postgres snapshot (or
fetches from Alchemy and writes through), then calls `filterTokens` on every read
— both the cache-hit and fresh paths. `filterTokens`
(`internal/wallet/service.go`) currently:

- keeps native tokens (`IsNative` / `TokenAddress == nil`),
- looks up each ERC-20 in the injected `Allowlist` (LI.FI list, held by
  `tokenlist.Holder`); listed → keep + `enrichToken`, unlisted → drop.

The persisted `wallet_tokens` snapshot contains the **full** normalized token set
(including unlisted junk); filtering is applied at read time. This design
preserves that: the new validity check also runs at read time, and verdicts live
in their own table independent of `wallet_tokens`.

**Base branch:** main.

## Components

### 1. `internal/moralis` — token-metadata HTTP client

A thin client following the existing `alchemy` / `lifi` pattern (an
`*http.Client` with a timeout, JSON decode, non-2xx → error).

```go
type Client struct { apiKey, chain, baseURL string; httpClient *http.Client }

type Metadata struct {
    PossibleSpam     bool
    VerifiedContract bool
}

// GetTokenMetadata fetches metadata for a single ERC-20 contract.
func (c *Client) GetTokenMetadata(ctx context.Context, address string) (Metadata, error)
```

- Request: `GET {baseURL}/erc20/metadata?chain={chain}&addresses[]={address}`,
  header `X-API-Key: {apiKey}`. Default `baseURL` =
  `https://deep-index.moralis.io/api/v2.2`.
- Response is a JSON array; the client reads element `[0]`, mapping
  `possible_spam` and `verified_contract` (all other fields ignored). An empty
  array is an error (contract not resolvable).

### 2. `internal/tokenvalidity` — cache-first validity checker

Composes the Moralis client and a Postgres-backed verdict store, the same way
`wallet.Service` composes Alchemy + store. It owns the cache-first logic and the
validity rule.

```go
type MoralisClient interface {
    GetTokenMetadata(ctx context.Context, address string) (moralis.Metadata, error)
}

type VerdictStore interface {
    GetSpamVerdict(ctx context.Context, chain, address string, ttl time.Duration) (Verdict, bool, error)
    SaveSpamVerdict(ctx context.Context, chain, address string, possibleSpam, verified bool, fetchedAt time.Time) error
}

type Checker struct { moralis MoralisClient; store VerdictStore; chain string; ttl time.Duration; now func() time.Time }

// IsValid reports whether an unlisted ERC-20 is a legitimate token.
func (c *Checker) IsValid(ctx context.Context, address string) (bool, error)
```

`IsValid` logic:

1. `GetSpamVerdict`; if found and fresh → return `!possibleSpam && verified`.
2. Otherwise `GetTokenMetadata`; on success `SaveSpamVerdict`, return
   `!PossibleSpam && VerifiedContract`.
3. On any Moralis error, return `(false, err)` — the caller fails closed.

The validity rule `valid = !possible_spam && verified_contract` lives here.

### 3. `wallet.Service` — new `Validator` dependency

```go
// Validator decides whether an unlisted ERC-20 contract is a legitimate token.
type Validator interface {
    IsValid(ctx context.Context, address string) (bool, error)
}
```

Injected via `NewService` (new last parameter). `filterTokens` becomes
`ctx`-aware:

```
for each token:
  native             → keep
  in LI.FI allowlist → keep + enrich            (unchanged)
  unlisted ERC-20    → valid, err := validator.IsValid(ctx, addr)
                         valid && err == nil → keep (Alchemy metadata only)
                         otherwise           → drop   (fail-closed)
```

`filterTokens` gains a `ctx` parameter; both call sites in `GetTokens` pass it.
Unlisted-but-valid tokens are returned as-is (no LI.FI overlay, so no
`LogoURI` / `CoinKey` / `PriceUSD`).

### 4. Postgres verdict table

Added to `internal/store/schema.sql` (idempotent, applied by the existing
`Migrate`):

```sql
CREATE TABLE IF NOT EXISTS token_spam_status (
    chain         TEXT NOT NULL,
    token_address TEXT NOT NULL,
    possible_spam BOOLEAN NOT NULL,
    verified      BOOLEAN NOT NULL,
    fetched_at    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (chain, token_address)
);
```

Two new `store.Postgres` methods implement `tokenvalidity.VerdictStore`:

- `GetSpamVerdict(ctx, chain, address, ttl)` — `SELECT ... WHERE chain=$1 AND
  token_address=$2 AND fetched_at > now() - $3::interval`, returning
  `(Verdict, found, err)` (uses the existing `intervalArg` helper;
  `token_address` lowercased like other tables).
- `SaveSpamVerdict(ctx, chain, address, possibleSpam, verified, fetchedAt)` —
  upsert `ON CONFLICT (chain, token_address) DO UPDATE`.

Raw booleans are stored (not a precomputed `valid`) so the verdict is debuggable
and the rule can change without a re-fetch.

### 5. Config (`internal/config`)

- `MORALIS_API_KEY` — **required** (like `ALCHEMY_API_KEY`); `Load` returns an
  error if empty.
- `MORALIS_CHAIN` — default `eth`.
- `SPAM_VERDICT_TTL_SECONDS` — positive integer, default `86400` (24h); validated
  the same way as the existing `*_SECONDS` vars.

### 6. Wiring (`cmd/server/main.go`)

Construct `moralis.New(cfg.MoralisAPIKey, cfg.MoralisChain)`, wrap it with
`tokenvalidity.NewChecker(moralisClient, pg, cfg.MoralisChain, cfg.SpamVerdictTTL)`,
and pass the checker as the new `Validator` argument to `wallet.NewService`.

## Data flow

```
GetTokens
   │
   ▼
snapshot fresh? ──yes──► filterTokens(ctx, snapshot)
   │ no                        │
   ▼                           │
Alchemy fetch → normalize      │
   │                           │
   ▼                           │
SaveTokens (full set)          │
   │                           │
   └──────────────► filterTokens(ctx, portfolio)
                               │
              ┌────────────────┼───────────────────────────┐
              ▼                ▼                            ▼
           native        in LI.FI list                 unlisted ERC-20
            keep          keep + enrich         validator.IsValid(ctx, addr)
                                                  │ verdict cached & fresh? use it
                                                  │ else Moralis → persist verdict
                                                  ▼
                                       valid → keep │ invalid/error → drop
```

## Error handling

- **Fail-closed.** A per-token Moralis error (or a verdict-store read/write error)
  causes that single token to be dropped — consistent with today's silent drop of
  unlisted tokens. One bad token never fails the whole `GetTokens` request.
- A successful Moralis fetch that returns an empty array is treated as an error →
  the token is dropped.
- `MORALIS_API_KEY` missing at startup → `config.Load` fails fast (process does
  not start), matching `ALCHEMY_API_KEY`.

## Testing

- **`moralis`**: `httptest` server returns a canned metadata array — assert
  `PossibleSpam` / `VerifiedContract` parsed correctly; non-2xx and empty-array
  responses → error.
- **`tokenvalidity`**: fake `MoralisClient` + fake `VerdictStore` with an
  injectable clock — cache hit (no Moralis call), cache miss → fetch → persist,
  stale verdict → refresh, Moralis error → `(false, err)`, and the
  `valid = !possible_spam && verified` truth table.
- **`wallet`**: fake `Validator` (allow / deny / error) — unlisted token kept
  when valid, dropped when invalid or on error; native always kept; listed token
  still enriched. Update `internal/wallet/fakes_test.go` and `NewService` call
  sites.
- **`store`**: `GetSpamVerdict` / `SaveSpamVerdict` round-trip, TTL staleness, and
  upsert-overwrite, following the existing `store_test.go` style.
- Full `go build ./...`, `go vet ./...`, `go test ./...`, and `make docs-check`
  still pass.

## Decisions

- **Second-chance gate, not a blanket re-check:** Moralis is consulted only for
  ERC-20s missing from the LI.FI allowlist. Listed tokens are trusted and
  enriched as before; native tokens are always kept.
- **Internal validity only:** the verdict decides keep/drop and is not surfaced
  in the API response — the `Token` JSON shape is unchanged.
- **Postgres verdict cache (not Redis):** verdicts survive Redis flushes, are
  queryable for debugging, and persist the raw `possible_spam` / `verified`
  booleans with `fetched_at`.
- **Fail-closed on Moralis/verdict errors:** never risk returning spam; matches
  the existing "unlisted is hidden" behavior.
- **`MORALIS_API_KEY` required:** the feature is always on; no silent
  degradation path to reason about.

## Future options (out of scope)

- **Batch Moralis lookups:** the metadata endpoint accepts up to 25 `addresses[]`
  per call — fetch all uncached unlisted contracts in one request.
- **Parallel per-token checks** with a bounded worker pool.
- Surface validity as an optional response field for clients that want to display
  flagged tokens.
- Periodic background refresh of stale verdicts instead of lazy on-read refresh.

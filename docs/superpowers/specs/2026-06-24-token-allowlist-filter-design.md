# Token allowlist filter & enrichment — Design

**Date:** 2026-06-24
**Status:** Approved (design); implementation plan pending

## Summary

Add an hourly background refresher that fetches the [LI.FI](https://li.quest)
Ethereum token list, persists it to Postgres (durable) and Redis (shared warm
cache), and holds it as an in-process snapshot for O(1) lookups. The wallet
service uses this list as an **allowlist**: it drops unknown/spam tokens from
read responses and enriches recognized tokens with LI.FI metadata.

The list is sourced from `GET https://li.quest/v1/tokens?chain=ETH`.

## Goals

- Hide spam / unrecognized ERC-20 tokens from API responses (pure allowlist —
  unknown tokens never appear, no override param).
- Enrich recognized tokens with LI.FI metadata (`logoURI`, `coinKey`,
  `priceUSD`, canonical `name`/`symbol`/`decimals`).
- Keep the lookup fast and resilient: in-memory hot path, Redis + Postgres as the
  durable, cross-instance warm-up tier.

## Non-goals

- No client-facing toggle to reveal unknown tokens (no `?includeUnknown`).
- No multi-chain support — ETH only, consistent with the prototype's single
  configured network.
- No change to the auth/rate-limiting posture (still none).

## Architecture (Approach B)

The hot read path is an **in-process snapshot**; the hourly cron writes the list
to Redis **and** Postgres for durability and cross-instance warm-up. This was
chosen over a Redis-per-request design (Approach A) because, long term, it gives
lower per-request latency (no network hop), survives a Redis outage during
serving, and still satisfies "store the list in Redis for quick lookup" — Redis
is the shared cache that warms every instance; memory is the last-inch cache in
front of it. Approach B is a superset of A, not a departure from it.

### Refresh path (background, hourly)

```
ticker(1h) ─▶ LI.FI GET /v1/tokens?chain=ETH
           ─▶ serialize list
           ─▶ write Postgres (durable)
           ─▶ write Redis (shared cache)
           ─▶ atomic-swap in-process snapshot  ◀─ hot read path
```

On a fetch error during periodic refresh, the refresher logs and **keeps the
prior snapshot**; it retries on the next tick.

### Startup (bootstrap, synchronous before serving)

```
fetch LI.FI ──success──▶ write PG + Redis + set snapshot ──▶ serve
     │fail
     ▼
load Redis ──hit──▶ set snapshot ──▶ serve
     │miss
     ▼
load Postgres ──hit──▶ set snapshot ──▶ serve
     │miss
     ▼
exit(1)   (nothing anywhere — cannot safely filter)
```

The server does not accept traffic until a list is loaded. On a *restart* where
Postgres already holds a previous list but the fresh fetch fails, it warms from
Postgres and serves (graceful degradation) rather than exiting. It only exits
when no list is obtainable from any source (truly first boot with LI.FI down).

### Read path (per request)

The wallet service reads the current in-process snapshot (no network hop) and
applies filter + enrich. Once the server is serving, the allowlist never
surfaces an HTTP error on the read path — it is always in memory.

## Components

### `internal/lifi` (new package)

LI.FI HTTP client.

- `Client.GetTokens(ctx, chain string) ([]ListToken, error)` — calls
  `GET {tokensURL}?chain={chain}` (where `tokensURL` is `LIFI_TOKENS_URL`,
  default `https://li.quest/v1/tokens`) and parses LI.FI's envelope
  `{"tokens": {"<chainId>": [ ... ]}}`, returning the token slice for the
  requested chain.
- `ListToken{ Address, Symbol, Name string; Decimals int; CoinKey, LogoURI, PriceUSD string }`.

### `internal/tokenlist` (new package)

The allowlist core.

- `Snapshot`:
  - `byAddress map[string]ListToken` — keyed by **lowercased** address.
  - `symbols map[string]struct{}` — **uppercased** symbols.
  - `chain string`, `fetchedAt time.Time`, `count int`.
  - `LookupByAddress(addr string) (ListToken, bool)` (case-insensitive).
  - `HasSymbol(sym string) bool` (case-insensitive).
- `Holder` — wraps `atomic.Pointer[Snapshot]`: `Current() *Snapshot`,
  `Set(*Snapshot)` for lock-free reads and atomic swaps.
- `Refresher` — deps: lifi client, redis client, pg store, holder, interval,
  chain, logger.
  - `Bootstrap(ctx) error` — the startup ladder above.
  - `Run(ctx)` — the ticker loop; logs and retains the prior snapshot on fetch
    error.

The wallet service depends on a small interface satisfied by `*Snapshot`, so it
never imports Redis or the refresher:

```go
type Allowlist interface {
    LookupByAddress(addr string) (lifi.ListToken, bool)
    HasSymbol(sym string) bool
}
```

## Persistence

Both tiers store the **same serialized snapshot** (a JSON array of `ListToken`)
so reads are symmetric and writes are atomic.

### Postgres

New table, created by the existing `Migrate`:

```sql
CREATE TABLE IF NOT EXISTS lifi_token_lists (
    chain      TEXT PRIMARY KEY,
    payload    JSONB       NOT NULL,   -- array of ListToken
    fetched_at TIMESTAMPTZ NOT NULL
);
```

One row per chain (`ETH`), upserted each refresh. No per-token rows — individual
tokens are never queried from SQL; lookups happen in memory.

Store methods:

- `SaveTokenList(ctx, chain string, tokens []lifi.ListToken, fetchedAt time.Time) error`
- `LoadTokenList(ctx, chain string) (tokens []lifi.ListToken, fetchedAt time.Time, ok bool, err error)`

### Redis

Key `lifi:tokens:{chain}` holding the same JSON array, set with a safety TTL
longer than the refresh interval (e.g. 24h) so a stale-but-present list survives
refresher hiccups while still expiring if the refresher dies entirely. The Redis
wrapper mirrors the two store methods (`SET` on refresh, `GET` on warm-up).

## Wallet service — filter & enrich

The service gains the `Allowlist` dependency. Filtering/enrichment happens **at
read time on every call** (after the existing cache lookups), so the Postgres
token/tx caches keep storing raw Alchemy data and allowlist changes take effect
immediately without cache invalidation.

### `GetTokens`

After `normalizeTokens`, for each token:

- **Native** (`IsNative`): always keep, no enrichment.
- **Non-native:** keep only if `LookupByAddress(*TokenAddress)` hits; otherwise
  drop. For survivors, enrich:
  - set `LogoURI`, `CoinKey`, and a distinct `PriceUSD` field;
  - override `Name` / `Symbol` / `Decimals` with LI.FI's canonical values.

**Decimals caveat:** `Balance` was scaled by
`ScaleBalance(rawBalance, alchemyDecimals)`. When decimals are overridden,
`Balance` **must be re-derived from `RawBalance` using LI.FI's decimals**, or the
displayed balance is wrong. The implementation re-scales on override.

New fields on `wallet.Token` (the existing Alchemy-sourced `price` is untouched):

```go
LogoURI  *string `json:"logoURI"`
CoinKey  *string `json:"coinKey"`
PriceUSD *string `json:"priceUSD"`
```

### `GetTransactions`

Filter `Transfers`: keep a transfer if `Asset == "ETH"` (native) or
`HasSymbol(Asset)`; drop everything else, including empty/unknown assets. This is
**best-effort** matching by symbol, because transfers carry an asset symbol but
no contract address, and symbols can collide or be spoofed. Applied at read time,
including on first-page cache hits.

## Config, dependencies, wiring

### New config (`internal/config`)

| Var | Required | Default | Notes |
|---|---|---|---|
| `REDIS_URL` | yes | — | e.g. `redis://localhost:6379/0` |
| `LIFI_TOKENS_URL` | no | `https://li.quest/v1/tokens` | base URL |
| `LIFI_CHAIN` | no | `ETH` | query chain |
| `LIFI_REFRESH_SECONDS` | no | `3600` | positive int; mirrors `CACHE_TTL_SECONDS` validation |

### Dependencies

- Add `github.com/redis/go-redis/v9`.
- Add a `redis:7` service to `docker-compose.yml` (`6379:6379`, healthcheck).

### Wiring (`cmd/server/main.go`)

Load config → open Postgres → `Migrate` (now also creates `lifi_token_lists`) →
open Redis → build `lifi.Client`, `tokenlist.Holder`, `tokenlist.Refresher` →
`refresher.Bootstrap(ctx)` (exit on error) → `go refresher.Run(ctx)` →
`wallet.NewService(..., holder)`. Bootstrap uses a bounded timeout; `Run` uses
the long-lived context.

## Error handling

- **LI.FI fetch fails at bootstrap:** fall back Redis → Postgres; if all empty,
  `exit(1)`. On a restart where PG holds a prior list, warm from it and serve.
- **LI.FI fetch fails during periodic refresh:** log; keep the prior in-memory
  snapshot; retry next tick.
- **Redis write fails on refresh:** log; Postgres is source of truth; still swap
  the snapshot (serving continues).
- **Postgres write fails on refresh:** log; still write Redis + swap snapshot
  (availability over durability); next successful tick reconciles.
- **Redis unreachable at warm-up:** fall through to Postgres.
- Sentinel errors (`ErrUpstream`, `ErrStore`) stay as-is. The allowlist never
  surfaces an HTTP error on the read path.

## Testing

- **`internal/lifi`:** `httptest` server returning sample
  `{"tokens":{"1":[...]}}`; assert parse, field mapping, chain selection.
- **`internal/tokenlist`:** snapshot lookups (address case-insensitivity, symbol
  set); `Holder` atomic set/current; `Refresher.Bootstrap` ladder
  (fetch-success / Redis-fallback / PG-fallback / all-empty-error) and `Run`
  swap-on-success / retain-on-error, using fakes + injected ticker/clock.
- **`internal/store`:** round-trip `SaveTokenList` / `LoadTokenList`.
- **`internal/wallet`:** extend `fakes_test.go` with a fake allowlist; assert
  `GetTokens` drops unknowns, keeps native, enriches, and re-scales `Balance` on
  decimals override; assert `GetTransactions` keeps `ETH` + known symbols and
  drops the rest.
- **`internal/config`:** `REDIS_URL` required; `LIFI_REFRESH_SECONDS`
  parsing/validation.

## Open considerations (future, out of scope)

- Multi-chain allowlists (one snapshot/key/row per chain).
- A standalone `cmd/tokensync` refresher binary if the API runs as multiple
  replicas (Approach C), so only one process fetches LI.FI.
- Reverting the read path to direct Redis lookups (Approach A) would require an
  address-keyed store shape (per-token rows + Redis HASH).

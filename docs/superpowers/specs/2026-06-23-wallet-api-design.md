# Wallet API — Design Spec

**Date:** 2026-06-23
**Status:** Approved (design phase)

## Overview

A read-only, non-custodial Ethereum (EVM) wallet API written in Go. It exposes
two endpoints — a combined token portfolio (native ETH + ERC-20, with metadata
and prices) and transaction history — for any given address. On-chain data is
sourced from **Alchemy**. Results are cached in **Postgres** using a read-through
(cache-first) strategy with a configurable TTL.

The API never handles private keys, signs transactions, or broadcasts
transactions. It is read-only.

### Maturity target

Learning / prototype. Clean, working endpoints with sensible error handling and
unit tests. No API authentication, rate-limiting, or structured logging in scope.

## Scope

In scope:
- `GET` token portfolio for an address (native ETH + ERC-20, metadata + prices)
- `GET` transaction history (asset transfers) for an address
- Postgres persistence:
  - structured `wallet_tokens` table holding the **newest** token snapshot
  - generic JSON cache table for transaction history
- Read-through (cache-first) with configurable TTL

Out of scope (explicitly):
- Private key management, transaction signing, broadcasting
- Multi-chain / non-EVM chains (EVM/Ethereum only)
- API authentication, rate-limiting, response caching beyond the DB
- Bitcoin / UTXO support

## Architecture

```
cmd/server/main.go        → load config, build store + alchemy client + service, start HTTP server
internal/config           → read env vars
internal/alchemy          → typed client over Alchemy APIs (HTTP POST)
internal/store            → Postgres: TokenStore (structured) + TxCache (JSON)
internal/wallet           → service: domain types, normalization, cache-first orchestration
internal/api              → http.Handlers + routing (net/http, Go 1.22 patterns)
migrations/0001_init.sql  → table schemas, applied on boot
docker-compose.yml        → local Postgres
```

**Dependency direction:** `api → wallet → {alchemy, store}`.

Each layer depends only on the layer(s) below it:
- `alchemy` knows nothing about HTTP serving or the database.
- `store` knows nothing about Alchemy's wire format.
- `wallet` orchestrates cache-first logic and maps raw responses into clean
  domain types.
- `api` turns HTTP requests into service calls and service results into JSON.

### Tech choices

- **Language:** Go. The `net/http` method-and-pattern routing requires 1.22+, but the effective floor is **1.25** because the `github.com/jackc/pgx/v5` dependency declares `go 1.25.0`; `go.mod` is pinned accordingly.
- **HTTP:** standard library `net/http`.
- **Data source:** Alchemy Portfolio API + `getAssetTransfers`.
- **Database:** Postgres (run locally via `docker-compose.yml`).

## Endpoints & response contracts

All endpoints are read-only `GET`s. Address is a path parameter. Responses are
JSON. Large numeric values (raw token balances) are returned as **strings** to
avoid float/precision loss.

### `GET /v1/addresses/{address}/tokens`

Backed by the Alchemy **Portfolio API — Tokens By Wallet** endpoint:

- **Method/URL:** `POST https://api.g.alchemy.com/data/v1/{apiKey}/assets/tokens/by-address`
- **Request body:**

```json
{
  "addresses": [
    { "address": "0x…", "networks": ["eth-mainnet"] }
  ],
  "withMetadata": true,
  "withPrices": true,
  "includeNativeTokens": true,
  "includeErc20Tokens": true
}
```

Returns native ETH and ERC-20 tokens together, each with metadata
(symbol/name/decimals/logo) and prices. Native tokens have a null
`tokenAddress`.

- **Upstream response shape (relevant fields):**

```json
{
  "data": {
    "tokens": [
      {
        "address": "0x…",
        "network": "eth-mainnet",
        "tokenAddress": "0x… or null",
        "tokenBalance": "string",
        "tokenMetadata": { "decimals": 6, "logo": "…", "name": "USD Coin", "symbol": "USDC" },
        "tokenPrices": [ { "currency": "usd", "value": "1.0001", "lastUpdatedAt": "ISO-8601" } ],
        "error": null
      }
    ],
    "pageKey": "…"
  }
}
```

- **Our API response (normalized, read from `wallet_tokens`):**

```json
{
  "address": "0x…",
  "network": "eth-mainnet",
  "fetchedAt": "ISO-8601",
  "tokens": [
    {
      "tokenAddress": null,
      "symbol": "ETH",
      "name": "Ethereum",
      "decimals": 18,
      "rawBalance": "1500000000000000000",
      "balance": "1.5",
      "isNative": true,
      "price": { "currency": "usd", "value": "3200.50", "lastUpdatedAt": "ISO-8601" }
    }
  ]
}
```

- `isNative` is `true` when `tokenAddress` is null.
- `balance` is `rawBalance` scaled by `decimals` (string, no float loss).
- `price` is the first entry of `tokenPrices`, or `null` if unavailable.
- Tokens whose upstream `error` is non-null are omitted (see open items).

### `GET /v1/addresses/{address}/transactions`

Backed by Alchemy `alchemy_getAssetTransfers` with
`category: ["external", "erc20"]`, covering both incoming and outgoing transfers.
Supports `?limit=` and `?pageKey=` query params for pagination.

```json
{
  "address": "0x…",
  "transfers": [
    {
      "hash": "0x…",
      "from": "0x…",
      "to": "0x…",
      "asset": "ETH",
      "value": "0.5",
      "blockNum": "0x…",
      "category": "external"
    }
  ],
  "nextPageKey": "…"
}
```

## Persistence & data flow

Two different storage strategies, one per resource.

### Tokens — structured table, newest snapshot

The `tokens` endpoint reads/writes the structured `wallet_tokens` table. It keeps
only the **current** snapshot per address (no history). A `token_fetch_meta` row
records the last fetch time per `(address, network)` so the freshness check works
even when a wallet holds **zero tokens**.

```
request → service.GetTokens(address, network)
  → read token_fetch_meta(address, network)
  → fresh?  (fetched_at > now() - CACHE_TTL)
       yes → SELECT rows from wallet_tokens → return
       no  → call Alchemy Portfolio API
            → in ONE transaction:
                 DELETE wallet_tokens WHERE address, network
                 INSERT fresh token rows (shared fetched_at = now())
                 UPSERT token_fetch_meta(address, network, fetched_at = now())
            → return fresh rows
```

This is **write-through on miss**: the freshly fetched snapshot is saved to the
DB before being returned. Replacing the row set each refresh means tokens the
wallet no longer holds disappear.

### Transactions — generic JSON cache

The `transactions` endpoint uses a generic JSON cache table (read-through,
write-through on miss).

```
request → service.GetTransactions(address, params)
  → look up tx_cache(address, params)
  → fresh? yes → return cached payload
           no  → call Alchemy → upsert payload + fetched_at → return
```

**Pagination caveat:** caching paginated history by `pageKey` is messy and
low-value, so the transactions endpoint caches **only the first page** (no
`pageKey`), keyed with `params` derived from `limit`. When a request includes
`?pageKey=`, the service **bypasses the cache** and calls Alchemy directly
(result not cached).

## Database schema

```sql
-- Newest token snapshot per (address, network, token).
-- token_key normalizes the nullable token address: 'native' for the chain
-- native token, otherwise the lowercased ERC-20 contract address.
CREATE TABLE wallet_tokens (
    address          TEXT NOT NULL,           -- normalized lowercase
    network          TEXT NOT NULL,
    token_key        TEXT NOT NULL,           -- 'native' | lowercased contract address
    token_address    TEXT,                    -- NULL for native
    is_native        BOOLEAN NOT NULL,
    symbol           TEXT,
    name             TEXT,
    decimals         INTEGER,
    raw_balance      TEXT NOT NULL,           -- uint256 as decimal string
    balance          TEXT NOT NULL,           -- scaled decimal string
    price_currency   TEXT,
    price_value      TEXT,
    price_updated_at TEXT,        -- stored as the upstream ISO-8601 string verbatim
    fetched_at       TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (address, network, token_key)
);

-- Freshness marker so empty wallets cache correctly.
CREATE TABLE token_fetch_meta (
    address    TEXT NOT NULL,
    network    TEXT NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (address, network)
);

-- Generic JSON cache for transaction history.
CREATE TABLE tx_cache (
    address    TEXT NOT NULL,            -- normalized lowercase
    params     TEXT NOT NULL DEFAULT '', -- e.g. limit for first page
    payload    JSONB NOT NULL,           -- the exact JSON returned to the client
    fetched_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (address, params)
);
```

Schema applied at startup via `migrations/0001_init.sql`.

## Configuration

All via environment variables:

| Var                 | Default        | Purpose                                  |
|---------------------|----------------|------------------------------------------|
| `ALCHEMY_API_KEY`   | (required)     | Alchemy API key (used in the URL path)   |
| `ALCHEMY_NETWORK`   | `eth-mainnet`  | Alchemy network slug                     |
| `DATABASE_URL`      | (required)     | Postgres connection string               |
| `CACHE_TTL_SECONDS` | `300`          | Cache freshness window (5 min default)   |
| `PORT`              | `8080`         | HTTP listen port                         |

## Error handling

All errors return `{ "error": "message" }` with an appropriate HTTP status:

- Invalid address (must be `0x` + 40 hex chars) → `400`
- Alchemy error / timeout → `502`
- Database unavailable → `503`

**Stale-serve policy:** for the prototype, if Alchemy fails and only *stale* data
exists, the API returns the error (no stale fallback).

## Testing

- `alchemy` client: unit tests against recorded JSON responses via an
  `httptest` server (no live network calls), covering both the Portfolio API
  request body and the `getAssetTransfers` call.
- `wallet` service: cache logic tests — fresh hit, miss (fetch + write), empty
  wallet, and `pageKey` bypass — using a fake/in-memory store.
- `store`: token snapshot replacement (delete + insert in one transaction) and
  freshness-marker behavior; optional DB integration test.
- `api` handlers: tested against a fake service.

No test makes live network calls.

## Open items / future work

- API authentication and rate-limiting (deferred — prototype scope).
- Stale-serve fallback on Alchemy failure (deferred).
- Multi-page pagination for the `tokens` endpoint (Portfolio API `pageKey`) — not
  exposed in this prototype.
- Surfacing per-token upstream `error` values vs. silently omitting them.
- Multi-page transaction caching (intentionally not cached).

# wallet-api

A **read-only, non-custodial** Ethereum (EVM) wallet API written in Go. It never
handles private keys, signs, or broadcasts transactions — it only reads on-chain
data and serves it back.

On-chain data is sourced from **Alchemy** and cached in **Postgres** with a
read-through (cache-first) strategy and a configurable TTL.

## Endpoints

| Method & path | Description |
|---|---|
| `GET /v1/addresses/{address}/tokens` | Token portfolio — native ETH + ERC-20, with metadata and prices |
| `GET /v1/addresses/{address}/transactions` | Transaction history (asset transfers), paginated via `limit` & `pageKey` |

`{address}` must be a `0x`-prefixed 20-byte hex address.

## API documentation

Interactive reference (Redoc) is served by the running app:

- `GET /docs` — rendered API reference
- `GET /openapi.json` — the OpenAPI 2.0 spec

A static copy is published under `docs/api/` (suitable for GitHub Pages).

The spec is generated from handler annotations — regenerate it with:

    make docs        # regenerate internal/apidocs/ and docs/api/openapi.json
    make docs-check  # CI guard: fails if the committed spec is stale

After cloning, run `make hooks` once to install a pre-commit hook that regenerates and stages the spec automatically when you change a handler annotation or a `wallet` type.

## Configuration

Set via environment variables:

| Variable | Required | Default | Description |
|---|---|---|---|
| `ALCHEMY_API_KEY` | yes | — | Alchemy API key |
| `DATABASE_URL` | yes | — | Postgres connection string |
| `ALCHEMY_NETWORK` | no | `eth-mainnet` | Target network |
| `CACHE_TTL_SECONDS` | no | `300` | Cache TTL (positive integer) |
| `PORT` | no | `8080` | HTTP listen port |
| `REDIS_URL` | yes | — | Redis connection string, e.g. `redis://localhost:6379/0` |
| `LIFI_TOKENS_URL` | no | `https://li.quest/v1/tokens` | LI.FI token-list endpoint |
| `LIFI_CHAIN` | no | `ETH` | LI.FI chain key for the allowlist |
| `LIFI_REFRESH_SECONDS` | no | `3600` | Allowlist refresh interval (positive integer) |
| `MORALIS_API_KEY` | yes | — | Moralis API key (spam/metadata for unlisted tokens) |
| `MORALIS_CHAIN` | no | `eth` | Moralis chain id |
| `MORALIS_RECHECK_SECONDS` | no | `604800` | Verdict re-check window, ~1 week (positive integer) |
| `MORALIS_REDIS_TTL_SECONDS` | no | `86400` | Verdict Redis hot-cache TTL, ~1 day (positive integer) |

Responses are filtered to the LI.FI token allowlist: tokens on the allowlist are enriched with `logoURI`, `coinKey`, and `priceUSD`. Unlisted ERC-20s are no longer simply hidden — they are checked against the Moralis API and kept (enriched with Moralis metadata) only if they are not flagged as `possible_spam` and are a `verified_contract`; otherwise they are dropped. The allowlist is fetched at startup and refreshed hourly.

## Run locally

```sh
docker compose up -d            # start Postgres (5433) + Redis (6379)
export ALCHEMY_API_KEY=...      # your key
export DATABASE_URL=postgres://wallet:wallet@localhost:5433/wallet
export REDIS_URL=redis://localhost:6379/0
go run ./cmd/server             # migrates schema, then listens on :8080
```

## Layout

```
cmd/server           entrypoint
internal/api         HTTP router & handlers
internal/wallet      domain service (portfolio, transactions)
internal/alchemy     Alchemy client
internal/store       Postgres store & schema
internal/config      env-based configuration
internal/lifi        LI.FI token-list client
internal/tokenlist   allowlist snapshot + hourly refresher
internal/rediscache  Redis token-list cache
```

Run the tests with `go test ./...`.

> Scope is a learning/prototype: EVM/Ethereum only, no auth or rate-limiting.

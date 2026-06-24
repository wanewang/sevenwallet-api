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

Responses are filtered to the LI.FI token allowlist: unrecognized ERC-20s are hidden and recognized tokens are enriched with `logoURI`, `coinKey`, and `priceUSD`. The allowlist is fetched at startup and refreshed hourly.

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
cmd/server      entrypoint
internal/api      HTTP router & handlers
internal/wallet   domain service (portfolio, transactions)
internal/alchemy  Alchemy client
internal/store    Postgres store & schema
internal/config   env-based configuration
internal/lifi       LI.FI token-list client
internal/tokenlist  allowlist snapshot + hourly refresher
internal/rediscache Redis token-list cache
```

Run the tests with `go test ./...`.

> Scope is a learning/prototype: EVM/Ethereum only, no auth or rate-limiting.

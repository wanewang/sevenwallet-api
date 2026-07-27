# wallet-api

A **read-only, non-custodial** Ethereum (EVM) wallet API written in Go. It never
handles private keys, signs, or broadcasts transactions — it only reads on-chain
data and serves it back.

On-chain data is sourced from **Alchemy** and cached in **Postgres** with a
read-through (cache-first) strategy and a configurable TTL.

## Endpoints

| Method & path | Description |
|---|---|
| `GET /v1/native` | Native ETH metadata and USD price from the refreshed LI.FI snapshot |
| `GET /v1/tokens/{address}` | Latest locally cached wallet tokens with fresh, source-labeled `cg` and `cmc` market data |
| `GET /v1/wallet/{address}` | Token portfolio with Alchemy balances and LI.FI/Moralis filtering, without CoinGecko enrichment |
| `GET /v1/addresses/{address}/tokens` | Token portfolio — native ETH + ERC-20, with metadata and prices |
| `GET /v1/addresses/{address}/transactions` | Transaction history (asset transfers), paginated via `limit` & `pageKey` |

`{address}` must be a `0x`-prefixed 20-byte hex address.

`GET /v1/tokens/{address}` is intentionally cache-only for wallet holdings. It uses the
latest Postgres wallet snapshot even when that snapshot is older than the normal
wallet TTL, reports the original time as `portfolioFetchedAt`, and never calls
Alchemy. A wallet must first have been cached through the existing address
token route; otherwise this route returns `404`. The normal LI.FI/Moralis token
filter is still applied.

Market data is returned independently as `cg` (CoinGecko) and `cmc`
(CoinMarketCap). Either object can be `null` when its mapping, fresh cache, or
provider is unavailable. Price and 24-hour-change fields may also be individually
`null`. Only fresh market data is exposed here, and one provider failing does
not fail the response.

`GET /v1/wallet/{address}` uses the same cache-first Alchemy snapshot, LI.FI
allowlist, and Moralis validation pipeline as the address token route, but does
not call CoinGecko. It returns the same `TokenPortfolio` shape. Alchemy `price`
and LI.FI `priceUSD` data may still be present, while CoinGecko-derived market
fields are not populated.

`GET /v1/native` returns a bare token array. It currently contains one ETH item,
allowing more native-token entries to be added later without changing the
top-level response type. Metadata and USD price are read from the existing LI.FI
snapshot, so the endpoint makes no request-time provider call.

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
| `PROVIDER_API_LOGGING` | no | `false` | Set exactly to `true` to enable local Alchemy, Moralis, and CoinGecko diagnostics; ignored on Cloud Run when `K_SERVICE` is present |
| `REDIS_URL` | yes | — | Redis connection string, e.g. `redis://localhost:6379/0` |
| `LIFI_TOKENS_URL` | no | `https://li.quest/v1/tokens` | LI.FI token-list endpoint |
| `LIFI_CHAIN` | no | `ETH` | LI.FI chain key for the allowlist |
| `LIFI_REFRESH_SECONDS` | no | `3600` | Allowlist refresh interval (positive integer) |
| `MORALIS_API_KEY` | yes | — | Moralis API key (spam/metadata for unlisted tokens) |
| `MORALIS_CHAIN` | no | `eth` | Moralis chain id |
| `MORALIS_RECHECK_SECONDS` | no | `604800` | Verdict re-check window, ~1 week (positive integer) |
| `MORALIS_REDIS_TTL_SECONDS` | no | `86400` | Verdict Redis hot-cache TTL, ~1 day (positive integer) |
| `COINGECKO_BASE_URL` | no | `https://api.coingecko.com/api/v3` | CoinGecko API root |
| `COINGECKO_USER_AGENT` | no | `wallet-api/1.0` | CoinGecko request user agent |
| `COINGECKO_PLATFORM` | no | `ethereum` | Existing-route CoinGecko platform key |
| `COINGECKO_LIST_REFRESH_SECONDS` | no | `21600` | CoinGecko mapping refresh interval, 6 hours |
| `COINGECKO_MARKET_TTL_SECONDS` | no | `1800` | CoinGecko market freshness, 30 minutes |
| `COINGECKO_ENRICH_TIMEOUT_SECONDS` | no | `5` | Existing-route CoinGecko enrichment timeout |
| `COINGECKO_NATIVE_IDS` | no | `ethereum` | Existing-route comma-separated native CoinGecko IDs |
| `COINMARKETCAP_BASE_URL` | no | `https://pro-api.coinmarketcap.com/public-api` | Keyless CoinMarketCap API root |
| `COINMARKETCAP_LIST_REFRESH_SECONDS` | no | `21600` | Active CMC map refresh interval, 6 hours |
| `COINMARKETCAP_MARKET_TTL_SECONDS` | no | `1800` | CMC market freshness, 30 minutes |
| `TOKEN_MARKET_ENRICH_TIMEOUT_SECONDS` | no | `5` | Shared `cg`/`cmc` lookup budget for `/v1/tokens` |
| `MARKET_MISS_TTL_SECONDS` | no | `7200` | How long a token a provider has no data for stays suppressed, 2 hours |

`MARKET_MISS_TTL_SECONDS` governs the negative cache, which is deliberately separate from the market TTLs above. A market TTL answers "how stale may a price be"; the miss TTL answers "how long before we ask again about a token the provider had nothing for". Misses live in their own Redis namespaces (`coingecko:miss:*`, `cmc:miss:*`), hold no market data, and are never written to PostgreSQL. A fresh record from any tier always outranks a live miss, so a token the provider starts carrying is picked up as soon as data exists for it.

Responses are filtered to the LI.FI token allowlist: tokens on the allowlist are enriched with `logoURI`, `coinKey`, and `priceUSD`. Unlisted ERC-20s are no longer simply hidden — they are checked against the Moralis API and kept (enriched with Moralis metadata) unless they are flagged as `possible_spam`; otherwise they are dropped. The allowlist is fetched at startup and refreshed hourly.

Provider API diagnostics are off by default. For local troubleshooting, set `PROVIDER_API_LOGGING=true` to log Alchemy and Moralis request attempts and decoded results, plus CoinGecko price attempts and results. CoinGecko's `/coins/list` operation logs only its sanitized request URL, never the catalog response or outcome details. The feature does not add LI.FI request or response logging. Cloud Run always suppresses these diagnostics because its `K_SERVICE` variable is present, even if the opt-in is set. Local diagnostic results can contain wallet addresses and provider data; API credentials and credential-bearing URLs are redacted.

Dual-provider support currently recognizes only `eth-mainnet`. Its static
registry uses EVM chain ID `1`, internal chain `ethereum`, CMC platform ID `1`,
CMC native ETH ID `1027`, and CoinGecko platform/native ID `ethereum`. Adding a
network requires updating that registry and its tests. CMC uses the keyless
active cryptocurrency map at startup and every six hours; catalog failure is
non-fatal and falls back to its last Postgres snapshot.

## Run locally

```sh
docker compose up -d            # start Postgres (5433) + Redis (6379)
export ALCHEMY_API_KEY=...      # your key
export DATABASE_URL=postgres://wallet:wallet@localhost:5433/wallet
export REDIS_URL=redis://localhost:6379/0
export PROVIDER_API_LOGGING=true # optional local provider diagnostics
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
internal/marketchain static supported-chain provider identifiers
internal/coinmarketcap keyless CoinMarketCap HTTP client
internal/cmcmarket   CoinMarketCap catalog and fresh market cache
internal/marketcompare concurrent provider comparison
```

Run the tests with `go test ./...`.

> Scope is a learning/prototype: EVM/Ethereum only, no auth or rate-limiting.

## Context

The service currently stores raw normalized Alchemy wallet snapshots in PostgreSQL, then applies LI.FI/Moralis filtering and CoinGecko enrichment when serving the existing address route. CoinGecko owns a six-hour in-memory/PostgreSQL mapping catalog and shared 30-minute Redis/PostgreSQL market records. Its public result is flattened into `wallet.Token`, so the current interface cannot represent two providers without changing existing payloads.

The new route must instead read the latest stored wallet snapshot without refreshing it, reuse the existing filter, and expose CoinGecko and CoinMarketCap side by side. CoinMarketCap's keyless API uses a numeric platform ID (`1` for Ethereum) inside `/v1/cryptocurrency/map`, numeric cryptocurrency IDs (`1660` for Monolith and `1027` for native ETH), and Simple Price batches capped at 50 IDs. The DEX platform-list and DEX price APIs are not part of this design.

## Goals / Non-Goals

**Goals:**

- Add an isolated, cache-only wallet market-comparison route with a stable provider-specific response.
- Reuse exactly the existing wallet-token filter while preventing Alchemy calls from the new route.
- Add durable, fresh-only CoinMarketCap mapping and market caches without mixing provider identities.
- Return useful partial results when either provider, cache layer, or catalog is unavailable.
- Bound dual-provider market work and preserve existing endpoint behavior.
- Make supported chain/provider identifiers explicit and testable.

**Non-Goals:**

- Selecting a preferred provider, averaging prices, calculating a discrepancy, or overwriting one provider with another.
- Adding CoinMarketCap data to existing address or native responses.
- Refreshing wallet holdings from Alchemy through the new route.
- Returning expired CG or CMC comparison values.
- Supporting authenticated CoinMarketCap Pro calls, DEX-derived pricing, or `/v1/dex/platform/list`.
- Adding a second blockchain in this change.
- Generalizing or migrating the existing `coingecko_*` persistence schema.
- Exposing market cap, volume, or provider update timestamps in the new response.

## Decisions

### 1. Add a distinct cache-only wallet service method and response type

The HTTP layer adds `GET /v1/tokens/{address}` and validates the required path address using the existing EVM address validator. A new wallet-service method loads the latest snapshot through a store operation that does not take a TTL. It then calls the existing `filterTokens` path before market comparison.

The existing `GetFreshTokens` operation remains unchanged for the address route. The new latest-snapshot operation first checks `token_fetch_meta`, which distinguishes a real empty portfolio from a wallet that has never been cached, then loads `wallet_tokens` using the same decoding rules as the fresh operation.

The new public types are separate from `wallet.Token` so no provider data is flattened or allowed to change existing fields:

```text
TokenMarketPortfolio
  wallet: string
  network: string
  portfolioFetchedAt: timestamp
  tokens: TokenMarket[]

TokenMarket
  tokenAddress: string | null
  symbol: string
  name: string
  decimals: integer
  balance: string
  cg: CoinGeckoMarket | null
  cmc: CoinMarketCapMarket | null

CoinGeckoMarket
  id: string
  priceUSD: string | null
  change24hPercent: number | null

CoinMarketCapMarket
  id: integer
  priceUSD: string | null
  change24hPercent: number | null
```

Provider keys and nullable fields are always present in JSON. A provider object exists only when its current mapping produced at least one fresh valid market field.

**Alternative considered:** Extend `wallet.Token` with CMC fields. Rejected because it would change existing payloads, retain the current asymmetrical flat CG fields, and make future provider additions increasingly awkward.

### 2. Keep supported chain identity in a static registry

A small internal registry is keyed by `ALCHEMY_NETWORK`. Its initial entry is:

| Field | Ethereum value |
|---|---:|
| Alchemy network | `eth-mainnet` |
| EVM chain ID | `1` |
| internal market chain | `ethereum` |
| CMC platform ID | `1` |
| CMC native cryptocurrency ID | `1027` |
| CG platform | `ethereum` |
| CG native ID | `ethereum` |

Startup fails when the configured Alchemy network has no entry. Adding another chain requires a registry row and tests for all provider identifiers. Existing CoinGecko configuration and behavior for existing routes are retained; the new comparison flow uses the selected registry entry so its two sources always refer to the same wallet chain.

**Alternative considered:** Discover platform IDs from `/v1/dex/platform/list` at every startup. Rejected because the supported networks change rarely, the DEX catalog is not required for `/v1/simple/price`, and a static registry makes chain support an explicit code/spec decision.

### 3. Introduce a thin keyless CoinMarketCap client

`internal/coinmarketcap` owns the HTTP envelope and provider-specific types. The client defaults to `https://pro-api.coinmarketcap.com/public-api` and sends no API-key header. Its operations are:

- Fetch one active cryptocurrency-map page using `listing_status=active`, one-based `start`, `limit=5000`, and platform auxiliary data.
- Fetch Simple Price for no more than 50 numeric IDs using comma-separated `ids`, `convert=USD`, and `include_percent_change_24h=true`.

Responses decode numbers with `json.Number` so USD price text can be preserved exactly. Both HTTP status and the CoinMarketCap response-envelope status are validated. Price calls follow the existing CoinGecko retry categories—transport/decode errors, `429`, and `5xx`—but every attempt and delay remains context-bound by the shared comparison deadline. Empty-ID requests return without HTTP work.

Configuration adds:

| Variable | Default | Purpose |
|---|---|---|
| `COINMARKETCAP_BASE_URL` | `https://pro-api.coinmarketcap.com/public-api` | keyless root/test override |
| `COINMARKETCAP_LIST_REFRESH_SECONDS` | `21600` | active-map refresh interval |
| `COINMARKETCAP_MARKET_TTL_SECONDS` | `1800` | CMC market freshness |
| `TOKEN_MARKET_ENRICH_TIMEOUT_SECONDS` | `5` | shared CG/CMC comparison budget |

Existing CoinGecko TTL and enrichment settings remain unchanged for existing routes. The comparison flow uses the existing CoinGecko market TTL and the new CMC TTL independently.

**Alternative considered:** Add optional key support immediately. Rejected because it adds credential and base-path modes not needed for the selected keyless endpoints.

### 4. Build and refresh a supported-platform CMC catalog

The CMC refresher fetches pages until it receives fewer than 5,000 data entries. It constructs mappings only for active entries whose `platform.id` exists in the static registry and whose ID and normalized address are usable. Ethereum `0x` addresses are lowercase-normalized. Native mappings do not come from platform-null map entries; they come from the registry.

All pages must succeed before any state changes. A complete non-empty mapping set is transactionally written to `coinmarketcap_coin_mappings`, then installed in an atomic in-memory holder. On bootstrap failure, the refresher uses a separately bounded PostgreSQL fallback load. Absence of both sources leaves only CMC unavailable; it never fails server startup. Refresh failure retains the installed catalog.

The six-hour interval is deliberately separate from the 30-minute market TTL: mappings change rarely, while comparison prices must be fresher.

**Alternative considered:** Fetch the map on every wallet request or store every returned platform. Rejected because the payload is global and large, while this deployment supports only Ethereum.

### 5. Resolve CMC IDs by contract first and symbol only on ambiguity

The CMC catalog indexes candidates by `(platform ID, normalized contract address)`. Resolution rules are deterministic:

1. One candidate: accept it without checking the symbol.
2. Multiple candidates: retain case-insensitive symbol matches against the filtered wallet token.
3. Exactly one symbol match: accept it.
4. Zero or multiple symbol matches: log the ambiguity and skip CMC for that token.

Native ETH resolves directly to registry ID `1027`. Rank is stored only if later needed for diagnostics; it never selects a winner.

When a contract mapping cannot be resolved, the provider lookup logs the normalized chain and contract address together with an explicit `source=cg` or `source=cmc` field. This keeps otherwise similar resolution failures attributable when both lookups run concurrently.

**Alternative considered:** Include symbol in every lookup or choose the best rank. Rejected because symbols are mutable/non-unique and rank does not prove contract identity.

### 6. Keep CMC persistence physically separate and timestamp-safe

PostgreSQL adds:

```sql
coinmarketcap_coin_mappings(
  id BIGINT,
  platform_id BIGINT,
  name TEXT,
  symbol TEXT,
  address TEXT,
  fetched_at TIMESTAMPTZ,
  PRIMARY KEY (id, platform_id, address)
)

coinmarketcap_market_data(
  chain TEXT,
  token_key TEXT,
  coinmarketcap_id BIGINT,
  price_usd TEXT NULL,
  change_24h_percent NUMERIC NULL,
  fetched_at TIMESTAMPTZ,
  PRIMARY KEY (chain, token_key)
)
```

The mappings table has a `(platform_id, address)` lookup index. Catalog replacement is transactional. Market upserts accept a record only when its `fetched_at` is not older than the stored row.

Redis keys use `cmc:market:<chain>:<token-key>`. Their versioned JSON payload contains the CMC ID and exact fetch timestamp. Writes use the existing compare-before-set timestamp discipline so an older concurrent result cannot replace a newer payload or extend freshness. PostgreSQL promotions use only the remaining portion of the configured TTL.

**Alternative considered:** Add a provider column to generalized tables and migrate CG. Rejected because separate storage isolates identities and avoids unnecessary risk to existing routes.

### 7. Add fresh-only provider lookup alongside existing CG enrichment

The comparison orchestrator needs provider-native records rather than a mutated `wallet.Token`. CoinGecko therefore gains a non-mutating fresh lookup path that reuses its current catalog, Redis/PostgreSQL cache, batching, client, and persistence. The existing `EnrichTokens` method and its stale non-price fallback remain behaviorally unchanged.

CMC implements the same lookup stages with its own catalog and stores:

1. Resolve token keys and current provider IDs.
2. Load matching fresh Redis records.
3. Load matching fresh PostgreSQL records for misses and promote them with remaining TTL.
4. Deduplicate provider IDs and fetch stale/missing IDs in provider-sized sequential batches.
5. Apply valid fetched results immediately, then best-effort persist them.
6. Do not surface any expired record on the new route.

A cached record whose provider ID no longer matches the catalog is a miss. Multiple wallet token identities resolving to one provider ID share one provider request, but retain separate chain/token cache keys.

**Alternative considered:** Reuse the flat `EnrichTokens` output. Rejected because it cannot preserve source identity and can intentionally expose stale CG non-price data, which the comparison route forbids.

### 8. Run both providers concurrently and fail independently

After filtering, the comparison service creates one context with `TOKEN_MARKET_ENRICH_TIMEOUT_SECONDS` and runs CG and CMC lookups concurrently. Each provider processes its own batches sequentially; CMC batches contain at most 50 IDs. The shared cascade publishes progress whenever a cache tier or provider batch applies new results. A fetched batch publishes before its PostgreSQL and Redis writes begin. The comparison service owns a deep copy of each snapshot and replaces it only with a newer pre-deadline snapshot. Provider errors are logged operationally and converted to absent provider objects rather than route errors.

The five-second budget covers each provider's catalog resolution, Redis/PostgreSQL market reads, HTTP calls/retries, and market writes. It does not cover the preceding wallet-store read or existing LI.FI/Moralis filter work. When the deadline expires, the comparison service cancels the shared context and returns the latest immutable snapshot from each provider without waiting for unfinished workers. A provider with completed batches therefore retains those results; only tokens with no published fresh result remain `null`. Publications after cancellation are ignored, and the response receives another deep copy so late work cannot mutate emitted JSON. Pipeline cancellation checks prevent later tiers, batches, and persistence calls from starting; an already-active context-aware operation may continue briefly only while it observes cancellation and unwinds. The route returns `200` whenever a wallet snapshot was loaded, including when every provider value is null.

**Alternative considered:** Run providers sequentially or fail the route when one source fails. Rejected because either choice turns an independent comparison source into avoidable latency or an availability dependency.

### 9. Keep documentation generated and behavior regression-tested

Handler annotations define the new address path parameter, response types, and `400`/`404`/`503` errors. `make docs` updates both internal Swagger outputs and `docs/api/openapi.json`; `make docs-check` remains the drift guard.

Regression tests assert that existing address/native routes make no CMC calls and retain their JSON fields. New tests cover latest-snapshot loading, filtering, CMC HTTP envelopes/queries/pagination, catalog ambiguity, timestamp-safe stores, provider concurrency/deadline behavior, fresh-only partial results, and generated docs.

## Risks / Trade-offs

- **[Keyless CMC rate limits or outages]** → Global market caching, ID deduplication, 50-ID batching, bounded retries, and independent partial responses keep the route available.
- **[Old wallet holdings can be mistaken for current holdings]** → The route explicitly reports `portfolioFetchedAt`; it never implies that the wallet snapshot was refreshed.
- **[CMC catalog pagination increases bootstrap time]** → Persist only supported platforms, install only complete catalogs, and fall back to the prior PostgreSQL snapshot without blocking startup.
- **[CG and CMC legitimately disagree]** → Return labeled source values without choosing, averaging, or deriving a canonical price.
- **[Static chain IDs drift or a chain is added incompletely]** → Fail unregistered networks at startup and require registry/spec tests with every new chain.
- **[Moralis filtering adds latency before comparison]** → Preserve the already-established cache-first and fail-closed behavior so the route's token set stays consistent with the address route.
- **[Concurrent cold requests duplicate provider work]** → Timestamp-guarded idempotent writes prevent corruption; cross-request single-flight or distributed locking is deferred unless observed keyless limits justify the complexity.
- **[Deadline expires after a provider completed batches but before its lookup returns]** → Publish an immutable snapshot before persistence, return the latest published results without extending the public latency budget, and cancel unfinished work. Ignore late publications and prevent subsequent pipeline work from starting after cancellation.

## Migration Plan

1. Add the static registry, configuration validation, CMC client/catalog code, and additive PostgreSQL tables.
2. Deploy the binary; migrations create empty CMC tables and bootstrap attempts to populate the active Ethereum catalog. Failure does not prevent startup.
3. Add fresh-only provider lookup, comparison orchestration, latest wallet snapshot loading, and the new route.
4. Regenerate and publish OpenAPI documentation after endpoint tests pass.
5. Monitor CMC catalog/bootstrap and price failure logs plus route latency before considering authenticated API support or request coalescing.

Rollback uses the prior binary. It ignores the additive CMC tables and Redis namespace, and existing endpoints continue to use the untouched CG paths. No destructive down migration is required.

## Open Questions

None. The route, response, chain identity, provider scope, freshness, failure, persistence, and documentation decisions were confirmed during design review.

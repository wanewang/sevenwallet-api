## Why

Clients need a cache-backed way to compare CoinGecko and CoinMarketCap prices for the same filtered wallet holdings without changing the behavior or response contracts of the existing wallet endpoints. CoinMarketCap's keyless APIs make that comparison possible while preserving the service's current best-effort market-data model.

## What Changes

- Add `GET /v1/tokens/{address}` to load the latest locally stored wallet snapshot, apply the existing LI.FI/Moralis filtering, and return provider-specific `cg` and `cmc` market data.
- Add a keyless CoinMarketCap client, active-cryptocurrency catalog refresh, contract/native ID resolution, batched Simple Price retrieval, and durable PostgreSQL/Redis caching.
- Run CoinGecko and CoinMarketCap enrichment independently and concurrently under a bounded request budget, publishing immutable progress snapshots after completed cache tiers and provider batches. At the deadline, the response preserves every published fresh result while cancelling unfinished work; late publications cannot mutate the response. The route makes no Alchemy request and labels unresolved contract-mapping logs with the provider source.
- Add a static supported-chain registry, initially covering Ethereum, and reject startup configurations for chains absent from that registry.
- Preserve the behavior and payloads of `/v1/addresses/{address}/tokens`, `/v1/addresses/{address}/transactions`, and `/v1/native`.
- Document the new route and its error responses in the generated OpenAPI contract.

## Capabilities

### New Capabilities

- `dual-provider-token-market-query`: Cache-only wallet lookup and fresh, independently cached CoinGecko/CoinMarketCap comparison data through the new token-market route.

### Modified Capabilities

None.

## Impact

- Public API: one additive GET route and new response types; no existing route changes.
- Runtime: new keyless CoinMarketCap catalog and price traffic, plus concurrent dual-provider enrichment for the new route only. Timed-out provider workers are cancelled; completed snapshots remain eligible for the response while in-flight context-aware work unwinds.
- Persistence: new CoinMarketCap mapping and market-data tables and provider-specific Redis keys; existing CoinGecko storage remains unchanged.
- Configuration: CoinMarketCap base URL, catalog refresh, market TTL, and bounded comparison-enrichment settings, together with a static chain registry.
- Documentation and tests: handler/OpenAPI, client, catalog, cache/store, service, startup, and failure-path coverage.

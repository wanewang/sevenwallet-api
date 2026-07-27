## Context

`GET /v1/addresses/{address}/tokens` currently owns the standard wallet-token
flow: validate the address, load a fresh Postgres snapshot or fetch and persist
Alchemy holdings, normalize balances, apply LI.FI allowlist enrichment, validate
unlisted ERC-20s through Moralis, and finally enrich the filtered tokens through
the CoinGecko-backed `MarketEnricher`.

The new `GET /v1/wallet/{address}` endpoint needs the same behavior through the
filtering stage while omitting only that final CoinGecko step. The two routes
must stay aligned for cache, filtering, empty results, errors, and response
shape.

## Goals / Non-Goals

**Goals:**

- Add the dedicated wallet route with the existing `TokenPortfolio` contract.
- Share the complete cache-first and filtering pipeline with the address-token
  route.
- Prevent the new route from invoking CoinGecko on either cache hits or misses.
- Preserve the existing route's empty-portfolio and HTTP error behavior.
- Keep generated OpenAPI and README documentation synchronized with runtime
  behavior.

**Non-Goals:**

- Changing the existing address-token route or its CoinGecko enrichment.
- Adding CoinMarketCap comparison data or changing `/v1/tokens/{address}`.
- Synthesizing a native token when filtering produces an empty portfolio.
- Changing token schemas, cache storage, TTLs, provider retry behavior, or
  persistence schemas.
- Guaranteeing that every price-related field is null; Alchemy and LI.FI data
  produced before CoinGecko enrichment remains part of the shared pipeline.

## Decisions

### Share one token-loading and filtering pipeline

`wallet.Service` will expose `GetWalletTokens` and route both public service
methods through one private token pipeline. A route policy controls only whether
the final `MarketEnricher.EnrichTokens` call executes. Cache lookup, Alchemy
fetching, normalization, persistence, LI.FI enrichment, and Moralis validation
remain structurally shared.

Duplicating the existing service method was considered, but it would allow the
two routes to drift as filtering and cache behavior evolve.

### Omit only CoinGecko enrichment

The new service method disables the final `MarketEnricher` call rather than
constructing a separate provider stack. This keeps all pre-CoinGecko token data,
including Alchemy `price` and LI.FI `priceUSD`, while ensuring the CoinGecko
client, caches, and store cascade are not reached for the new route.

Returning a reduced response type was considered, but the requirement is to
preserve `TokenPortfolio` compatibility with the existing route.

### Preserve empty results and errors exactly

If filtering removes every token, the new route returns `200` with an empty
`tokens` array, as the existing route does. It does not perform a separate native
token lookup or introduce an additional failure mode. Store, upstream, and
unexpected errors continue through the shared API error mapper and retain the
existing `503`, `502`, and `500` mappings.

A zero-balance native fallback was considered, but rejected because it changes
both successful output and error behavior beyond the sole CoinGecko exception.

### Keep the HTTP route explicit

`internal/api` adds a dedicated handler and `WalletService.GetWalletTokens`
interface method. The handler uses the same address validator and service-error
mapping as the existing route. Its OpenAPI operation documents `200`, `400`,
`500`, `502`, and `503` responses and the existing `TokenPortfolio` schema.

## Risks / Trade-offs

- [The two route entry points could diverge later] → Keep loading and filtering
  in one private service pipeline and cover cache-hit, cache-miss, filtering,
  empty-result, and error parity in tests.
- [Developers may interpret “without CoinGecko” as “all prices are null”] →
  Document that only the final CoinGecko enrichment is omitted and retain
  pre-existing Alchemy/LI.FI fields.
- [Generated API documentation may become stale] → Regenerate all OpenAPI
  artifacts from annotations and assert the wallet path and response statuses in
  API tests.

## Migration Plan

1. Add and test the shared service path and dedicated API handler.
2. Regenerate and publish the OpenAPI artifacts with the additive route.
3. Deploy normally; no data or configuration migration is required.
4. Roll back by removing the route and service entry point; stored portfolios
   remain compatible and require no cleanup.

## Open Questions

None.

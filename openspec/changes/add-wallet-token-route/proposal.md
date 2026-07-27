## Why

Clients need a wallet-token endpoint that preserves the existing address-token
pipeline and response contract without invoking CoinGecko enrichment. This
provides the same cache-first holdings and token filtering behavior when market
enrichment is unnecessary.

## What Changes

- Add `GET /v1/wallet/{address}` as an additive public endpoint.
- Reuse the existing address-token route's address validation, cache-first
  Alchemy/Postgres flow, LI.FI allowlist enrichment, Moralis validation,
  `TokenPortfolio` response shape, empty-portfolio behavior, and error mapping.
- Skip the CoinGecko-backed market enrichment step for the new route on both
  cache hits and cache misses.
- Document the endpoint in README and generated OpenAPI artifacts and add
  automated coverage for routing, response statuses, route parity, and
  CoinGecko exclusion.

## Capabilities

### New Capabilities

- `wallet-token-query`: Query a wallet's cache-first, filtered token portfolio
  through a dedicated route without CoinGecko enrichment.

### Modified Capabilities

None.

## Impact

- Affected code: `internal/api` routing and handlers plus the token-portfolio
  orchestration in `internal/wallet`.
- Public API: adds `GET /v1/wallet/{address}` with the existing
  `TokenPortfolio` schema.
- Documentation: updates README and generated OpenAPI JSON/YAML artifacts.
- Dependencies and persistence: no new dependencies, schemas, or migrations.

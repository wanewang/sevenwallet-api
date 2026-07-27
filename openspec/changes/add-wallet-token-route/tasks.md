## 1. Shared Wallet Token Pipeline

- [x] 1.1 Add `GetWalletTokens` and route it through the existing cache-first Alchemy/Postgres, normalization, LI.FI, and Moralis token pipeline.
- [x] 1.2 Make CoinGecko enrichment the sole route policy difference while preserving empty portfolios and existing errors without a native-token fallback.
- [x] 1.3 Cover cache hits, cache misses, filtering, empty results, and absence of market-enricher calls in wallet service tests.

## 2. HTTP Route and Contract

- [x] 2.1 Add `GET /v1/wallet/{address}` to the API router and service interface with the existing address validation and error mapping.
- [x] 2.2 Add handler tests for successful responses, invalid addresses, and unexpected-error mapping to `500`.

## 3. Documentation and Verification

- [x] 3.1 Add complete handler annotations, regenerate all OpenAPI artifacts, and test that the wallet operation documents `200`, `400`, `500`, `502`, and `503`.
- [x] 3.2 Document the route and clarify that CoinGecko is skipped while pre-CoinGecko Alchemy and LI.FI price fields may remain.
- [x] 3.3 Run formatting, the full Go test suite, vet, OpenAPI generation checks, and repository diff checks.

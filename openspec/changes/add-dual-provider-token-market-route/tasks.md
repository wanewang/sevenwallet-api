## 1. Supported Chain and Configuration

- [x] 1.1 Add the static market-provider chain registry with the approved Ethereum identifiers, unit tests for lookup, and fail-fast validation for an unregistered `ALCHEMY_NETWORK`.
- [x] 1.2 Add and test keyless CoinMarketCap base URL, six-hour catalog refresh, 30-minute market TTL, and five-second comparison timeout configuration defaults and positive-duration validation.

## 2. CoinMarketCap HTTP Client

- [x] 2.1 Add CoinMarketCap envelope, map, platform, and Simple Price response types that preserve JSON price numbers exactly and expose numeric provider IDs.
- [x] 2.2 Implement and test the keyless client, including active-map paging parameters, 50-ID Simple Price queries, required USD/change flags, empty-ID behavior, envelope errors, HTTP errors, retries, cancellation, and absence of API-key headers.

## 3. CoinMarketCap Catalog

- [x] 3.1 Implement and test supported-platform mapping construction, address normalization, atomic holder access, native-ID registry lookup, and contract ambiguity resolution with symbol fallback.
- [x] 3.2 Add `coinmarketcap_coin_mappings` schema and PostgreSQL tests/operations for atomic full replacement, complete loading, empty detection, and rollback on invalid or duplicate data.
- [x] 3.3 Implement and test the paginated CoinMarketCap catalog refresher, including all-pages-before-install behavior, six-hour ticks, PostgreSQL bootstrap fallback, prior-catalog retention, and non-fatal empty startup.

## 4. CoinMarketCap Market Persistence

- [x] 4.1 Add provider-specific CMC market record, key, freshness, and cache-write types with tests for exact price text and remaining TTL behavior.
- [x] 4.2 Add `coinmarketcap_market_data` schema and PostgreSQL batch load/save tests, including provider-ID round trips, nullable fields, requested-key filtering, and protection against out-of-order writes.
- [x] 4.3 Add CMC Redis MGET/pipelined persistence under `cmc:market:<chain>:<token-key>`, with tests for payload validation, remaining TTL promotion, namespace isolation, malformed values, and out-of-order write protection.

## 5. Fresh Provider Lookup and Comparison

- [x] 5.1 Add a non-mutating, fresh-only CoinGecko lookup path that returns provider ID, price, and 24-hour change while reusing current CG catalog/cache/fetch persistence and preserving all existing `EnrichTokens` behavior.
- [x] 5.2 Implement and test fresh-only CoinMarketCap lookup across Redis, PostgreSQL, and sequential 50-ID Simple Price batches, including ID deduplication, partial responses, mapping-ID mismatch, cache promotion, and no expired fallback.
- [x] 5.3 Implement and test the dual-provider comparison orchestrator with concurrent CG/CMC work, one shared five-second budget, independent failures, immutable per-provider progress snapshots, nullable partial fields, completed-result preservation at the deadline, and cancellation propagation to unfinished persistence and post-processing.

## 6. Cache-Only Wallet Route

- [x] 6.1 Add and test a latest-wallet-snapshot store operation that ignores the normal TTL, distinguishes never-cached wallets from cached empty portfolios, preserves `fetchedAt`, and leaves `GetFreshTokens` unchanged.
- [x] 6.2 Add the provider-specific market response types and wallet service method that normalize the wallet, reuse existing LI.FI/Moralis filtering, never call Alchemy, preserve filtered order, and map native/contract results into `cg` and `cmc`.
- [x] 6.3 Add `GET /v1/tokens/{address}` to the service interface, router, and handlers with tests for success, stale cached snapshots, invalid wallet `400`, never-cached `404`, storage `503`, partial provider data, and unchanged existing route payloads.

## 7. Server Wiring and Documentation

- [x] 7.1 Wire the supported-chain selection, CoinMarketCap client/catalog refresher, provider services, and comparison service at startup without making CMC catalog availability fatal.
- [x] 7.2 Document the new route, cache-only/stale-portfolio semantics, provider behavior, static Ethereum IDs, and all CoinMarketCap/configuration defaults in README and operational comments.
- [x] 7.3 Add Swagger annotations and regenerate internal/public OpenAPI artifacts for the new address path, response objects, and `400`/`404`/`503` errors.

## 8. Verification

- [x] 8.1 Run focused unit/integration tests for config, CoinMarketCap, catalogs, PostgreSQL, Redis, provider comparison, wallet, and API packages; resolve all failures.
- [x] 8.2 Run the full Go test suite, `go vet`, documentation drift check, formatting/diff checks, and OpenSpec validation; confirm no existing endpoint regression.

## 9. Route and Diagnostic Refinement

- [x] 9.1 Move the cache-only market endpoint from the `wallet` query form to `GET /v1/tokens/{address}`, update handler tests, README, and generated OpenAPI artifacts, and remove the old route.
- [x] 9.2 Label unresolved CoinGecko and CoinMarketCap contract-mapping diagnostics with `source=cg` and `source=cmc`, add log assertions, and rerun verification.

## 10. Provider Deadline Follow-up

- [x] 10.1 Replace the atomic whole-provider handoff with incremental batch publication or a deterministic deadline handoff that preserves fresh batches completed before cancellation without extending the HTTP response budget. Ensure late provider work cannot mutate the emitted response, avoid unnecessary work after the response, and test deadline behavior while PostgreSQL or Redis persistence is active.

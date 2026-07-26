## Why

`add-dual-provider-token-market-route` shipped `internal/cmcmarket/` as a near-verbatim fork of `internal/marketdata/`: same four filenames, the same three-tier lookup cascade, the same `bootstrapFallbackTimeout`, and `Record.Fresh`/`RemainingTTL` copied character-for-character. The two copies have already drifted — `cmcmarket.applyResult` returns a bool and gates `complete[key]` on it, while `marketdata.applyCoinGeckoResult` returns nothing and marks the key complete unconditionally. That drift is not cosmetic: an all-null CoinGecko record is persisted to Postgres and Redis and then, being "fresh", suppresses any refetch for the whole TTL while the route keeps returning `cg: null`. Every future provider fix now has to be written twice, and each rewrite is a chance to diverge again.

## What Changes

- Extract the three-tier lookup cascade (Redis → Postgres promotion → batched provider fetch → persist) into one generic implementation that both `marketdata` and `cmcmarket` drive, parameterised over provider ID type, record type, and result type.
- **BREAKING (behaviour)** — Align the CoinGecko path with the CoinMarketCap one: a record carrying neither a price nor a 24h change no longer marks its key complete and is no longer written to the market tables in Postgres or the market records in Redis. Response shape is unchanged.
- Add an explicit negative cache so the null-payload fix does not turn every dataless token into a provider call on every request: a key whose provider returned nothing is recorded in its own Redis namespace with its own TTL, default 2 hours, configurable via `MARKET_MISS_TTL_SECONDS`. A key with a live miss entry is skipped in the provider batch and its result stays `null`. Miss entries never enter Postgres and never enter the market record namespaces.
- Move `Key`, `ContractKey`, `NativeKey`, and the TTL/freshness arithmetic into a neutral package so `cmcmarket` stops importing `marketdata` for shared vocabulary, and so `Fresh`/`RemainingTTL` exist once.
- Collapse the two `Refresher` types (bootstrap → persist → fall back to the last non-empty Postgres snapshot → tick) into one generic catalog refresher; delete the duplicated `bootstrapFallbackTimeout`.
- Fold `marketdata.EnrichTokens` onto the same cascade via a stale-fallback hook, removing the third in-repo copy of the walk. Its externally observable behaviour on the existing address route is unchanged.
- Deduplicate `cloneString`/`cloneFloat` (three copies across `marketdata`, `cmcmarket`, and `wallet.cloneStringPtr`) into one home.
- Replace the CMC Redis CAS script — currently derived from the CoinGecko script by `strings.Replace` on a literal `local required = {...}` line — with an explicit shared template, so a whitespace edit can no longer silently produce an unvalidated script.

## Capabilities

### New Capabilities
- `market-lookup-pipeline`: the provider-agnostic contract for cache-only market lookup — tier ordering, freshness and promotion rules, batching, persistence-failure tolerance, and the null-payload rule that both providers must obey identically.

### Modified Capabilities
<!-- None. `openspec/specs/` is empty; the dual-provider route's spec is still an
     unarchived delta under add-dual-provider-token-market-route. The externally
     observable route contract from that change is preserved here, except for the
     null-payload rule captured in the new capability above. -->

## Impact

- **Refactored**: `internal/marketdata/{service,record,refresher}.go`, `internal/cmcmarket/{service,record,refresher}.go`, `internal/rediscache/cache.go`, `internal/wallet/service.go` (clone helper), plus a new shared package.
- **Behaviour change**: `GET` token-market comparison — an all-null CoinGecko payload no longer poisons the market record for the market TTL; it lands in the negative cache instead.
- **New config**: `MARKET_MISS_TTL_SECONDS`, default `7200`, shared by both providers; documented in `README.md` alongside the existing market TTLs.
- **New Redis namespaces**: `coingecko:miss:*` and `cmc:miss:*`, holding only a timestamp — no market payload.
- **Unchanged**: HTTP routes, request/response schemas, OpenAPI output, database schema, the existing Redis market namespaces (`coingecko:market:*`, `cmc:market:*`), and the wire payloads both CAS scripts validate.
- **Tests**: `marketdata`, `cmcmarket`, `rediscache`, `store`, and `wallet` suites; new coverage for the null-payload rule on the CoinGecko path.
- **Explicitly out of scope** (raised in the same review, tracked separately): the non-blocking drain in `marketcompare.Compare` that discards a slow provider's completed batches at the deadline; the unused `marketchain.Chain.EVMChainID`; the `MarketChain` field name; two-phase construction via `wallet.Service.SetMarketComparator`.

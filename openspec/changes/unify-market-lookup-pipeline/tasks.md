## 1. Provider-neutral vocabulary

- [x] 1.1 Create `internal/marketkey` holding `Key`, `ContractKey`, `NativeKey`, and `normalizePlatformAddress`, copied verbatim from `internal/marketdata/record.go` so normalisation is unchanged
- [x] 1.2 Add `marketkey.RemainingTTL`/`marketkey.Fresh` as free functions over a `fetchedAt` timestamp, so the arithmetic exists once without disturbing either record's field layout
- [x] 1.3 Add a key-stability test asserting `ContractKey`/`NativeKey` output and the derived `coingecko:market:*` / `cmc:market:*` strings against literals captured from the current implementation
- [x] 1.4 Alias `marketdata.Key = marketkey.Key` so `internal/store` and `internal/rediscache` compile unchanged, then reduce both records' `RemainingTTL`/`Fresh` to one-line delegations to `marketkey`
- [x] 1.5 Drop the `marketdata` import from `internal/cmcmarket` and confirm the CMC package no longer depends on the CoinGecko package
- [x] 1.6 Assert the encoded Redis payload for both record types is byte-identical to the pre-change shape, since both CAS scripts parse `fetchedAt`/`fetchedAtUnixNano`

## 2. Shared cascade

- [x] 2.1 Create `internal/marketpipeline` with the generic `Provider[ID, R]` interface from design.md and the Redis → PostgreSQL → provider cascade
- [x] 2.2 Make `Apply` return `bool`, and have the cascade mark a key complete and append to `fetched` only when it reports true
- [x] 2.3 Implement promotion of fresh PostgreSQL records into Redis with remaining TTL only, skipping records with zero remaining TTL
- [x] 2.4 Implement grouping by resolved provider ID, deterministic ID ordering, batching at `Provider.BatchLimit()`, and a context check before each batch
- [x] 2.5 Make persistence failures log-and-continue: no already-applied result is discarded and remaining batches still run
- [x] 2.6 Unit-test the cascade directly with a fake provider: tier fallthrough, expired-is-a-miss, ID mismatch, promotion TTL, null-payload rejection, batch sizing, cancellation, and save-failure tolerance

## 3. Port CoinMarketCap

- [x] 3.1 Reduce `cmcmarket.Service.LookupFresh` to resolution plus a cascade call, keeping `PriceBatchLimit` at 50 and the `cmcmarket:` log prefix
- [x] 3.2 Delete `matchingMarketRecord`, `incompleteMarketKeys`, and the inlined walk now owned by the cascade
- [x] 3.3 Run `internal/cmcmarket` tests unmodified and confirm they pass

## 4. Port CoinGecko and fix the null-payload defect

- [x] 4.1 Reduce `marketdata.Service.LookupFresh` to resolution plus a cascade call, keeping `priceBatchSize` at 100 and the `marketdata:` log prefix
- [x] 4.2 Convert `applyCoinGeckoResult` into an `Apply` returning false for a record with neither price nor 24h change, so such a key is neither completed nor persisted
- [x] 4.3 Add a test proving an all-null CoinGecko payload is not written to PostgreSQL or the `coingecko:market:*` namespace and leaves the result slot `null`
- [x] 4.4 Add a test proving a price-only and a change-only record each still complete and persist
- [x] 4.5 Run `internal/marketdata` tests and update only assertions that encoded the old cached-null behaviour, noting each in the commit message

## 4a. Negative cache

- [x] 4a.1 Add `MARKET_MISS_TTL_SECONDS` to `internal/config` via `positiveSeconds` with default `7200`, plus a config test covering default and override
- [x] 4a.2 Add `coingecko:miss:*` and `cmc:miss:*` key builders derived from `marketkey.Key`, and extend the group 1 key-stability test to both prefixes
- [x] 4a.3 Add `LoadMisses`/`SaveMisses` to `internal/rediscache` storing a timestamp only, with no market payload and no PostgreSQL write path
- [x] 4a.4 Have the cascade drop keys with a live miss from the batch groups after the PostgreSQL tier, leaving their result slots `null`
- [x] 4a.5 Record a miss only when the provider returned no data for a requested ID — never on a batch error, and never when a payload was returned but was not useful to this caller
- [x] 4a.6 Test: live miss suppresses the provider call; expired miss releases the key; a fresh record from any tier outranks a live miss; a miss never satisfies a market read
- [x] 4a.7 Test that the miss TTL is independent of the market TTL (7200 default against an 1800 market TTL)
- [x] 4a.8 Document `MARKET_MISS_TTL_SECONDS` in `README.md` alongside the existing market TTL variables

## 5. Shared catalog refresher

- [x] 5.1 Add a generic refresher covering bootstrap → persist → install, PostgreSQL fallback under its own `context.WithoutCancel` timeout, tick loop, and keep-prior-catalog on failure
- [x] 5.2 Declare `bootstrapFallbackTimeout` once and delete both per-package copies
- [x] 5.3 Reduce both `Refresher` types to their fetch step — single-call for CoinGecko, paginated for CoinMarketCap — plus their log prefixes
- [x] 5.4 Run both refresher test suites unmodified and confirm they pass

## 6. CAS scripts and clone helpers

- [x] 6.1 Turn `marketCASLua` into a template with one substitution point for the required-fields list and extra validation
- [x] 6.2 Build both scripts by explicit call and delete the `strings.Replace` derivation at `internal/rediscache/cache.go:422`
- [x] 6.3 Test that both scripts reject an out-of-order write, and that the CMC script rejects a payload with a missing or non-positive `coinMarketCapID`
- [x] 6.4 Move `cloneString`/`cloneFloat`/`cloneTime` into one internal package and repoint `marketdata`, `cmcmarket`, and `wallet.cloneStringPtr`; leave `cloneTokens` in `marketdata`

## 7. Enrichment on the cascade

- [x] 7.1 Add the optional stale-record hook to the cascade, tracking the newest expired record per key across Redis and PostgreSQL; when nil, expired records are dropped as today
- [x] 7.2 Give enrichment its own `Apply`, treating a market-cap-only or timestamp-only record as usable — do not reuse the `LookupFresh` predicate, which would discard it
- [x] 7.3 Reduce `marketdata.EnrichTokens` to a cascade call with the hook, keeping its own timeout and its non-mutation of the caller's token slice
- [x] 7.4 Confirm enrichment records a miss only when the provider returned nothing, not when a payload was returned that `LookupFresh` would reject
- [x] 7.5 Run the existing address-route enrichment tests with assertions unmodified; if the hook cannot preserve behaviour, revert this group and leave `EnrichTokens` as-is per design.md
- [x] 7.6 Confirm the stale form still applies 24h change, market cap, and market-data timestamp without setting price or the `Price` object

## 8. Verification

- [x] 8.1 `go build ./...`, `go vet ./...`, and `go test ./...` all clean
- [x] 8.2 `make docs-check` clean, confirming no OpenAPI drift
- [x] 8.3 Run the PostgreSQL-backed tests with `WALLET_TEST_DATABASE_URL` set so the store assertions actually execute rather than skip — 34 previously-skipped store/rediscache tests now run and pass; fixed a pre-existing broken assertion in `TestGetLatestTokensReturnsExpiredAndDistinguishesEmptyFromMissing` (`==` on `time.Time` compares the location pointer, so it could never pass)
- [x] 8.4 Confirm no HTTP route, response schema, or DB schema changed in the diff, and that the only new Redis namespaces are `coingecko:miss:*` and `cmc:miss:*`
- [x] 8.5 Grep for remaining duplication — no second `RemainingTTL`/`Fresh`, no second `bootstrapFallbackTimeout`, no third `cloneString`

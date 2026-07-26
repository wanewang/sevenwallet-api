## Context

`internal/cmcmarket/` was created as a copy of `internal/marketdata/` — same four filenames, same struct shapes, same control flow. The two `LookupFresh` methods (`internal/marketdata/service.go:68-192`, `internal/cmcmarket/service.go:55-184`) are the same 120-line walk differing only in ID type (`string` vs `int64`), record type, cache/store method names, batch limit, and log prefix. `Record.RemainingTTL`/`Fresh` are byte-identical between `internal/marketdata/record.go:49-59` and `internal/cmcmarket/record.go:24-34`. The two `Refresher` types differ only in their fetch step. `cloneString`/`cloneFloat` exist three times (`marketdata`, `cmcmarket`, and as `wallet.cloneStringPtr`). `internal/rediscache/cache.go:422` builds the CMC Lua script by `strings.Replace` against a literal line of the CoinGecko script.

The copies have already drifted once, and the drift is a live defect: `cmcmarket.applyResult` returns a bool that gates `complete[key]`, while `marketdata.applyCoinGeckoResult` returns nothing and the caller sets `complete[key] = true` unconditionally and appends the record to `fetched`. A CoinGecko payload with neither price nor 24h change therefore gets persisted and, being "fresh", suppresses refetch for the full TTL while the route serves `cg: null`.

`cmcmarket` also imports `marketdata` purely for `Key`/`ContractKey`/`NativeKey`, so the CoinMarketCap package depends on the CoinGecko package for vocabulary that belongs to neither.

## Goals / Non-Goals

**Goals:**

- One implementation of the Redis → PostgreSQL → provider cascade, driven by both providers and by address-route enrichment.
- The null-payload rule enforced identically on both paths, fixing the CoinGecko cached-null defect.
- Shared key/record vocabulary in a provider-neutral package; no `cmcmarket` → `marketdata` import.
- One catalog refresher, one `bootstrapFallbackTimeout`, one pair of clone helpers.
- CAS scripts authored explicitly rather than derived by string substitution.
- No change to HTTP contracts, OpenAPI output, DB schema, or Redis key namespaces.

**Non-Goals:**

- The non-blocking drain in `marketcompare.Compare` (`internal/marketcompare/service.go:65-81`) that discards a slow provider's completed batches at the deadline. Real, separately tracked, and touching it here would blur a behaviour fix into a refactor.
- Provider batch limits. CoinGecko stays at 100, CoinMarketCap at 50 — 50 is the documented maximum for its Simple Price endpoint and `coinmarketcap.Client.GetPrices` rejects anything larger.
- The unused `marketchain.Chain.EVMChainID`, the `MarketChain` field name, and `wallet.Service.SetMarketComparator`'s two-phase construction.
- Any change to catalog resolution logic, which genuinely differs per provider.

## Decisions

### Generics over interface-boxing or code generation

The cascade becomes a generic function in a new `internal/marketpipeline` package, parameterised over the provider ID type and record type, with providers supplying the varying steps through one small interface.

Sketch:

```go
type Provider[ID comparable, R any] interface {
    LoadCache(ctx, []marketkey.Key) (map[marketkey.Key]R, error)
    SaveCache(ctx, []Write[R]) error
    LoadStore(ctx, []marketkey.Key) (map[marketkey.Key]R, error)
    SaveStore(ctx, []R) error
    Fetch(ctx, []ID) (map[ID]R, error)   // one batch
    BatchLimit() int
    Matches(r R, expected ID) bool       // stored ID equals resolved ID
    Fresh(r R, now time.Time) bool
    RemainingTTL(r R, now time.Time) time.Duration
    Apply(index int, r R) bool           // false when the payload is all-null
    Logf(string, ...any)
}
```

`Apply` returning `bool` is the unified contract, and it is deliberately the CoinMarketCap shape: the cascade marks a key complete and appends to `fetched` only when `Apply` reports true. That single choice makes the null-payload rule structural rather than a convention each provider has to remember.

**Alternative — `any` plus type assertions.** Rejected: it pushes provider-specific errors from compile time to run time in exactly the code that is meant to become harder to get wrong.

**Alternative — `go:generate` a second copy.** Rejected: it preserves two artifacts to read and review; the duplication just moves into the toolchain.

**Alternative — leave the two copies and only fix the null-payload bug.** Cheapest and lowest risk, and it does resolve the live defect. Rejected because the next provider fix would again need writing twice, which is how this drift arose.

### Key vocabulary moves to `internal/marketkey`

`Key`, `ContractKey`, `NativeKey`, `normalizePlatformAddress`, and the TTL/freshness arithmetic move to `internal/marketkey`. `marketdata.Key` becomes an alias (`type Key = marketkey.Key`) so `internal/store` and `internal/rediscache` signatures compile unchanged during the move and can be migrated file by file.

Normalisation logic is copied verbatim, not rewritten. Redis key strings and Postgres primary keys derive from `Chain`/`TokenKey`, so any change in normalisation would strand every cached and stored record. A test asserts key-string stability against literals captured from the current implementation.

**Alternative — leave `Key` in `marketdata`.** Rejected: it is what forces `cmcmarket` to import the CoinGecko package, and it makes `marketdata` look like the base package rather than a peer.

### Freshness arithmetic as shared free functions

`marketkey.RemainingTTL(fetchedAt, now, ttl)` and `marketkey.Fresh(...)` hold the arithmetic once; each `Record` keeps its own `FetchedAt` field and exposes one-line delegating methods. The record structs stay separate — CoinGecko carries `MarketCapUSD` and `MarketDataUpdatedAt`, CoinMarketCap does not, and merging them would give CoinMarketCap fields it can never populate.

**Revised during implementation.** The original plan was an embedded `marketkey.Fetched` struct. That turned out to break every `Record{..., FetchedAt: x}` composite literal across the test suites, which contradicts the "tests unmodified" guard that groups 3 and 5 rely on to prove the refactor is behaviour-preserving. Free functions reach the same goal — one implementation of the arithmetic — with no literal churn and no change to the stored JSON. The two remaining one-line methods are adapter code, not the duplication being removed.

The stored payload must stay byte-identical (`fetchedAt`, `fetchedAtUnixNano`), because both CAS scripts parse those fields out of the JSON and already-cached records must stay decodable. `internal/rediscache/marketstability_test.go` pins both payload shapes and all four market key strings against captured literals.

### Negative cache in its own namespace

A key the provider has no data for is recorded as a miss in `coingecko:miss:*` / `cmc:miss:*`, holding a timestamp and nothing else, under `MARKET_MISS_TTL_SECONDS` (default 7200). Keys with a live miss are dropped from the batch groups after the PostgreSQL tier and before batching.

The point is that this is *not* the behaviour being fixed, even though both suppress a refetch. The defect is that an all-null payload is written into the market record itself — so a null occupies the slot a real quote should hold, it lands in PostgreSQL as durable state, and its lifetime is the market TTL, which exists to answer "how stale may a price be", a question a missing token has no stake in. The negative cache separates those: market records only ever hold real data, misses are Redis-only and expire on their own clock, and a fresh record always outranks a live miss so a token that starts publishing is picked up as soon as any tier has data for it.

Miss entries are deliberately not persisted to PostgreSQL. A miss is a cheap hint, not durable state, and losing the whole set on a Redis flush costs one round of provider calls.

**Alternative — no negative cache, retry every request.** This was the original plan. Rejected on volume: a wallet holding a dozen dataless tokens would issue provider calls for all of them on every single request, forever.

**Alternative — reuse the market TTL for misses.** Rejected: 1800 seconds is tuned for price staleness, which is a different question from whether the provider carries the token at all. Keeping the two independent means retuning one never silently retunes the other.

**On the 2-hour default.** It is a deliberately conservative first cut. Most of the volume win is in the first order of magnitude — one call per 2 hours instead of one per request already removes essentially all of the repeated-call cost — and the remaining benefit of a longer TTL buys progressively less while extending the window in which a newly-listed token is wrongly reported as `null`. Two hours caps that exposure at four market TTLs. If provider call volume turns out to still be a problem, raising `MARKET_MISS_TTL_SECONDS` is a config change, not a code change.

**Trade-off accepted:** a token the provider starts carrying is still reported `null` for up to 2 hours, unless a fresh record reaches Redis or PostgreSQL by another path first, in which case it outranks the miss immediately.

### Enrichment folded in via a stale hook

`marketdata.EnrichTokens` is the third copy of the walk, differing by tracking the newest expired record per key and applying it when no tier completes. The cascade takes an optional stale hook; when nil (both `LookupFresh` paths) expired records are dropped as they are today.

Concretely, what makes this the riskiest step:

- **The two callers disagree on what "usable" means, on the same record type.** `LookupFresh` treats a record with no price and no 24h change as nothing (`internal/marketdata/service.go:194-197`). `EnrichTokens` does not: `applyFresh` (`:468-484`) also writes `MarketCapUSD` and `MarketDataUpdatedAt`, so a record carrying only a market cap is genuinely useful to the address route and genuinely useless to the comparison route. Once the cascade owns the completion test through `Apply`, the same provider must supply two different `Apply` implementations — and if that is done carelessly, enrichment either starts discarding market-cap-only records or starts recording misses for keys that had data.
- **The negative cache must not leak across callers for the same reason.** A key that is a miss for `LookupFresh` may be perfectly serviceable for enrichment. Misses are recorded only on the provider-returned-nothing path, not on the payload-not-useful-to-me path.
- **`EnrichTokens` writes into a cloned `[]wallet.Token` while `LookupFresh` fills a `[]*wallet.CoinGeckoMarket`.** The cascade must stay agnostic about the destination, which is why `Apply` takes an index and the provider closes over its own output slice.
- **It is the live route.** `EnrichTokens` backs `GET /v1/tokens/{address}`, which has real traffic today; `LookupFresh` backs the comparison route added last commit.

So it lands only after both `LookupFresh` paths are on the cascade and green, and the existing `internal/marketdata` enrichment tests are the guard, with their assertions unmodified.

**Alternative — leave `EnrichTokens` alone.** Defensible, and it is the fallback if the hook makes the cascade signature awkward. Rejected as the default because the duplication would then survive inside `marketdata` itself, which is most of what this change exists to remove.

### CAS scripts from an explicit template

`marketCASLua` becomes a template with one substitution point for the provider's required-fields list and extra validation, and both scripts are built from it by an explicit call. The current `strings.Replace` against a literal `local required = {...}` line fails silently on any whitespace edit — the replacement simply does not apply and the CMC script ships without its `coinMarketCapID` check.

A test asserts both scripts still reject an out-of-order write and a payload missing its provider ID, so a broken template surfaces as a failure rather than as an unvalidated script.

### Clone helpers

`cloneString`, `cloneFloat`, `cloneTime` land in one small internal package; `marketdata`, `cmcmarket`, and `wallet.cloneStringPtr` all call it. `cloneTokens` stays in `marketdata` — it is about `wallet.Token`, not about market records.

## Risks / Trade-offs

- **Generics make the cascade harder to read than either copy was** → keep the `Provider` interface small and named after what varies, document the tier order at the top of the cascade, and keep provider-specific resolution in the provider packages.
- **`EnrichTokens` regression on the live address route** → land it as its own step after the `LookupFresh` paths are green; existing enrichment tests pass unmodified or the step is dropped (the fallback in Decisions above).
- **Moving `Key` changes a cache or DB key** → verbatim copy of normalisation plus a stability test against literal key strings; no schema or namespace edits in this change.
- **The null-payload fix increases provider call volume** → bounded by the negative cache: a dataless key costs one provider call per `MARKET_MISS_TTL_SECONDS`, not one per request.
- **A live miss hides a token that starts publishing** → capped at 2 hours by default and tunable via `MARKET_MISS_TTL_SECONDS`; a fresh record from any tier outranks a live miss, so only keys with no data anywhere stay suppressed.
- **The miss namespace drifts from the market namespace** → both derive from the same `marketkey.Key`, so the key-stability test in group 1 covers both prefixes.
- **The embedded `Fetched` type changes stored JSON** → assert the encoded payload against the current shape before the swap; both CAS scripts parse these fields.
- **Refactor-sized diff across seven files makes review harder** → sequence the tasks so each step compiles and tests green on its own: vocabulary move, then cascade with CoinMarketCap, then CoinGecko `LookupFresh` (which carries the behaviour fix), then refresher, then scripts and clones, then enrichment last.

## Migration Plan

No data migration. Existing Redis keys, Postgres tables, and stored JSON shapes are unchanged; the miss namespaces are new and start empty. `MARKET_MISS_TTL_SECONDS` has a default, so no deploy-time config change is required. The deploy is a plain code rollout and rollback is a revert — reverting leaves orphaned `*:miss:*` keys in Redis, which expire on their own and are read by nothing.

Records already written as all-null before the fix age out naturally at their existing market TTL; until they do, they are served as they are today.

## Open Questions

- Should a miss recorded because the provider returned an error be distinguished from one recorded because the provider returned no data? Current plan records a miss only for the latter — an errored batch leaves the key untouched — but a provider outage then costs full retry volume on every request until it recovers.
- Is `internal/marketpipeline` plus `internal/marketkey` the right split, or should the key vocabulary live inside the pipeline package? Resolve when the first provider is ported — whichever leaves `internal/store` and `internal/rediscache` with the smaller import surface.

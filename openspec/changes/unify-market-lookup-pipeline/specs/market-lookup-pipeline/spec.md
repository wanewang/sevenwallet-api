## ADDED Requirements

### Requirement: Single shared lookup cascade

Both the CoinGecko and CoinMarketCap market services SHALL obtain their records through one shared cascade implementation rather than per-provider copies. The cascade SHALL consult, in order: Redis, then PostgreSQL for keys still incomplete, then the provider API for keys still incomplete. A provider SHALL contribute only its own identifier type, record type, persistence methods, batch limit, and log prefix; it SHALL NOT restate the tier ordering, the completion bookkeeping, or the persistence sequence.

#### Scenario: Redis satisfies every key

- **WHEN** a lookup runs and Redis returns a fresh record whose provider ID matches the expected ID for every requested key
- **THEN** PostgreSQL is not queried, the provider API is not called, and every result slot is populated

#### Scenario: Cascade falls through tier by tier

- **WHEN** Redis returns no usable record for a key and PostgreSQL returns a fresh one
- **THEN** the provider API is not called for that key, and the key is excluded from the provider batch groups

#### Scenario: Both providers share one implementation

- **WHEN** the tier ordering, completion rule, or persistence sequence is changed
- **THEN** the change SHALL take effect for both providers from a single edit site

### Requirement: Expired records are misses, never fallbacks

A fresh lookup SHALL treat a record at or beyond its TTL boundary as absent. Freshness SHALL be computed by one shared implementation: a record is fresh when the positive remainder of `ttl - (now - fetchedAt)` is greater than zero. A record whose stored provider ID differs from the expected provider ID for its key SHALL be treated as absent.

#### Scenario: Expired Redis record does not complete a key

- **WHEN** Redis returns a record whose `fetchedAt` is older than the TTL
- **THEN** the key remains incomplete and falls through to PostgreSQL and then the provider

#### Scenario: Mismatched provider ID is discarded

- **WHEN** a stored record's provider ID differs from the ID the catalog resolved for that key
- **THEN** the record is ignored and the key falls through to the next tier

### Requirement: Promotion of fresh PostgreSQL records

When PostgreSQL supplies a fresh record that Redis did not, the cascade SHALL write that record back to Redis with only its remaining TTL, never a full TTL. A record with zero remaining TTL SHALL NOT be promoted.

#### Scenario: Promotion carries remaining TTL

- **WHEN** a PostgreSQL record was fetched 10 minutes ago under a 30-minute TTL
- **THEN** it is written to Redis with a 20-minute TTL

### Requirement: Null-payload records neither complete nor persist

A record carrying neither a price nor a 24h change SHALL NOT populate a result slot, SHALL NOT mark its key complete, and SHALL NOT be written to the PostgreSQL market tables or the Redis market namespaces. This rule SHALL apply identically to both providers.

#### Scenario: CoinGecko returns an all-null payload

- **WHEN** the CoinGecko API returns a record for a key with both price and 24h change absent
- **THEN** the record is not written to PostgreSQL or the `coingecko:market:*` namespace, the key is not marked complete, and the result slot for that key stays `null`

#### Scenario: Partial payload still completes

- **WHEN** a record carries a 24h change but no price, or a price but no 24h change
- **THEN** the key is marked complete and the record is persisted

### Requirement: Negative cache for dataless keys

When the provider returns no usable payload for a key, the cascade SHALL record a miss in a Redis namespace separate from the market records — `coingecko:miss:*` and `cmc:miss:*` — carrying only a timestamp. The miss TTL SHALL be independent of the market TTL, SHALL default to 2 hours, and SHALL be configurable through `MARKET_MISS_TTL_SECONDS`. A key with a live miss entry SHALL be excluded from the provider batch and SHALL return a `null` result slot. Miss entries SHALL NOT be written to PostgreSQL and SHALL NOT be readable as market records.

#### Scenario: A miss suppresses the provider call while it lives

- **WHEN** a lookup recorded a miss for a key, and a second lookup runs for the same key within the miss TTL
- **THEN** the provider is not called for that key and the result slot stays `null`

#### Scenario: An expired miss releases the key

- **WHEN** a lookup runs for a key whose miss entry has passed its TTL
- **THEN** the key is included in the provider batch again

#### Scenario: A miss never satisfies a market read

- **WHEN** a key has a live miss entry
- **THEN** no market record is returned for it from Redis or PostgreSQL, and nothing is written to the market tables

#### Scenario: Fresh data outranks a stale miss

- **WHEN** a key has a live miss entry but Redis or PostgreSQL also holds a fresh market record for it
- **THEN** the fresh record is applied and the key is marked complete

#### Scenario: Miss TTL is independent of market TTL

- **WHEN** `MARKET_MISS_TTL_SECONDS` is unset and the market TTL is 1800 seconds
- **THEN** miss entries expire after 7200 seconds, not 1800

### Requirement: Provider batching honours limits and cancellation

The cascade SHALL group incomplete keys by resolved provider ID, order the IDs deterministically, and request them in batches no larger than the provider's own batch limit — 100 IDs for CoinGecko, 50 IDs for CoinMarketCap, the maximum its Simple Price endpoint accepts. Before each batch the cascade SHALL check the context and stop when it is done. A batch that fails SHALL be logged and skipped without aborting the remaining batches, unless the context is done.

#### Scenario: Batch size respects the provider limit

- **WHEN** 120 distinct CoinMarketCap IDs are incomplete
- **THEN** the provider client is called three times with at most 50 IDs per call

#### Scenario: Each provider keeps its own limit

- **WHEN** the shared cascade batches CoinGecko IDs
- **THEN** it uses 100 per batch, not the CoinMarketCap limit of 50

#### Scenario: Deadline stops further batches

- **WHEN** the context is cancelled after the first batch returns
- **THEN** no further batch is requested and the records already applied are still returned

#### Scenario: One failing batch does not discard the others

- **WHEN** the second of three batches returns an error and the context is still live
- **THEN** the error is logged and the third batch is still requested

### Requirement: Persistence failures never discard provider data

A PostgreSQL or Redis write failure SHALL be logged and SHALL NOT remove already-applied results or abort the remaining batches. Results SHALL be returned to the caller regardless of persistence outcome.

#### Scenario: Redis save fails after a successful fetch

- **WHEN** the provider returns usable records and the Redis write returns an error
- **THEN** the error is logged and the caller still receives the fetched records

### Requirement: Shared market key and record vocabulary

`Key`, `ContractKey`, `NativeKey`, and the TTL/freshness arithmetic SHALL live in one provider-neutral package. Neither provider package SHALL import the other to obtain this vocabulary.

#### Scenario: No cross-provider import for shared types

- **WHEN** the CoinMarketCap package is compiled
- **THEN** it does not import the CoinGecko package

#### Scenario: Key normalisation is unchanged

- **WHEN** a contract key is built from a mixed-case chain and address, or a native key from a lower-case symbol
- **THEN** the resulting `Chain`, `TokenKey`, and the derived Redis key strings (`coingecko:market:*`, `cmc:market:*`) are byte-identical to those produced before this change

### Requirement: Shared catalog refresher

Both catalog refreshers SHALL be one shared implementation. It SHALL attempt a provider fetch, persist and install the result on success, and otherwise fall back to the last non-empty PostgreSQL snapshot under its own bounded timeout that survives an exhausted caller deadline. It SHALL never fail startup, and on a tick failure it SHALL keep the prior catalog. The bootstrap fallback timeout SHALL be declared once.

#### Scenario: Bootstrap falls back to PostgreSQL

- **WHEN** the provider fetch fails and PostgreSQL holds a non-empty snapshot
- **THEN** the catalog is installed from PostgreSQL and startup continues

#### Scenario: Refresh failure keeps the prior catalog

- **WHEN** a scheduled refresh fails to fetch or to persist
- **THEN** the previously installed catalog remains in place

#### Scenario: Provider-specific fetch stays provider-specific

- **WHEN** CoinMarketCap paginates its catalog and CoinGecko does not
- **THEN** only the fetch step differs; bootstrap, fallback, and tick behaviour come from the shared implementation

### Requirement: Explicitly authored CAS scripts

Each provider's Redis compare-and-set script SHALL be produced from an explicit shared template with provider-specific validation supplied as a parameter. No script SHALL be derived by string substitution against another script's source text.

#### Scenario: Editing one script cannot silently disarm another

- **WHEN** the CoinGecko script's validation line is reformatted
- **THEN** the CoinMarketCap script's validation is unaffected and still rejects a payload with a missing or non-positive `coinMarketCapID`

#### Scenario: Older writes are still rejected

- **WHEN** a write arrives whose `fetchedAtUnixNano` is older than the stored record's
- **THEN** the script rejects it, for both providers

### Requirement: Enrichment reuses the cascade with a stale fallback

Token enrichment on the address route SHALL run on the same shared cascade, supplying a stale-record hook rather than a second copy of the walk. When a key completes no tier, the newest stale record seen across Redis and PostgreSQL SHALL be applied in the reduced stale form. Enrichment SHALL keep its own timeout and SHALL NOT mutate the caller's token slice.

#### Scenario: Stale fallback still applies

- **WHEN** every tier fails for a key but an expired record was seen in PostgreSQL
- **THEN** that record's 24h change, market cap, and market-data timestamp are applied, and no price or `Price` object is set

#### Scenario: Newest stale record wins

- **WHEN** both Redis and PostgreSQL hold expired records for the same key
- **THEN** the one with the later `fetchedAt` is applied

#### Scenario: Address route output is unchanged

- **WHEN** the existing address-route enrichment tests run against the refactored implementation
- **THEN** they pass without modification to their assertions

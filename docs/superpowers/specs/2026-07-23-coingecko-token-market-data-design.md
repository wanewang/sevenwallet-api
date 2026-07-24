# CoinGecko Token Market Data Design

## Summary

Extend `wallet.Token` with CoinGecko 24-hour price-change percentage, USD market
capitalization, and market-data update time. Fresh CoinGecko USD prices also
replace the existing Alchemy or LI.FI price in token responses.

CoinGecko market data is global to a token, not specific to a wallet. The
service therefore stores one shared market record per chain and token identity
instead of duplicating the same values across `wallet_tokens` rows. Redis is the
first-level cache, PostgreSQL is the durable fresh/stale cache, and CoinGecko is
called only for missing or stale records.

Contract-to-CoinGecko-ID mappings come from:

```text
GET https://api.coingecko.com/api/v3/coins/list?include_platform=true
```

The service expands that response into queryable PostgreSQL rows, refreshes it
every six hours, and holds an immutable in-memory lookup index for request-time
resolution. Prices come from:

```text
GET https://api.coingecko.com/api/v3/simple/price
    ?vs_currencies=usd
    &ids=<comma-separated CoinGecko IDs>
    &include_24hr_change=true
    &include_last_updated_at=true
    &include_market_cap=true
```

CoinGecko references:

- [Coins List (ID Map)](https://docs.coingecko.com/reference/coins-list)
- [Coin Price by IDs, Symbols, or Names](https://docs.coingecko.com/reference/simple-price)

## Goals

- Add nullable `change24hPercent`, `marketCapUSD`, and
  `marketDataUpdatedAt` properties to every public `wallet.Token` object.
- Use fresh CoinGecko USD price data to overwrite both existing token price
  representations.
- Share CoinGecko market records across every wallet that holds the same token.
- Read shared records from Redis first, then PostgreSQL, with a configurable
  freshness window (default 30 minutes) based on the original CoinGecko fetch time.
- Batch all stale or missing CoinGecko IDs from one wallet response into as few
  `/simple/price` requests as practical.
- Retry a failed price request three times after the initial attempt, waiting
  one second before each retry.
- Preserve endpoint availability when CoinGecko, Redis market caching, or
  CoinGecko-specific PostgreSQL operations fail.
- Bound total request-time enrichment latency so a slow or degraded CoinGecko
  cannot balloon wallet-endpoint response times.
- Persist a queryable multi-platform CoinGecko mapping catalog and refresh it
  atomically every six hours.
- Enrich native ETH as well as Ethereum ERC-20 tokens.

## Non-goals

- Caching complete wallet portfolios in Redis.
- Adding CoinGecko values to every wallet-specific `wallet_tokens` row.
- Keeping historical prices, market caps, or 24-hour changes.
- Proactively fetching market data for every coin in the CoinGecko catalog.
- Supporting currencies other than USD.
- Replacing the existing LI.FI allowlist, Moralis validation, or Alchemy wallet
  balance fetch.
- Requiring a CoinGecko API key or paid CoinGecko plan.
- Adding distributed refresh locks or cross-instance request coalescing.
- Enabling non-Ethereum wallet endpoints now. The catalog is multi-platform so
  future networks do not require another catalog schema.

## Existing behavior retained

`wallet.Service.GetTokens` remains responsible for the existing cache-first
Alchemy portfolio flow and LI.FI/Moralis filtering. CoinGecko enrichment happens
after filtering, so the service does not spend CoinGecko calls on tokens that
will not be returned.

`GET /v1/native` continues to build its base native token from the current LI.FI
snapshot. CoinGecko enrichment does not make an unavailable LI.FI native token
available; it only overlays market data on a successfully constructed token.

The existing PostgreSQL `wallet_tokens` schema and wallet portfolio freshness
rules do not change.

## Public API contract

Add these fields to `wallet.Token`:

```go
Change24HPercent   *float64 `json:"change24hPercent"`
MarketCapUSD       *float64 `json:"marketCapUSD"`
MarketDataUpdatedAt *string `json:"marketDataUpdatedAt"`
```

The fields deliberately do not use `omitempty`. They are always present and are
JSON `null` when no usable CoinGecko value exists.

Example with CoinGecko data:

```json
{
  "tokenAddress": null,
  "symbol": "ETH",
  "name": "Ether",
  "decimals": 18,
  "rawBalance": "0",
  "balance": "0",
  "isNative": true,
  "price": {
    "currency": "usd",
    "value": "3210.45",
    "lastUpdatedAt": "2026-07-23T10:00:00Z"
  },
  "priceUSD": "3210.45",
  "change24hPercent": -1.23,
  "marketCapUSD": 387123456789.45,
  "marketDataUpdatedAt": "2026-07-23T10:00:00Z"
}
```

`marketDataUpdatedAt` is CoinGecko's `last_updated_at` converted from Unix time
to UTC RFC 3339. The internal time at which this service fetched CoinGecko is
kept separately and is not exposed.

When a fresh CoinGecko record contains `usd`, it overwrites both existing price
representations:

- `Token.PriceUSD` becomes the CoinGecko USD value formatted as a string.
- `Token.Price` becomes a USD `Price` using the same value and CoinGecko update
  time.

If CoinGecko returns a record but a particular market property is `null`, its
public property remains `null`. A missing `usd` value does not erase or
overwrite the existing token price.

Generated OpenAPI files must describe the three new nullable properties for all
endpoints returning `wallet.Token`.

## Architecture

### `internal/coingecko`

A thin HTTP client owns CoinGecko-specific behavior:

- Build and send `/coins/list` requests with `include_platform=true`.
- Build `/simple/price` requests using `url.Values`, including every required
  USD, 24-hour change, market-cap, and update-time parameter.
- Set `Accept: application/json` and the configured `User-Agent` on every
  request.
- Decode response numbers without losing the string representation required by
  the existing `price` and `priceUSD` fields.
- Bound response handling with the request context and a 15-second HTTP-client
  timeout. The shorter enrichment context remains the effective limit for
  request-time price calls.
- Classify errors for the retry policy.

The client exposes domain-neutral CoinGecko response types. It does not know
about wallets, Redis, PostgreSQL, or API response objects.

### `internal/marketdata`

This package owns global market-data orchestration:

- Transform a CoinGecko coin list into normalized mapping rows.
- Maintain an immutable, atomically swappable mapping snapshot.
- Bootstrap and run the six-hour catalog refresh.
- Resolve filtered wallet tokens to CoinGecko IDs.
- Batch Redis and PostgreSQL market-cache operations.
- Fetch, retry, persist, and merge `/simple/price` data.
- Apply the fresh/stale price-precedence rules.
- Enforce a total enrichment time budget across all batches for one response.

It implements a narrow interface consumed by `wallet.Service`, conceptually:

```go
type MarketEnricher interface {
    EnrichTokens(ctx context.Context, tokens []Token) []Token
}
```

Enrichment is optional. The implementation returns the original tokens with the
best usable overlays and logs internal failures instead of turning them into a
wallet API error.

### `internal/store`

PostgreSQL implements:

- Atomic full replacement and complete loading of CoinGecko mapping rows.
- Batch reads of shared market records.
- Batch upserts of successfully fetched shared market records.

### `internal/rediscache`

Redis implements:

- One `MGET` for all mapped token identities in a wallet response.
- Pipelined writes for fresh market records.
- Per-record TTLs based on the remaining part of the original freshness window.

Redis never stores the CoinGecko catalog or complete wallet portfolios.

### `internal/wallet`

`wallet.Service` receives a `MarketEnricher`. It invokes enrichment:

1. after an address portfolio has passed LI.FI/Moralis filtering; and
2. after a native token has been successfully constructed for `/v1/native`.

The wallet package remains the owner of the public `Token` shape and existing
price semantics.

## Coin mapping catalog

### Relational representation

Each object from `/coins/list?include_platform=true` expands into one row per
usable platform entry.

For example, one USDC object may create:

```text
usd-coin | USD Coin | usdc | ethereum | 0xa0b8...
usd-coin | USD Coin | usdc | base     | 0x8335...
```

The row fields are:

- `id`: CoinGecko ID used by `/simple/price`.
- `name`: CoinGecko coin name.
- `symbol`: CoinGecko symbol.
- `chain`: CoinGecko platform key for contract tokens.
- `address`: platform contract address.
- `fetched_at`: shared service fetch time for the complete snapshot.

CoinGecko objects with no usable platform entries become native candidates:

```text
ethereum | Ethereum | eth | eth | native
solana   | Solana   | sol | sol | native
```

For a native candidate, `chain` is the lowercase symbol and `address` is the
literal `native`. This is a lookup namespace; the allowed native-ID list guards
against unrelated or duplicate symbol matches.

Blank platform names or blank platform addresses are ignored. Known EVM
addresses are normalized to lowercase. Case-sensitive non-EVM addresses are
preserved. The current request path only looks up the configured `ethereum`
platform.

The table permits multiple IDs for the same `(chain, address)` so ambiguous
upstream mappings can be detected instead of silently choosing one.

### Snapshot refresh

At startup:

1. Attempt a CoinGecko list fetch.
2. Validate that transformation produced a non-empty mapping set.
3. Atomically persist it to PostgreSQL and then install the in-memory snapshot.
4. If fetch, validation, or persistence fails, load the last complete
   PostgreSQL snapshot.
5. If no usable snapshot exists, leave the holder empty and continue starting
   the API. Tokens will be returned without CoinGecko enrichment.

Every six hours, the refresher repeats the fetch and validation. A successful
refresh replaces PostgreSQL inside one transaction, commits, builds the new
immutable index, and atomically swaps the holder. Any failure retains the prior
PostgreSQL rows and prior in-memory snapshot.

The replacement transaction uses `TRUNCATE` followed by PostgreSQL bulk copy.
`TRUNCATE` is transactional, so an insert or commit failure restores the prior
complete catalog. Concurrent readers see either the old committed snapshot or
the new committed snapshot, never a partial list.

### In-memory indexes and ambiguity

The immutable snapshot indexes:

- contract candidates by normalized `(platform, address)`; and
- native candidates by `(lowercase symbol, native)`.

ERC-20 resolution requires exactly one candidate for the configured platform
and contract address. Zero or multiple candidates means unresolved; the token
is returned without CoinGecko enrichment.

Native resolution:

1. Select native candidates whose symbol equals the token symbol,
   case-insensitively.
2. Restrict candidates to `COINGECKO_NATIVE_IDS`.
3. Require exactly one remaining CoinGecko ID.

The initial native allowlist contains only `ethereum`. Zero or multiple allowed
candidates means unresolved.

## Shared market data

### Identity

Market cache identity is `(chain, token_key)`:

- `chain` is the configured CoinGecko platform, initially `ethereum`.
- ERC-20 `token_key` is the normalized contract address.
- Native `token_key` is `native:<uppercase symbol>`, initially `native:ETH`.

Every record also stores `coingecko_id`. A cache record whose stored ID differs
from the current catalog mapping is treated as a miss. This prevents old market
data from surviving a mapping change. The ID-match gate applies to every use of a
record, including a stale record considered only for non-price fallback: a stale
row whose ID no longer matches the current mapping is discarded, not used, so a
re-mapped token never inherits another coin's stale change, cap, or timestamp.

### Stored record

A shared record contains:

- chain and token key;
- CoinGecko ID;
- nullable USD price;
- nullable 24-hour percentage change;
- nullable USD market cap;
- nullable CoinGecko market update time; and
- the UTC time at which this service fetched the record.

PostgreSQL retains the newest record indefinitely so it can serve as stale
fallback. Redis retains only the fresh cache entry.

### Freshness

The freshness window is `COINGECKO_MARKET_TTL_SECONDS` (default 30 minutes),
denoted `ttl` below. A record is fresh only while:

```text
now - fetched_at < ttl
```

Freshness always uses the original CoinGecko fetch time. Loading a PostgreSQL
record into Redis never resets it. The Redis TTL is:

```text
ttl - (now - fetched_at)
```

If the remaining duration is non-positive, the row is stale and is not written
to Redis. Consumers also verify `fetched_at` and `coingecko_id` after decoding a
Redis value instead of trusting TTL alone.

## Address-token request flow

After existing portfolio loading and filtering:

1. Resolve every returned ERC-20 and native token through the in-memory catalog.
2. Deduplicate token cache identities and CoinGecko IDs.
3. `MGET` all resolved identities from Redis in one round trip.
4. Accept valid, fresh Redis records whose CoinGecko ID matches the current
   mapping.
5. Batch-load only Redis misses from PostgreSQL.
6. Accept fresh PostgreSQL rows and promote them to Redis using their remaining
   TTL.
7. Retain stale PostgreSQL rows whose stored CoinGecko ID matches the current
   mapping separately as possible non-price fallback; discard ID-mismatched rows.
8. Collect the IDs still lacking fresh data.
9. Fetch those IDs from `/simple/price` in chunks of at most 100 unique IDs.
10. Apply successful results to the current response immediately.
11. Batch-upsert successful results to PostgreSQL and pipeline them to Redis.
12. For failed or omitted IDs, apply the stale fallback rules.

Different token identities that resolve to the same CoinGecko ID share one
price request result, which is then written under each applicable token cache
identity.

Price batches are sent sequentially to limit load on the public CoinGecko API.
Failure of one batch does not discard successful results from other batches.

## Price retry policy

Each failed `/simple/price` batch gets one initial attempt and up to three
retries, for four total attempts:

```text
attempt 1
  -> wait 1 second
retry 1
  -> wait 1 second
retry 2
  -> wait 1 second
retry 3
```

Retry:

- network request failures;
- response-body read failures;
- JSON decode failures;
- HTTP `429 Too Many Requests`; and
- HTTP `5xx` responses.

Other HTTP `4xx` responses are terminal because repeating an invalid request
cannot repair it. Retry waits are context-aware; request cancellation stops the
timer and further attempts immediately.

The six-hour `/coins/list` refresher does not use this request-time retry loop.
A failed list refresh keeps the prior snapshot and waits for the next scheduled
run.

## Enrichment time budget

Because enrichment runs inline before the wallet response and price batches are
sent sequentially, a slow or unreachable CoinGecko could otherwise stack per-batch
timeouts and retry waits into a large response delay. Enrichment therefore runs
under a single overall deadline, `COINGECKO_ENRICH_TIMEOUT_SECONDS`, derived from
the request context:

- The deadline covers the whole enrichment pass for one response, not each batch.
- When it expires, in-flight and not-yet-sent CoinGecko fetches stop immediately.
  Because retry waits are context-aware, an expiring deadline also cancels a
  pending retry timer.
- IDs left without a fresh result when the deadline expires fall through to the
  same stale/`null` fallback rules used for a failed fetch. Already-applied fresh
  results and cache writes from earlier batches are retained.

The budget favors bounded latency over completeness: it may be shorter than the
full retry ladder of a single batch, in which case remaining retries are
abandoned and stale fallback covers those IDs. Cache-only responses (all IDs
served from fresh Redis or PostgreSQL) do no CoinGecko work and are unaffected.

## Merge and fallback rules

| CoinGecko state | Existing `price` and `priceUSD` | New market fields |
|---|---|---|
| Fresh Redis record | Overwrite with CoinGecko USD price when present | Use fresh values |
| Fresh PostgreSQL record | Overwrite with CoinGecko USD price when present | Use fresh values |
| New CoinGecko fetch succeeds | Overwrite immediately when USD is present | Use returned values |
| All attempts fail and matching-ID stale PostgreSQL row exists | Keep existing Alchemy/LI.FI price | Use stale change, cap, and market timestamp |
| All attempts fail and no matching-ID stale row exists | Keep existing Alchemy/LI.FI price | Return `null` |

Stale CoinGecko USD price is never used. A stale record older than the freshness
window may provide stale `change24hPercent`, `marketCapUSD`, and
`marketDataUpdatedAt` only, and only when its stored CoinGecko ID still matches
the current catalog mapping. An ID-mismatched stale row is treated as no stale
row: the token keeps its existing price and returns `null` new fields.

A successful CoinGecko result is merged into the current response before cache
persistence. PostgreSQL or Redis write failure therefore does not discard fresh
data from that response.

## PostgreSQL migration

Extend the embedded, idempotent schema with:

```sql
CREATE TABLE IF NOT EXISTS coingecko_coin_mappings (
    id         TEXT        NOT NULL,
    name       TEXT        NOT NULL,
    symbol     TEXT        NOT NULL,
    chain      TEXT        NOT NULL,
    address    TEXT        NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (id, chain, address)
);

CREATE INDEX IF NOT EXISTS coingecko_coin_mappings_lookup_idx
    ON coingecko_coin_mappings (chain, address);

CREATE TABLE IF NOT EXISTS coingecko_market_data (
    chain                TEXT        NOT NULL,
    token_key            TEXT        NOT NULL,
    coingecko_id         TEXT        NOT NULL,
    price_usd            TEXT,
    change_24h_percent   NUMERIC,
    market_cap_usd       NUMERIC,
    market_updated_at    TIMESTAMPTZ,
    fetched_at           TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (chain, token_key)
);
```

`price_usd` is text so PostgreSQL preserves CoinGecko's exact JSON number
literal, including scientific notation, consistently with Redis and fresh API
responses. Migration also converts an existing numeric column to text.

`wallet_tokens` does not gain CoinGecko columns. The global market tables avoid
duplicating one token's market data for every wallet.

## Redis representation

Redis key:

```text
coingecko:market:<chain>:<token_key>
```

Examples:

```text
coingecko:market:ethereum:0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48
coingecko:market:ethereum:native:ETH
```

The JSON payload mirrors the shared market record, including `coingeckoID` and
`fetchedAt`. Redis operations are batched. Malformed entries are logged and
treated as misses.

Redis errors fall through to PostgreSQL or CoinGecko. They never fail the wallet
endpoint.

## Configuration

| Environment variable | Default | Purpose |
|---|---:|---|
| `COINGECKO_BASE_URL` | `https://api.coingecko.com/api/v3` | CoinGecko API root and test override |
| `COINGECKO_USER_AGENT` | `wallet-api/1.0` | Required request `User-Agent` value |
| `COINGECKO_PLATFORM` | `ethereum` | Contract lookup platform and market cache chain |
| `COINGECKO_LIST_REFRESH_SECONDS` | `21600` | Coin mapping refresh interval |
| `COINGECKO_MARKET_TTL_SECONDS` | `1800` | Shared market freshness window |
| `COINGECKO_ENRICH_TIMEOUT_SECONDS` | `5` | Total request-time enrichment budget |
| `COINGECKO_NATIVE_IDS` | `ethereum` | Comma-separated allowed native CoinGecko IDs |

Refresh, TTL, and enrichment-timeout values must be positive integers. Native IDs are trimmed,
deduplicated, and must leave at least one non-empty value. Invalid configuration
fails startup. CoinGecko network availability does not.

The price batch size of 100, three retries, and one-second retry delay are
internal constants for this feature.

## Failure handling

### Catalog bootstrap and refresh

- Fetch, validation, or PostgreSQL replacement failure: retain/load the prior
  PostgreSQL snapshot.
- No prior snapshot: start with an empty mapping holder and skip enrichment.
- Periodic refresh failure: retain the current mapping table and memory holder.
- An empty transformed list is invalid and never replaces usable data.

### Market reads and writes

- Redis read failure: batch-read PostgreSQL.
- PostgreSQL market read failure: attempt CoinGecko without a stale fallback.
- CoinGecko failure after retries: use stale non-price fields from a
  matching-ID record when available; discard ID-mismatched stale rows.
- Enrichment budget expires mid-pass: stop remaining CoinGecko work and apply
  stale/`null` fallback to the unresolved IDs, keeping earlier fresh results.
- PostgreSQL market write failure: log, still write Redis, and return fresh data.
- Redis market write failure: log, keep PostgreSQL durability, and return fresh
  data.
- Both writes fail: return fresh data for the current request but refetch on a
  later request.
- Missing or ambiguous catalog mapping: return the token unchanged except for
  nullable new fields.

These optional enrichment failures do not map to new HTTP error responses.

## Testing strategy

### CoinGecko client tests

- `/coins/list` includes `include_platform=true`, `Accept`, and configured
  `User-Agent`.
- `/simple/price` includes encoded, comma-separated IDs and all required flags.
- Numeric and `null` response properties decode correctly.
- Network, read, decode, `429`, and `5xx` failures are retryable.
- Other `4xx` responses are terminal.
- Exactly four attempts occur with three one-second waits.
- An injected sleeper or clock makes retry tests deterministic and fast.
- Context cancellation interrupts retry waiting.

### Catalog tests

- One coin expands into every usable platform row.
- Blank platform data is ignored.
- Platform-less coins become symbol/native rows.
- EVM address normalization and case-sensitive address preservation are correct.
- Contract lookup is case-insensitive for Ethereum.
- Ambiguous contract mappings do not select an arbitrary ID.
- Native lookup filters by symbol and allowed IDs and requires one result.
- Successful refresh atomically replaces PostgreSQL and the memory holder.
- Fetch, validation, and persistence failures keep the prior snapshot.
- Bootstrap falls back to PostgreSQL and remains non-fatal when both sources are
  empty.

### PostgreSQL and Redis tests

- Mapping bulk replacement is all-or-nothing.
- Mapping rows load with their shared fetch time.
- Market batch upsert/read round-trips nullable numeric fields and timestamps.
- Redis uses one batch read and pipelined writes.
- PostgreSQL promotion uses only the remaining Redis TTL.
- Stale and CoinGecko-ID-mismatched Redis records are misses.
- Stale PostgreSQL rows remain available without becoming fresh.

### Market orchestration tests

- Tokens are resolved and deduplicated before cache calls.
- A fresh Redis hit avoids PostgreSQL and CoinGecko.
- A fresh PostgreSQL hit is promoted to Redis without resetting freshness.
- Missing/stale IDs are fetched in batches of at most 100.
- Multiple token identities sharing an ID use one fetched result.
- Fresh CoinGecko price overwrites `price` and `priceUSD`.
- Stale CoinGecko price does not overwrite existing price.
- Stale change, cap, and market timestamp are used after all fetch attempts fail
  only when the stale record's CoinGecko ID matches the current mapping.
- An ID-mismatched stale row is discarded: existing price is kept and new fields
  are null, as if no stale record existed.
- No stale record preserves existing price and leaves new fields null.
- Partial CoinGecko responses update only returned IDs and fields.
- PostgreSQL and Redis write errors do not fail or discard the current response.
- An expired enrichment budget stops further CoinGecko fetches, retains earlier
  fresh results, and applies stale/`null` fallback to the remaining IDs.

### Wallet and API tests

- CoinGecko enrichment runs after LI.FI/Moralis filtering.
- Both fresh PostgreSQL portfolios and new Alchemy portfolios are enriched.
- Native ETH is resolved through the native candidate and allowed-ID rules.
- `/v1/native` receives the same price and market overlay behavior.
- Every token JSON object contains the three new nullable properties.
- Generated OpenAPI schemas expose the new fields and existing endpoint shapes
  remain unchanged.

### Configuration tests

- Every default is applied.
- Every override is parsed.
- Non-positive refresh/TTL/enrichment-timeout values fail validation.
- Native ID parsing trims and deduplicates values and rejects an empty list.

## Documentation and observability

Update the README configuration table and architecture summary. Log catalog
source and row count, refresh success/failure, retry exhaustion, ambiguous
mappings, malformed cache records, and non-fatal cache persistence failures.

The repository has no metrics framework today, so this feature does not add one.
Logs must not include API secrets; this design introduces none.

## Compatibility and rollout

The database migration only creates new tables and an index. It does not rewrite
existing wallet data. Redis uses a new namespace and does not conflict with
LI.FI or Moralis keys.

Adding nullable fields is backward-compatible for ordinary JSON clients. Clients
that reject unknown properties must update before deployment. If the CoinGecko
catalog is unavailable at rollout, the server still starts and existing token
responses continue with the three new properties set to `null`.

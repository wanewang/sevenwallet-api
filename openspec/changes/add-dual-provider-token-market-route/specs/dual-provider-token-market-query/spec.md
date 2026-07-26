## ADDED Requirements

### Requirement: Cache-only wallet market route
The system SHALL expose `GET /v1/tokens/{address}` and SHALL build its token set from the latest PostgreSQL wallet snapshot for the configured network, regardless of the snapshot's age. The route MUST NOT call Alchemy to create or refresh wallet holdings.

#### Scenario: Cached wallet snapshot exists
- **WHEN** a request supplies a valid wallet address with a stored snapshot
- **THEN** the system loads that latest snapshot without applying the normal wallet-snapshot freshness TTL
- **AND** the system does not call Alchemy

#### Scenario: Cached snapshot is old
- **WHEN** the latest stored wallet snapshot is older than the normal address-route cache TTL
- **THEN** the system still uses that snapshot and reports its original fetch time as `portfolioFetchedAt`

#### Scenario: Wallet has never been cached
- **WHEN** a valid wallet address has no stored snapshot for the configured network
- **THEN** the system returns HTTP `404` with `{"error":"wallet token cache not found"}`
- **AND** the system does not call Alchemy, CoinGecko, or CoinMarketCap

#### Scenario: Wallet path address is invalid
- **WHEN** the `address` path parameter is not a `0x`-prefixed 20-byte hexadecimal address
- **THEN** the system returns HTTP `400` with `{"error":"invalid wallet"}`

#### Scenario: Wallet storage read fails
- **WHEN** PostgreSQL cannot determine or load the latest wallet snapshot
- **THEN** the system returns HTTP `503` with `{"error":"storage unavailable"}`

### Requirement: Existing wallet filtering is reused
The new route SHALL apply the same native-token handling, LI.FI enrichment, and Moralis validation rules as `GET /v1/addresses/{address}/tokens` before resolving or fetching market data. Unlisted ERC-20 tokens SHALL be validated through the existing cache-first Moralis path, including a Moralis request when the existing validator requires one.

#### Scenario: LI.FI-listed token is cached
- **WHEN** the wallet snapshot contains a token present in the current LI.FI list
- **THEN** the new route keeps and enriches that token using the existing LI.FI behavior

#### Scenario: Unlisted token has no fresh validation verdict
- **WHEN** the wallet snapshot contains an unlisted ERC-20 token for which the existing validator requires a Moralis lookup
- **THEN** the new route performs that lookup and applies the same keep-or-drop result as the address route

#### Scenario: Token validation is unsuccessful
- **WHEN** an unlisted token is invalid or Moralis validation fails
- **THEN** the new route drops that token without failing the portfolio response

### Requirement: Dual-provider response contract
The route SHALL return a portfolio envelope containing `wallet`, `network`, `portfolioFetchedAt`, and `tokens`. Each returned token SHALL contain `tokenAddress`, `symbol`, `name`, `decimals`, `balance`, `cg`, and `cmc`. The `cg` object SHALL use a string `id`; the `cmc` object SHALL use an integer `id`; and both objects SHALL contain nullable `priceUSD` strings and nullable numeric `change24hPercent` values.

#### Scenario: Both providers return complete data
- **WHEN** a filtered token has fresh valid price and 24-hour change data from both providers
- **THEN** its `cg` and `cmc` objects contain their provider-specific IDs, exact USD price strings, and numeric 24-hour percentage changes

#### Scenario: Provider returns one usable field
- **WHEN** a mapped provider has a fresh valid price or 24-hour change but not both
- **THEN** the system returns that provider object with the unavailable field set to JSON `null`

#### Scenario: Provider has no usable fresh fields
- **WHEN** a provider is unmapped, unavailable, expired without a successful refresh, or returns neither a valid price nor a valid 24-hour change
- **THEN** the corresponding `cg` or `cmc` property is JSON `null`

#### Scenario: Native token is returned
- **WHEN** the filtered wallet snapshot includes the native token
- **THEN** its `tokenAddress` is JSON `null` and its provider data is resolved through the configured native provider IDs

### Requirement: Existing endpoint behavior remains unchanged
The change MUST NOT add CoinMarketCap calls or alter the response schemas, caching semantics, or CoinGecko enrichment behavior of `GET /v1/addresses/{address}/tokens`, `GET /v1/addresses/{address}/transactions`, or `GET /v1/native`.

#### Scenario: Existing token portfolio route is called
- **WHEN** a client requests `GET /v1/addresses/{address}/tokens`
- **THEN** the system follows its existing cache-first Alchemy and CoinGecko flow and emits its existing response shape without `cg` or `cmc` objects

#### Scenario: Existing native route is called
- **WHEN** a client requests `GET /v1/native`
- **THEN** the system follows its existing LI.FI and CoinGecko flow without calling CoinMarketCap or changing its response shape

### Requirement: Static supported-chain registry
The system SHALL resolve provider chain identifiers through a static supported-chain registry keyed by the configured Alchemy network. The initial `eth-mainnet` entry SHALL use EVM chain ID `1`, market chain key `ethereum`, CoinMarketCap platform ID `1`, CoinMarketCap native cryptocurrency ID `1027`, CoinGecko platform `ethereum`, and CoinGecko native ID `ethereum`. Adding another supported chain MUST include a corresponding registry entry and tests.

#### Scenario: Ethereum is configured
- **WHEN** the service starts with `ALCHEMY_NETWORK=eth-mainnet`
- **THEN** the system selects the Ethereum registry entry and uses CoinMarketCap platform ID `1` for contract mappings and cryptocurrency ID `1027` for native ETH prices

#### Scenario: Configured network is not registered
- **WHEN** `ALCHEMY_NETWORK` does not have a supported-chain registry entry
- **THEN** configuration loading or server startup fails with an error that identifies the unregistered network

### Requirement: Keyless CoinMarketCap client
The system SHALL use CoinMarketCap's keyless public API without an API key or `X-CMC_PRO_API_KEY` header. The base URL SHALL be configurable and SHALL default to `https://pro-api.coinmarketcap.com/public-api`.

#### Scenario: CoinMarketCap request is created
- **WHEN** the service requests a CoinMarketCap catalog or Simple Price batch
- **THEN** the request uses the configured keyless base URL and contains no API-key header

#### Scenario: Base URL is overridden
- **WHEN** `COINMARKETCAP_BASE_URL` is configured
- **THEN** all CoinMarketCap requests use that base URL while retaining the documented endpoint paths and queries

### Requirement: Active CoinMarketCap catalog lifecycle
The system SHALL fetch the active CoinMarketCap cryptocurrency map during bootstrap and every six hours by default. It SHALL paginate with at most 5,000 entries per request, SHALL request only active cryptocurrencies, SHALL build mappings only for platform IDs in the supported-chain registry, and SHALL atomically replace the persisted catalog only after every page succeeds and produces at least one usable mapping.

#### Scenario: Complete catalog fetch succeeds
- **WHEN** every active-map page succeeds during bootstrap or refresh
- **THEN** the system atomically replaces the CoinMarketCap mapping table and in-memory catalog with the newly built supported-platform mappings

#### Scenario: Catalog requires multiple pages
- **WHEN** an active-map page contains the requested 5,000 entries
- **THEN** the system requests the next page using the next one-based `start` offset until a shorter page is received

#### Scenario: Catalog page fails
- **WHEN** any page fails, cannot be decoded, or reports a CoinMarketCap API error
- **THEN** the system does not install or persist a partial catalog and retains the prior catalog

#### Scenario: Bootstrap fetch is unavailable
- **WHEN** the bootstrap catalog fetch fails
- **THEN** the system attempts to load the last non-empty CoinMarketCap mapping snapshot from PostgreSQL
- **AND** server startup continues whether or not that fallback exists

#### Scenario: No CoinMarketCap catalog is available
- **WHEN** neither a fetched nor persisted non-empty CoinMarketCap catalog is available
- **THEN** the server starts with CoinMarketCap enrichment unavailable and the new route can still return CoinGecko data

### Requirement: CoinMarketCap token ID resolution
For a contract token, the system SHALL first select active CoinMarketCap candidates by supported platform ID and normalized contract address. It SHALL accept a sole candidate; when multiple candidates remain, it SHALL filter them by case-insensitive equality with the filtered wallet token symbol and accept the result only if exactly one candidate remains. Native tokens SHALL use the static registry's native CoinMarketCap cryptocurrency ID.

#### Scenario: Contract has one candidate
- **WHEN** platform ID and normalized contract address identify exactly one active CoinMarketCap ID
- **THEN** the system uses that ID without requiring a symbol match

#### Scenario: Symbol disambiguates duplicate candidates
- **WHEN** multiple IDs share a platform and contract address but exactly one candidate symbol matches the wallet token symbol case-insensitively
- **THEN** the system uses the matching candidate ID

#### Scenario: Duplicate candidates remain ambiguous
- **WHEN** multiple contract candidates still match the same wallet token symbol or no candidate symbol matches
- **THEN** the system skips CoinMarketCap enrichment for that token

#### Scenario: Native Ethereum is resolved
- **WHEN** the wallet token is native ETH under the Ethereum registry entry
- **THEN** the system requests CoinMarketCap cryptocurrency ID `1027`

### Requirement: Provider-attributed mapping diagnostics
When a contract token cannot be resolved to a provider ID, the system SHALL log the unresolved chain and contract address together with a provider source field whose value is `cg` for CoinGecko or `cmc` for CoinMarketCap.

#### Scenario: CoinGecko contract mapping is unresolved
- **WHEN** CoinGecko cannot resolve a contract mapping
- **THEN** the diagnostic identifies `source=cg` together with the chain and contract address

#### Scenario: CoinMarketCap contract mapping is unresolved
- **WHEN** CoinMarketCap cannot resolve a contract mapping
- **THEN** the diagnostic identifies `source=cmc` together with the chain and contract address

### Requirement: Independent fresh market caches
CoinGecko and CoinMarketCap SHALL use independent market freshness settings, both defaulting to 30 minutes. The new route SHALL use only records whose provider ID matches the current catalog resolution and whose original provider fetch time remains strictly within that provider's TTL. Loading a record from PostgreSQL into Redis MUST preserve its original fetch time and remaining TTL.

#### Scenario: Fresh Redis record exists
- **WHEN** a provider's Redis record matches the current provider ID and is within its TTL
- **THEN** the system returns that record without reading PostgreSQL or calling that provider for the token

#### Scenario: Fresh PostgreSQL record exists
- **WHEN** Redis misses but a matching PostgreSQL record is within its TTL
- **THEN** the system returns the PostgreSQL record, promotes it to Redis for only its remaining TTL, and does not call that provider for the token

#### Scenario: Stored provider ID is obsolete
- **WHEN** a cached record's provider ID differs from the current catalog resolution
- **THEN** the system treats the record as a miss and does not return it as current data

#### Scenario: Record is expired and refresh fails
- **WHEN** a saved market record is outside its provider TTL and the provider cannot refresh it
- **THEN** the new route does not return any field from that expired record

### Requirement: Concurrent bounded provider enrichment
After wallet filtering, the new route SHALL run CoinGecko and CoinMarketCap market resolution, cache access, fetching, and cache writes concurrently under one configurable five-second market-enrichment budget by default. Each provider SHALL publish an immutable progress snapshot whenever a cache tier or provider batch applies new fresh results. A fetched batch SHALL publish before its PostgreSQL and Redis persistence begins. When the deadline expires, the route SHALL cancel unfinished work and return the latest pre-deadline snapshot from each provider without waiting for unfinished workers. Results published after cancellation SHALL NOT alter the emitted response, and pipeline work that has not already started SHALL NOT begin after cancellation.

#### Scenario: Both providers complete within budget
- **WHEN** CoinGecko and CoinMarketCap complete their work before the market-enrichment deadline
- **THEN** the route returns all fresh usable data from both providers

#### Scenario: One provider is slow or unavailable
- **WHEN** one provider exceeds the deadline or fails while the other provider succeeds
- **THEN** the route returns the successful provider data and sets unavailable provider objects to JSON `null`

#### Scenario: Budget expires after some batches complete
- **WHEN** the deadline expires after one or more provider batches have completed but before that provider's lookup returns
- **THEN** the route cancels the provider context and returns a partial HTTP `200` response containing the fresh results from every completed batch
- **AND** tokens without a published fresh result retain JSON `null` for that provider
- **AND** late publications are discarded and cannot mutate the emitted response

#### Scenario: Persistence is active at the deadline
- **WHEN** a provider is writing completed batch data to PostgreSQL or Redis when the market-enrichment deadline expires
- **THEN** the route returns without waiting for the write or provider goroutine and includes the snapshot published before that persistence began
- **AND** the cancelled context is propagated to the active write, which may continue briefly while it exits
- **AND** no later provider batch or persistence operation is started after cancellation

#### Scenario: Both providers are unavailable
- **WHEN** neither provider produces usable fresh data but the wallet snapshot was loaded successfully
- **THEN** the route returns HTTP `200` with the filtered tokens and JSON `null` provider objects

### Requirement: Batched CoinMarketCap Simple Price retrieval
The system SHALL deduplicate resolved CoinMarketCap cryptocurrency IDs and request them through `/v1/simple/price` in sequential batches of at most 50 IDs. Each request SHALL use comma-separated `ids`, `convert=USD`, and `include_percent_change_24h=true`. Partial response data SHALL be associated by CoinMarketCap ID and SHALL update only the returned IDs.

#### Scenario: Multiple CoinMarketCap IDs require one batch
- **WHEN** at most 50 distinct stale or missing CoinMarketCap IDs require refresh
- **THEN** the system issues one Simple Price request containing their comma-separated IDs and the required USD and 24-hour-change parameters

#### Scenario: More than 50 CoinMarketCap IDs require refresh
- **WHEN** more than 50 distinct CoinMarketCap IDs require refresh
- **THEN** the system sends sequential batches containing no more than 50 IDs each while the shared enrichment context remains active

#### Scenario: Simple Price response is partial
- **WHEN** CoinMarketCap omits one or more requested IDs or market fields
- **THEN** the system updates and caches only returned valid fields and leaves omitted provider results unavailable

### Requirement: Separate CoinMarketCap persistence
CoinMarketCap catalog and market records SHALL be stored separately from existing CoinGecko records. PostgreSQL SHALL use `coinmarketcap_coin_mappings` and `coinmarketcap_market_data`; Redis market keys SHALL use `cmc:market:<chain>:<token-key>`. A CoinMarketCap market record SHALL retain its CoinMarketCap ID, exact JSON USD-price representation, nullable 24-hour change, and original fetch timestamp.

#### Scenario: Fresh CoinMarketCap result is received
- **WHEN** CoinMarketCap returns at least one valid requested market field
- **THEN** the system makes that data immediately available to the current response and best-effort persists it to PostgreSQL and Redis under the CMC namespace

#### Scenario: Older write arrives after newer data
- **WHEN** an older CoinMarketCap record is saved after a newer record for the same market key
- **THEN** neither PostgreSQL nor Redis replaces the newer record or extends its freshness

#### Scenario: CoinGecko and CoinMarketCap share a token key
- **WHEN** both providers cache data for the same chain and token key
- **THEN** their IDs, values, timestamps, rows, and Redis payloads remain independent

### Requirement: Generated API documentation
The generated OpenAPI contract SHALL document `GET /v1/tokens/{address}`, its required `address` path parameter, its portfolio and provider response objects, and its `400`, `404`, and `503` error responses.

#### Scenario: API documentation is regenerated
- **WHEN** the repository's documentation generation command runs after implementation
- **THEN** the committed internal and public OpenAPI files contain the new route and match the annotated Go contract

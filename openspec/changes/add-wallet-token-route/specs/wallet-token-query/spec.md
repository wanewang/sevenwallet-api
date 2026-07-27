## ADDED Requirements

### Requirement: Dedicated wallet token endpoint
The system SHALL expose `GET /v1/wallet/{address}` and return the existing
`TokenPortfolio` response shape for a valid wallet address.

#### Scenario: Valid wallet request
- **WHEN** a client sends `GET /v1/wallet/{address}` with a valid 20-byte EVM address
- **THEN** the system returns the wallet's filtered token portfolio using the `TokenPortfolio` schema

#### Scenario: Invalid wallet address
- **WHEN** a client sends `GET /v1/wallet/{address}` with an invalid address
- **THEN** the system returns `400` with the standard error response

### Requirement: Address-token pipeline parity
The wallet-token endpoint SHALL use the same address normalization, cache-first
Alchemy/Postgres flow, balance normalization, LI.FI allowlist enrichment, and
Moralis validation behavior as `GET /v1/addresses/{address}/tokens`.

#### Scenario: Fresh wallet snapshot exists
- **WHEN** a fresh wallet snapshot exists in Postgres
- **THEN** the system returns the filtered cached snapshot without calling Alchemy

#### Scenario: Fresh wallet snapshot does not exist
- **WHEN** no fresh wallet snapshot exists in Postgres
- **THEN** the system fetches holdings from Alchemy, normalizes and persists the snapshot, and applies the normal LI.FI and Moralis filtering pipeline

#### Scenario: Filtering removes every token
- **WHEN** the shared filtering pipeline removes every token from the wallet snapshot
- **THEN** the system returns `200` with an empty `tokens` array and does not synthesize a native-token fallback

### Requirement: CoinGecko enrichment exclusion
The wallet-token endpoint MUST NOT invoke the CoinGecko-backed market enrichment
step. All token data produced before that step SHALL otherwise be preserved.

#### Scenario: Cache-hit request skips CoinGecko
- **WHEN** the wallet-token endpoint serves a fresh cached snapshot
- **THEN** it does not call the market enricher or CoinGecko

#### Scenario: Cache-miss request skips CoinGecko
- **WHEN** the wallet-token endpoint fetches and persists an Alchemy snapshot
- **THEN** it does not call the market enricher or CoinGecko after filtering

#### Scenario: Pre-CoinGecko price data exists
- **WHEN** Alchemy supplies `price` data or LI.FI supplies `priceUSD` during the shared pipeline
- **THEN** the wallet-token response preserves that data without adding or overwriting it through CoinGecko

### Requirement: Existing error contract
The wallet-token endpoint SHALL preserve the existing address-token route's
error mapping and SHALL NOT introduce errors that arise only from additional
fallback behavior.

#### Scenario: Alchemy request fails
- **WHEN** the shared token pipeline cannot load a fresh cache and the Alchemy request fails
- **THEN** the endpoint returns `502` with the standard error response

#### Scenario: Token storage fails
- **WHEN** loading or saving the wallet snapshot fails
- **THEN** the endpoint returns `503` with the standard error response

#### Scenario: Unexpected service error occurs
- **WHEN** the wallet service returns an error outside the known upstream and storage categories
- **THEN** the endpoint returns `500` with the standard error response

### Requirement: Wallet endpoint documentation
The generated OpenAPI specification SHALL describe the wallet-token operation,
its `TokenPortfolio` response, and its supported HTTP error responses.

#### Scenario: OpenAPI artifacts are generated
- **WHEN** API documentation is generated from handler annotations
- **THEN** `/wallet/{address}` documents `200`, `400`, `500`, `502`, and `503` responses

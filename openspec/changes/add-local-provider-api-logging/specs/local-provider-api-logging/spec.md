## ADDED Requirements

### Requirement: Local provider diagnostics activation
The system SHALL enable provider API diagnostic logging only when `PROVIDER_API_LOGGING` is explicitly set to `true` and the process is running outside GCP Cloud Run. The system SHALL keep diagnostics disabled by default and SHALL disable them whenever the `K_SERVICE` environment variable is present, regardless of the opt-in value.

#### Scenario: Local process explicitly enables diagnostics
- **WHEN** the service starts with `PROVIDER_API_LOGGING=true` and without `K_SERVICE` in its environment
- **THEN** provider API diagnostic logging is enabled for the Alchemy, Moralis, and CoinGecko clients

#### Scenario: Local process does not opt in
- **WHEN** the service starts without `PROVIDER_API_LOGGING=true`
- **THEN** provider API diagnostic logging is disabled

#### Scenario: Cloud Run process suppresses diagnostics
- **WHEN** the service starts with `K_SERVICE` in its environment, including when `PROVIDER_API_LOGGING=true`
- **THEN** no provider API diagnostic entries are emitted by the Alchemy, Moralis, or CoinGecko clients

### Requirement: Provider connection logging
When provider API diagnostic logging is enabled, the system SHALL emit a connection entry immediately before each actual HTTP request attempt to Alchemy, Moralis, or CoinGecko. Each connection entry SHALL identify the provider, semantic operation, HTTP method, and non-secret inputs needed to understand the request, except that a CoinGecko coin-list entry SHALL contain only its sanitized, fully resolved request URL.

#### Scenario: Alchemy request is attempted locally
- **WHEN** an Alchemy token or transfer operation issues an HTTP request in a local run
- **THEN** the system logs an Alchemy connection entry describing that operation before sending the request

#### Scenario: Moralis request is attempted locally
- **WHEN** a Moralis token metadata operation issues an HTTP request in a local run
- **THEN** the system logs a Moralis connection entry describing that operation before sending the request

#### Scenario: CoinGecko price request is attempted locally
- **WHEN** a CoinGecko price operation issues an HTTP request in an opted-in local run
- **THEN** the system logs a CoinGecko connection entry describing that operation before sending the request

#### Scenario: CoinGecko coin-list request is attempted locally
- **WHEN** a CoinGecko coin-list operation issues an HTTP request in an opted-in local run
- **THEN** the system logs only the sanitized, fully resolved request URL for that operation

#### Scenario: CoinGecko price request is retried locally
- **WHEN** CoinGecko retries a price request
- **THEN** the system emits a separate connection entry for every HTTP attempt

### Requirement: Provider result logging
When provider API diagnostic logging is enabled, the system SHALL emit the successfully decoded result of each Alchemy, Moralis, and CoinGecko price request. If one of those attempts fails, the system SHALL instead emit sanitized outcome context that identifies the provider, operation, and available error or HTTP status information. CoinGecko coin-list requests SHALL emit no response, status, or error-detail diagnostic beyond their sanitized request URL.

#### Scenario: Successful provider response
- **WHEN** Alchemy, Moralis, or a CoinGecko price request returns a successful response that the client decodes
- **THEN** the system logs the provider and operation together with the decoded result used by the application

#### Scenario: Successful CoinGecko coin-list response
- **WHEN** CoinGecko returns a successful coin-list response
- **THEN** the system does not log the decoded response or response body

#### Scenario: Failed CoinGecko coin-list response
- **WHEN** a CoinGecko coin-list request returns a non-success status or encounters a transport or decoding error
- **THEN** the provider diagnostic logger emits no status, response, or error details beyond the request URL already logged

#### Scenario: Provider returns a non-success status
- **WHEN** an Alchemy, Moralis, or CoinGecko price request returns a non-success HTTP status
- **THEN** the system logs the provider, operation, and status code without logging the response body

#### Scenario: Provider transport or decode failure
- **WHEN** an Alchemy, Moralis, or CoinGecko price request fails during transport or response decoding
- **THEN** the system logs the provider, operation, and sanitized error context

#### Scenario: No provider request occurs
- **WHEN** a client operation completes without issuing an HTTP request, such as a CoinGecko price lookup with no IDs
- **THEN** the system does not emit a connection or result entry for that operation

### Requirement: Provider credential redaction
Provider API diagnostic logs MUST NOT contain API keys, authorization header values, or credential-bearing endpoint URLs in either local or Cloud Run environments.

#### Scenario: Alchemy diagnostics are emitted
- **WHEN** the Alchemy client logs a connection, result, or failure
- **THEN** the log contains neither the Alchemy API key nor the credential-bearing request URL

#### Scenario: Moralis diagnostics are emitted
- **WHEN** the Moralis client logs a connection, result, or failure
- **THEN** the log contains neither the Moralis API key nor the `X-API-Key` header value

#### Scenario: Error contains credential text
- **WHEN** an underlying provider error includes a known API key or credential-bearing URL
- **THEN** the diagnostic log replaces the credential text before emission

### Requirement: Diagnostic instrumentation preserves provider behavior
Enabling or disabling provider API diagnostic logging MUST NOT change provider HTTP request methods, URLs, queries, headers, or bodies; provider call and retry behavior; values returned to callers; or errors returned to callers. Diagnostic formatting and emission SHALL remain observational and SHALL NOT participate in provider control flow.

#### Scenario: Request construction is unchanged
- **WHEN** the same Alchemy, Moralis, or CoinGecko operation runs with diagnostics disabled and enabled
- **THEN** both runs send equivalent HTTP methods, URLs, queries, headers, and request bodies to the provider

#### Scenario: CoinGecko retry behavior is unchanged
- **WHEN** the same retryable CoinGecko price failure occurs with diagnostics disabled and enabled
- **THEN** both runs make the same retry decisions and number of provider attempts

#### Scenario: Successful return value is unchanged
- **WHEN** the same successful provider response is processed with diagnostics disabled and enabled
- **THEN** both runs return equivalent values to the caller

#### Scenario: Returned error is unchanged
- **WHEN** the same provider failure is processed with diagnostics disabled and enabled
- **THEN** both runs return equivalent errors to the caller

#### Scenario: Client has no logging hook
- **WHEN** a provider client is constructed without a diagnostic logging hook
- **THEN** diagnostic calls are no-ops and the client completes with its existing value or error behavior

### Requirement: LI.FI is excluded from provider diagnostics
The `PROVIDER_API_LOGGING` feature SHALL apply only to Alchemy, Moralis, and CoinGecko. It MUST NOT add request, response, result, status, or error-detail diagnostics to the LI.FI client, and it SHALL NOT change existing LI.FI request or operational logging behavior.

#### Scenario: LI.FI request occurs while diagnostics are enabled
- **WHEN** the service issues a LI.FI request while `PROVIDER_API_LOGGING=true` in an eligible local environment
- **THEN** the feature emits no LI.FI request URL, request data, response data, result, status, or error-detail diagnostic

#### Scenario: LI.FI behavior remains unchanged
- **WHEN** provider diagnostics are enabled or disabled
- **THEN** LI.FI requests, returned values, errors, and existing token-list operational logs behave equivalently

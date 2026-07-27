## 1. Diagnostic Logging Foundation

- [x] 1.1 Add provider-local, nil-safe `logf func(string, ...any)` hooks and optional variadic constructor options, plus shared pure helpers where useful for consistent JSON result serialization and known-secret redaction.
- [x] 1.2 Add unit tests proving nil hooks are no-ops, enabled hooks emit semantic fields and decoded results, and known credentials are redacted without requiring existing constructor calls or direct client struct literals to change.

## 2. Runtime Activation and Wiring

- [x] 2.1 Add configuration that enables provider diagnostics only when `PROVIDER_API_LOGGING=true` and `K_SERVICE` is absent, with tests for default-off, explicit local opt-in, non-true values, and the Cloud Run override.
- [x] 2.2 Have `cmd/server` apply `log.Printf` through each provider client's optional hook only when configuration enables diagnostics; leave hooks nil otherwise.

## 3. Provider Client Instrumentation

- [x] 3.1 Instrument every Alchemy token and transfer HTTP attempt with safe connection, decoded-result, HTTP-status, transport-error, and decode-error diagnostics; add tests that ensure the API key and credential-bearing URLs never appear.
- [x] 3.2 Instrument every Moralis metadata HTTP attempt with safe connection, decoded-result, HTTP-status, transport-error, and decode-error diagnostics; add tests that ensure the API key and `X-API-Key` value never appear.
- [x] 3.3 Instrument CoinGecko price HTTP attempts, including each retry, with connection, decoded-result, HTTP-status, transport-error, and decode-error diagnostics; make coin-list fetching emit only its sanitized resolved URL and never its response or outcome details; test both behaviors and the empty-ID no-request path.
- [x] 3.4 Add enabled-versus-disabled regression tests proving equivalent provider HTTP methods, URLs/queries, headers, bodies, CoinGecko retry decisions and attempt counts, returned values, and returned errors.
- [x] 3.5 Verify `PROVIDER_API_LOGGING` adds no LI.FI request or response diagnostics and does not change LI.FI requests, results, errors, or existing token-list operational logs.

## 4. Documentation and Verification

- [x] 4.1 Document the `PROVIDER_API_LOGGING=true` local opt-in, Cloud Run suppression through `K_SERVICE`, the CoinGecko coin-list URL-only exception, the LI.FI exclusion, and the presence of wallet/provider data in other local result logs.
- [x] 4.2 Run formatting, the full Go test suite, vet, documentation checks, and repository diff checks; resolve any regressions.

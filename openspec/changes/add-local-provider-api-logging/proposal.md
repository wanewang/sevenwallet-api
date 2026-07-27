## Why

Local development currently provides little visibility into outbound Alchemy, Moralis, and CoinGecko calls, which makes integration failures and unexpected upstream data difficult to diagnose. Developers need request lifecycle and result logging locally without sending verbose provider payloads or sensitive data to GCP logs.

## What Changes

- Add explicitly enabled, local-only diagnostic logging for outbound Alchemy, Moralis, and CoinGecko API calls.
- Keep provider diagnostics off by default; enable them only when `PROVIDER_API_LOGGING=true` and the service is not running on GCP Cloud Run.
- Log when each provider request begins and log its outcome, including a sanitized representation of successful response data and useful error/status context, except that CoinGecko coin-list fetching logs only its sanitized request URL and never its response.
- Disable this provider diagnostic logging regardless of opt-in when the Cloud Run `K_SERVICE` runtime marker is present.
- Redact API keys and other credentials from all provider log messages.
- Keep the instrumentation observational: provider requests, retry behavior, returned values, and returned errors remain unchanged whether logging is enabled or disabled.
- Exclude LI.FI from the new provider request and response diagnostics.
- Add automated coverage for explicit opt-in, default and GCP suppression, response logging, the CoinGecko coin-list exception, credential redaction, behavior preservation, and LI.FI exclusion.

## Capabilities

### New Capabilities

- `local-provider-api-logging`: Local-only, credential-safe request and response diagnostics for the Alchemy, Moralis, and CoinGecko integrations, with behavior-preservation guarantees and explicit exclusion of LI.FI.

### Modified Capabilities

None.

## Impact

- Affected code: runtime configuration and startup wiring, plus the HTTP clients in `internal/alchemy`, `internal/moralis`, and `internal/coingecko`.
- Public HTTP API behavior and response schemas remain unchanged.
- GCP deployments continue making the same provider calls but do not emit the new diagnostic logs.
- No new external dependencies or persistence changes are required.

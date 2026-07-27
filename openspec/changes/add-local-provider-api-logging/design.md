## Context

Alchemy, Moralis, and CoinGecko are implemented as independent HTTP clients. They currently return decoded data or errors but do not expose the request lifecycle, so developers cannot easily tell whether a provider was contacted or inspect the upstream result while running the service locally. The diagnostics must require explicit local opt-in. The deployed service runs on GCP Cloud Run, where `K_SERVICE` is supplied by the runtime, and must not receive the new verbose logs. Alchemy credentials are embedded in request URLs and Moralis credentials are sent in a header, so logging raw requests is unsafe. CoinGecko's full coin catalog is also too large and noisy to print, so that operation needs URL-only diagnostics.

## Goals / Non-Goals

**Goals:**

- Keep diagnostics disabled unless a developer explicitly opts in locally.
- Emit a diagnostic entry before every actual Alchemy, Moralis, and CoinGecko HTTP attempt in opted-in local runs.
- Emit the successfully decoded provider result, or sanitized failure context, after each attempt except CoinGecko coin-list fetching.
- Emit only the sanitized request URL for CoinGecko coin-list fetching and never log its response or outcome details.
- Suppress all new provider diagnostics in Cloud Run.
- Keep credentials out of diagnostic output by construction.
- Preserve provider request construction, retries, returned values, and returned errors exactly as they behave without diagnostics.
- Leave LI.FI outside the new provider diagnostic scope.
- Make enablement and emitted messages deterministic and testable.

**Non-Goals:**

- Changing provider requests, retries, caching, application responses, or error semantics.
- Adding remote log aggregation, log-level configuration, tracing, or metrics.
- Logging database, Redis, LI.FI, or inbound HTTP activity.
- Guaranteeing that wallet addresses or returned market data are private in a developer's local terminal; these values are the intended diagnostic content.

## Decisions

### Follow the existing nil-safe `logf` hook pattern

Add a `logf func(string, ...any)` field to each of the Alchemy, Moralis, and CoinGecko clients, matching the injection pattern already used by `marketdata.Service`, `marketdata.Refresher`, and `tokenlist.Refresher`. Each client will route diagnostics through a private nil-safe method so a missing hook is a no-op. This is required for existing tests that construct clients with struct literals, including `internal/moralis/client_test.go`.

Production constructors will preserve their existing call forms by accepting an optional variadic logging option. `cmd/server` will supply `log.Printf` only when diagnostics are enabled; otherwise the client retains a nil hook. This intentionally differs from the existing higher-level components that default to `log.Printf`, because provider response diagnostics require explicit opt-in. Small pure helpers for consistent serialization and secret sanitization can be shared, but no new logger object will be passed through constructors.

A required logger constructor parameter was considered, but it would force unrelated call-site and test rewrites. A shared injected logger object was also considered, but it would introduce a new dependency pattern where the repository already has a simple, testable function hook.

### Detect the deployed environment at startup

Configuration will enable provider diagnostics only when `PROVIDER_API_LOGGING` is explicitly set to `true` and `K_SERVICE` is empty. Any other opt-in value leaves logging disabled. The presence of `K_SERVICE` overrides the opt-in and disables diagnostics because Cloud Run defines this marker. `cmd/server` will apply the optional `log.Printf` hook to the Alchemy, Moralis, and CoinGecko clients only when the computed value is enabled.

Enabling diagnostics automatically whenever `K_SERVICE` is absent was considered, but that would print potentially large or private provider data in every non-Cloud-Run environment without consent. A manually managed general-purpose `ENVIRONMENT` variable was also rejected because its meaning would be ambiguous. The dedicated opt-in makes the intent explicit, while the independent `K_SERVICE` guard prevents accidental GCP logging. Compile-time build tags were rejected because the same binary should behave correctly based on runtime configuration.

### Log semantic operations, not raw HTTP requests

Connection entries will contain the provider, operation, HTTP method, and safe operation inputs such as network, chain, IDs, or wallet/contract address. They will not serialize headers or credential-bearing endpoint URLs. Each successful response other than the CoinGecko coin list will be logged from the decoded Go value, after the normal JSON decoder has accepted it. This gives local developers the useful API results they requested without duplicating body reads or changing response processing.

The CoinGecko `/coins/list` operation is a deliberate exception: it will emit only the sanitized, fully resolved request URL before the call. It will not emit the decoded catalog, status, or error details through the provider diagnostic logger. CoinGecko price requests retain normal connection and outcome entries, and each price retry receives its own entries because each retry is an actual provider call. Empty-input paths that do not call a provider will not claim a connection.

Logging raw request objects was considered, but Alchemy places its API key in URL paths and Moralis places its key in a header. Logging raw response bytes was also rejected because it would duplicate buffering and could diverge from the value used by the application.

### Sanitize failures before emission

Diagnostic errors will be sanitized before logging, including replacement of known API keys if an underlying transport error contains a URL. Other than the URL-only CoinGecko coin-list operation, non-success HTTP results will log the provider, operation, and status code; response bodies will continue to be discarded and will not be logged. Existing returned errors remain behaviorally unchanged.

### Keep instrumentation observational

Logging will wrap the existing request and decode points without rebuilding requests, adding provider attempts, changing CoinGecko retry decisions, or transforming returned values and errors. Formatting or emitting a diagnostic must not participate in control flow and must not replace a provider result or error. Tests will compare request method, URL/query, headers, and body plus retry counts and returned outcomes with diagnostics disabled and enabled.

LI.FI is intentionally not instrumented by this change. Its existing token-list refresher operational logs remain unchanged, but the `PROVIDER_API_LOGGING` opt-in will not add LI.FI request URLs, request data, or responses to logs.

## Risks / Trade-offs

- [Large CoinGecko catalog results can create very large local log entries] → Never log the coin-list response; log only that operation's sanitized request URL.
- [Provider results can contain wallet addresses and portfolio data] → Keep diagnostics disabled in Cloud Run and document that local logs contain developer-requested upstream data.
- [A non-Cloud-Run GCP runtime might not define `K_SERVICE`] → Scope automatic detection to the repository's current Cloud Run deployment; revisit detection if another GCP target is added.
- [Constructor changes can affect tests and call sites] → Use variadic options so existing constructor calls remain valid, and make nil `logf` hooks safe for direct struct literals.
- [Serialization for logging adds local CPU and memory cost] → Perform serialization only when diagnostics are explicitly enabled, never serialize the CoinGecko catalog, and leave default and GCP execution on a no-op path.

## Migration Plan

1. Add and test nil-safe provider `logf` hooks, shared pure formatting/redaction helpers as needed, and the `PROVIDER_API_LOGGING` opt-in with the `K_SERVICE` override.
2. Apply the optional hook to the three clients and add provider-specific request/result and behavior-preservation tests.
3. Update local configuration documentation.
4. Deploy normally; Cloud Run suppresses the new diagnostics automatically. Rollback consists of reverting the logger wiring, with no data migration or API compatibility work required.

## Open Questions

None for the current Cloud Run deployment target.

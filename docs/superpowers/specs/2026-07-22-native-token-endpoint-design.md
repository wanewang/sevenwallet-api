# Native Token Endpoint Design

## Summary

Add `GET /v1/native`, a parameterless endpoint that returns Ethereum's native
ETH token as one bare `wallet.Token` JSON object. The response is built from the
existing in-memory LI.FI token-list snapshot; the endpoint does not call LI.FI
directly and does not read a wallet balance.

The endpoint is ETH-specific, matching the application's current single-chain
scope.

## Goals

- Expose LI.FI metadata and USD price information for native ETH.
- Preserve the existing `wallet.Token` response shape.
- Reuse the token-list snapshot populated by the existing refresh process.
- Return a non-null nested `price` object whose timestamp represents when the
  LI.FI snapshot was fetched.
- Document and test the endpoint consistently with the existing API.

## Non-goals

- Looking up a wallet's native ETH balance.
- Calling LI.FI during an endpoint request.
- Adding multi-chain selection or query parameters.
- Changing token-list persistence, refresh scheduling, or configuration.
- Changing the existing address token or transaction endpoints.

## HTTP contract

### Request

```http
GET /v1/native
```

The endpoint accepts no path or query parameters.

### Success response

The response status is `200 OK`. The body is a bare `wallet.Token`, not an
envelope:

```json
{
  "tokenAddress": null,
  "symbol": "ETH",
  "name": "Ethereum",
  "decimals": 18,
  "rawBalance": "0",
  "balance": "0",
  "isNative": true,
  "price": {
    "currency": "usd",
    "value": "3200.50",
    "lastUpdatedAt": "2026-07-22T12:00:00Z"
  },
  "logoURI": "https://example.com/eth.png",
  "coinKey": "ETH",
  "priceUSD": "3200.50"
}
```

`logoURI` and `coinKey` retain their existing `omitempty` behavior and may be
absent if LI.FI does not provide them. `priceUSD` and the nested `price` object
are required for a successful response.

### Unavailable response

The endpoint returns `503 Service Unavailable` when the native-token response
cannot be built from a complete current snapshot:

```json
{"error":"native token data unavailable"}
```

The unavailable cases are:

- There is no active LI.FI snapshot.
- The snapshot has no native ETH entry.
- The native entry has an empty `priceUSD`.
- The snapshot fetch time is missing.

Unexpected errors continue to use the API's existing `500 Internal Server
Error` behavior.

## Architecture

The request follows the existing layering:

```text
HTTP handler
  -> wallet.Service.GetNativeToken
    -> Allowlist.LookupNative
      -> current tokenlist.Snapshot
```

The API router registers `GET /v1/native`. Its handler calls the wallet service,
maps the native-data-unavailable domain error to the agreed `503` response, and
writes the returned token directly as JSON.

The wallet service owns the conversion from the LI.FI representation into the
public `wallet.Token` domain model. The HTTP layer does not depend directly on
the token-list holder or LI.FI types.

The allowlist boundary gains a native lookup that returns the native
`lifi.ListToken`, the snapshot fetch time, and whether a usable snapshot entry
was found. This keeps LI.FI's native-token address convention inside the
token-list layer.

## Native token lookup

LI.FI represents native ETH with the zero address:

```text
0x0000000000000000000000000000000000000000
```

`tokenlist.Snapshot.LookupNative` resolves that entry from the snapshot's
case-insensitive address index and returns the snapshot's `FetchedAt` value with
it. `tokenlist.Holder.LookupNative` safely delegates to the current snapshot. A
nil current snapshot produces a miss rather than a panic.

The lookup does not mutate the snapshot and does not perform network or storage
I/O.

## Response mapping

`wallet.Service.GetNativeToken` constructs a new `wallet.Token` using this
mapping:

| `wallet.Token` field | Source/value |
|---|---|
| `TokenAddress` | `nil` |
| `Symbol` | LI.FI `symbol` |
| `Name` | LI.FI `name` |
| `Decimals` | LI.FI `decimals` |
| `RawBalance` | `"0"` |
| `Balance` | `"0"` |
| `IsNative` | `true` |
| `Price.Currency` | `"usd"` |
| `Price.Value` | LI.FI `priceUSD` |
| `Price.LastUpdatedAt` | Snapshot fetch time in UTC RFC 3339 format |
| `LogoURI` | LI.FI `logoURI`, when non-empty |
| `CoinKey` | LI.FI `coinKey`, when non-empty |
| `PriceUSD` | LI.FI `priceUSD` |

The service validates that the native entry has a non-empty `priceUSD` and that
the fetch time is non-zero before constructing the response. Failure produces a
dedicated native-data-unavailable domain error, allowing the API layer to map it
to `503` without classifying it as a database failure.

The snapshot fetch time describes when this application observed the LI.FI
data. LI.FI does not provide a price-specific update timestamp in the token-list
response.

## Component changes

### `internal/tokenlist`

- Define the LI.FI zero address in the token-list layer.
- Add native lookup behavior to `Snapshot` and `Holder`.
- Return the snapshot fetch time with the native list token.

### `internal/wallet`

- Extend `Allowlist` with the native lookup behavior.
- Add a domain error for unavailable native-token data.
- Add `Service.GetNativeToken(ctx)` to validate and map snapshot data into a new
  `wallet.Token`.

### `internal/api`

- Extend `WalletService` with `GetNativeToken`.
- Register `GET /v1/native`.
- Add a handler that returns the bare token and maps the native-data-unavailable
  error to `503` with `{"error":"native token data unavailable"}`.
- Add handler annotations so the generated OpenAPI specification includes
  `/native` under the application's existing `/v1` base path.

### Documentation

- Regenerate the committed OpenAPI artifacts with `make docs`.
- Add `GET /v1/native` to the README endpoint table.
- Clarify that native metadata and price come from the existing refreshed LI.FI
  snapshot.

No database schema, Redis payload, environment variable, startup, or refresh
loop changes are required.

## Testing

### Token-list tests

- A snapshot finds the zero-address native ETH entry and returns its fetch time.
- Address indexing remains case-insensitive.
- A snapshot without the native entry returns a miss.
- A holder without a current snapshot returns a miss without panicking.

### Wallet service tests

- A complete native LI.FI entry maps to every expected `wallet.Token` field.
- The response uses a nil token address, zero balances, and `IsNative: true`.
- `Price` is non-null, uses `usd`, copies `priceUSD`, and formats snapshot
  `FetchedAt` in UTC RFC 3339 form.
- Optional empty logo and coin-key values stay omitted.
- Missing snapshot/native entry, empty price, and zero fetch time each return the
  native-data-unavailable domain error.

### API tests

- `GET /v1/native` returns `200` and a bare token object.
- The native-data-unavailable error returns `503` and the agreed JSON error.
- Unexpected service errors retain the existing `500` response.
- Existing address routes remain registered and unchanged.

### Documentation verification

- `make docs-check` confirms that generated and committed API specifications
  are synchronized.
- `go test ./...` verifies all package behavior.

## Compatibility and operational impact

This is an additive endpoint. Existing responses, persistence formats, refresh
behavior, and configuration remain compatible. Endpoint availability follows
the existing LI.FI snapshot lifecycle: once the refresher has loaded a complete
ETH token list, requests are local in-memory reads; if usable native data is not
available, callers receive a retryable `503`.

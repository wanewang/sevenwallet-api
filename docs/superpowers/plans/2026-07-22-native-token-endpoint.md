# Native Token Endpoint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `GET /v1/native`, returning a bare one-element array containing native ETH metadata and USD price from the existing LI.FI snapshot.

**Architecture:** Add a native-token lookup to the immutable token-list snapshot and holder, expose it through the wallet allowlist boundary, and map the LI.FI entry into `[]wallet.Token` in the wallet service. The API handler returns that slice directly and maps incomplete snapshot data to a dedicated retryable `503` response; no request-time network or storage I/O is added.

**Tech Stack:** Go 1.25, standard `net/http`, standard `testing`/`httptest`, swaggo OpenAPI 2.0 generation, existing atomic LI.FI token-list snapshot.

## Global Constraints

- This is a public repository. Do not include `Codex-Session:` or any `https://Codex.ai/code/session_...` URL in commit messages or PR descriptions.
- The route is exactly `GET /v1/native` and accepts no path or query parameters.
- A success body is a bare, non-nil `[]wallet.Token`, not an envelope; the initial implementation returns exactly one ETH item.
- Read only from the current in-memory LI.FI snapshot. Do not call LI.FI, Redis, Postgres, Alchemy, or Moralis during the request.
- LI.FI native ETH is identified by `0x0000000000000000000000000000000000000000`, and that convention remains encapsulated in `internal/tokenlist`.
- The returned token has `tokenAddress: null`, `rawBalance: "0"`, `balance: "0"`, and `isNative: true`.
- Copy `symbol`, `name`, `decimals`, `logoURI`, `coinKey`, and `priceUSD` from LI.FI; empty `logoURI` and `coinKey` remain omitted through their existing JSON tags.
- The nested `price` is required: currency is `usd`, value is LI.FI `priceUSD`, and `lastUpdatedAt` is snapshot `FetchedAt` converted to UTC and formatted with `time.RFC3339`.
- Missing snapshot/native entry, empty `priceUSD`, or a zero fetch time returns `503` with `{"error":"native token data unavailable"}`.
- Do not change database schemas, Redis formats, environment variables, startup wiring, or the token-list refresh loop.
---

## File Structure

- `internal/tokenlist/snapshot.go` — owns the LI.FI native-address convention and provides native lookup through `Snapshot` and `Holder`.
- `internal/tokenlist/snapshot_test.go` — verifies native lookup, fetch-time propagation, missing-entry behavior, and nil-holder safety.
- `internal/wallet/types.go` — adds the native lookup to `Allowlist` and defines the native-data-unavailable sentinel error.
- `internal/wallet/service.go` — validates and maps native LI.FI data into a non-nil one-element `[]Token`.
- `internal/wallet/fakes_test.go` — extends `fakeAllowlist` with native token state for service tests.
- `internal/wallet/service_test.go` — verifies exact field mapping, optional metadata, UTC timestamp formatting, and unavailable cases.
- `internal/api/router.go` — adds `GetNativeTokens` to the API boundary and registers `GET /v1/native`.
- `internal/api/handlers.go` — implements the handler, OpenAPI annotations, and `503` error mapping.
- `internal/api/handlers_test.go` — verifies the bare array response and error responses.
- `internal/api/openapi_test.go` — verifies `/native` is documented with an array-of-`wallet.Token` success schema.
- `internal/apidocs/swagger.json` — regenerated embedded OpenAPI JSON.
- `internal/apidocs/swagger.yaml` — regenerated OpenAPI YAML.
- `docs/api/openapi.json` — regenerated static OpenAPI JSON.
- `README.md` — lists and explains the new endpoint.

---

### Task 1: Native lookup in the token-list snapshot

**Files:**
- Modify: `internal/tokenlist/snapshot.go:13-82`
- Test: `internal/tokenlist/snapshot_test.go:1-60`

**Interfaces:**
- Consumes: existing `Snapshot.LookupByAddress(addr string) (lifi.ListToken, bool)` and `Snapshot.FetchedAt() time.Time`.
- Produces:
  - `func (s *Snapshot) LookupNative() (lifi.ListToken, time.Time, bool)`
  - `func (h *Holder) LookupNative() (lifi.ListToken, time.Time, bool)`

- [ ] **Step 1: Write failing snapshot and holder tests**

Append these tests to `internal/tokenlist/snapshot_test.go`:

```go
func TestSnapshotLookupNative(t *testing.T) {
	fetchedAt := time.Date(2026, 7, 22, 12, 30, 0, 0, time.UTC)
	native := lifi.ListToken{
		Address:  "0x0000000000000000000000000000000000000000",
		Symbol:   "ETH",
		Name:     "Ethereum",
		Decimals: 18,
		PriceUSD: "3200.50",
	}
	s := NewSnapshot("ETH", append(sampleTokens(), native), fetchedAt)

	got, gotAt, ok := s.LookupNative()
	if !ok {
		t.Fatal("expected native ETH lookup hit")
	}
	if got != native {
		t.Errorf("token = %+v, want %+v", got, native)
	}
	if !gotAt.Equal(fetchedAt) {
		t.Errorf("fetchedAt = %v, want %v", gotAt, fetchedAt)
	}

	missing := NewSnapshot("ETH", sampleTokens(), fetchedAt)
	if _, _, ok := missing.LookupNative(); ok {
		t.Error("snapshot without zero-address token should miss")
	}
}

func TestHolderLookupNative(t *testing.T) {
	var h Holder
	if _, _, ok := h.LookupNative(); ok {
		t.Error("holder without a snapshot should miss, not panic")
	}

	fetchedAt := time.Date(2026, 7, 22, 12, 30, 0, 0, time.UTC)
	native := lifi.ListToken{
		Address:  "0x0000000000000000000000000000000000000000",
		Symbol:   "ETH",
		PriceUSD: "3200.50",
	}
	h.Set(NewSnapshot("ETH", []lifi.ListToken{native}, fetchedAt))

	got, gotAt, ok := h.LookupNative()
	if !ok || got.Symbol != "ETH" || !gotAt.Equal(fetchedAt) {
		t.Fatalf("holder native lookup = (%+v, %v, %v)", got, gotAt, ok)
	}
}
```

- [ ] **Step 2: Run the focused tests and verify they fail**

Run:

```bash
go test ./internal/tokenlist -run 'Test(Snapshot|Holder)LookupNative' -count=1
```

Expected: FAIL to compile because `Snapshot.LookupNative` and `Holder.LookupNative` are undefined.

- [ ] **Step 3: Implement native lookup in the token-list layer**

Add the constant near the top of `internal/tokenlist/snapshot.go`, after the imports:

```go
const nativeTokenAddress = "0x0000000000000000000000000000000000000000"
```

Add this method after `LookupByAddress`:

```go
// LookupNative returns the LI.FI native-token entry and snapshot fetch time.
func (s *Snapshot) LookupNative() (lifi.ListToken, time.Time, bool) {
	t, ok := s.LookupByAddress(nativeTokenAddress)
	return t, s.fetchedAt, ok
}
```

Add this method after `Holder.LookupByAddress`:

```go
// LookupNative delegates to the current snapshot.
func (h *Holder) LookupNative() (lifi.ListToken, time.Time, bool) {
	s := h.Current()
	if s == nil {
		return lifi.ListToken{}, time.Time{}, false
	}
	return s.LookupNative()
}
```

- [ ] **Step 4: Format and run the token-list tests**

Run:

```bash
gofmt -w internal/tokenlist/snapshot.go internal/tokenlist/snapshot_test.go
go test ./internal/tokenlist -count=1
```

Expected: PASS for all `internal/tokenlist` tests.

- [ ] **Step 5: Commit the native lookup**

```bash
git add internal/tokenlist/snapshot.go internal/tokenlist/snapshot_test.go
git commit -m "feat(tokenlist): add native token lookup"
```

---

### Task 2: Wallet-domain native token mapping

**Files:**
- Modify: `internal/wallet/types.go:12-70`
- Modify: `internal/wallet/service.go:30-57`
- Modify: `internal/wallet/fakes_test.go:66-85`
- Test: `internal/wallet/service_test.go:1-310`

**Interfaces:**
- Consumes: `Allowlist.LookupNative() (lifi.ListToken, time.Time, bool)` implemented by `tokenlist.Holder` in Task 1.
- Produces:
  - `var ErrNativeTokenUnavailable error`
  - `func (s *Service) GetNativeTokens(ctx context.Context) ([]Token, error)`

- [ ] **Step 1: Extend the wallet test fake for native lookup**

Replace the `fakeAllowlist` declaration in `internal/wallet/fakes_test.go` with:

```go
type fakeAllowlist struct {
	byAddr          map[string]lifi.ListToken
	symbols         map[string]bool
	native          lifi.ListToken
	nativeFetchedAt time.Time
	nativeOK        bool
}
```

Add this method after `LookupByAddress`:

```go
func (f *fakeAllowlist) LookupNative() (lifi.ListToken, time.Time, bool) {
	return f.native, f.nativeFetchedAt, f.nativeOK
}
```

- [ ] **Step 2: Write failing service mapping and unavailable-data tests**

Update the imports in `internal/wallet/service_test.go` to include `reflect`:

```go
import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"wallet-api/internal/alchemy"
	"wallet-api/internal/lifi"
)
```

Then append:

```go
func TestGetNativeTokensMapsLifiToken(t *testing.T) {
	fetchedAt := time.Date(2026, 7, 22, 20, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	allow := &fakeAllowlist{
		native: lifi.ListToken{
			Address:  "0x0000000000000000000000000000000000000000",
			Symbol:   "ETH",
			Name:     "Ethereum",
			Decimals: 18,
			CoinKey:  "ETH",
			LogoURI:  "https://logo/eth.png",
			PriceUSD: "3200.50",
		},
		nativeFetchedAt: fetchedAt,
		nativeOK:        true,
	}
	svc := NewService(&fakeAlchemy{}, &fakeTokenStore{}, &fakeTxCache{}, allow, denyValidator(), "eth-mainnet", time.Minute)

	got, err := svc.GetNativeTokens(context.Background())
	if err != nil {
		t.Fatalf("GetNativeTokens: %v", err)
	}
	want := []Token{{
		TokenAddress: nil,
		Symbol:       "ETH",
		Name:         "Ethereum",
		Decimals:     18,
		RawBalance:   "0",
		Balance:      "0",
		IsNative:     true,
		Price: &Price{
			Currency:      "usd",
			Value:         "3200.50",
			LastUpdatedAt: "2026-07-22T12:30:00Z",
		},
		LogoURI:  strptr("https://logo/eth.png"),
		CoinKey:  strptr("ETH"),
		PriceUSD: strptr("3200.50"),
	}}
	if got == nil || !reflect.DeepEqual(got, want) {
		t.Errorf("tokens = %+v, want %+v", got, want)
	}
}

func TestGetNativeTokensLeavesOptionalMetadataNil(t *testing.T) {
	allow := &fakeAllowlist{
		native:          lifi.ListToken{Symbol: "ETH", Name: "Ethereum", Decimals: 18, PriceUSD: "3200.50"},
		nativeFetchedAt: time.Date(2026, 7, 22, 12, 30, 0, 0, time.UTC),
		nativeOK:        true,
	}
	svc := NewService(&fakeAlchemy{}, &fakeTokenStore{}, &fakeTxCache{}, allow, denyValidator(), "eth-mainnet", time.Minute)

	got, err := svc.GetNativeTokens(context.Background())
	if err != nil {
		t.Fatalf("GetNativeTokens: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("token count = %d, want 1", len(got))
	}
	if got[0].LogoURI != nil || got[0].CoinKey != nil {
		t.Errorf("optional metadata should remain nil: %+v", got[0])
	}
}

func TestGetNativeTokensUnavailable(t *testing.T) {
	fetchedAt := time.Date(2026, 7, 22, 12, 30, 0, 0, time.UTC)
	tests := []struct {
		name  string
		allow *fakeAllowlist
	}{
		{name: "missing snapshot or native entry", allow: &fakeAllowlist{}},
		{name: "empty price", allow: &fakeAllowlist{
			native: lifi.ListToken{Symbol: "ETH"}, nativeFetchedAt: fetchedAt, nativeOK: true,
		}},
		{name: "zero fetch time", allow: &fakeAllowlist{
			native: lifi.ListToken{Symbol: "ETH", PriceUSD: "3200.50"}, nativeOK: true,
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(&fakeAlchemy{}, &fakeTokenStore{}, &fakeTxCache{}, tt.allow, denyValidator(), "eth-mainnet", time.Minute)
			got, err := svc.GetNativeTokens(context.Background())
			if got != nil {
				t.Errorf("tokens = %+v, want nil", got)
			}
			if !errors.Is(err, ErrNativeTokenUnavailable) {
				t.Errorf("error = %v, want ErrNativeTokenUnavailable", err)
			}
		})
	}
}
```

- [ ] **Step 3: Run the focused service tests and verify they fail**

Run:

```bash
go test ./internal/wallet -run TestGetNativeTokens -count=1
```

Expected: FAIL to compile because `Service.GetNativeTokens` and `ErrNativeTokenUnavailable` are undefined.

- [ ] **Step 4: Add the domain error and allowlist interface method**

Extend the sentinel block in `internal/wallet/types.go` to:

```go
var (
	ErrUpstream               = errors.New("upstream provider error")
	ErrStore                  = errors.New("storage error")
	ErrNativeTokenUnavailable = errors.New("native token data unavailable")
)
```

Extend `Allowlist` to:

```go
type Allowlist interface {
	LookupByAddress(addr string) (lifi.ListToken, bool)
	LookupNative() (lifi.ListToken, time.Time, bool)
	HasSymbol(sym string) bool
}
```

- [ ] **Step 5: Implement the wallet service mapping**

Add this method after `GetTokens` in `internal/wallet/service.go`:

```go
// GetNativeTokens returns native ETH metadata and price from the current LI.FI snapshot.
func (s *Service) GetNativeTokens(_ context.Context) ([]Token, error) {
	lt, fetchedAt, ok := s.allow.LookupNative()
	if !ok || strings.TrimSpace(lt.PriceUSD) == "" || fetchedAt.IsZero() {
		return nil, ErrNativeTokenUnavailable
	}

	t := Token{
		TokenAddress: nil,
		Symbol:       lt.Symbol,
		Name:         lt.Name,
		Decimals:     lt.Decimals,
		RawBalance:   "0",
		Balance:      "0",
		IsNative:     true,
		Price: &Price{
			Currency:      "usd",
			Value:         lt.PriceUSD,
			LastUpdatedAt: fetchedAt.UTC().Format(time.RFC3339),
		},
		PriceUSD: strptr(lt.PriceUSD),
	}
	if lt.LogoURI != "" {
		t.LogoURI = strptr(lt.LogoURI)
	}
	if lt.CoinKey != "" {
		t.CoinKey = strptr(lt.CoinKey)
	}
	return []Token{t}, nil
}
```

- [ ] **Step 6: Format and run the wallet tests**

Run:

```bash
gofmt -w internal/wallet/types.go internal/wallet/service.go internal/wallet/fakes_test.go internal/wallet/service_test.go
go test ./internal/wallet -count=1
```

Expected: PASS for all `internal/wallet` tests, including the three new native-token tests.

- [ ] **Step 7: Commit the wallet-domain behavior**

```bash
git add internal/wallet/types.go internal/wallet/service.go internal/wallet/fakes_test.go internal/wallet/service_test.go
git commit -m "feat(wallet): map native token from LI.FI"
```

---

### Task 3: HTTP endpoint, OpenAPI contract, and README

**Files:**
- Modify: `internal/api/router.go:10-24`
- Modify: `internal/api/handlers.go:32-123`
- Test: `internal/api/handlers_test.go:13-139`
- Test: `internal/api/openapi_test.go:10-29`
- Regenerate: `internal/apidocs/swagger.json`
- Regenerate: `internal/apidocs/swagger.yaml`
- Regenerate: `docs/api/openapi.json`
- Modify: `README.md:10-17,55`

**Interfaces:**
- Consumes: `Service.GetNativeTokens(context.Context) ([]wallet.Token, error)` and `wallet.ErrNativeTokenUnavailable` from Task 2.
- Produces: `GET /v1/native` with `200 []wallet.Token`, `503 ErrorResponse` for unavailable native data, and existing `500 ErrorResponse` behavior for unexpected errors.

- [ ] **Step 1: Extend the API stub and write failing endpoint tests**

Add `nativeTokens []wallet.Token` to `stubService` in `internal/api/handlers_test.go`:

```go
type stubService struct {
	portfolio   *wallet.TokenPortfolio
	page        *wallet.TransactionPage
	nativeTokens []wallet.Token
	err         error
	lastLimit   int
	lastPage    string
}
```

Add the required service method after `GetTokens`:

```go
func (s *stubService) GetNativeTokens(ctx context.Context) ([]wallet.Token, error) {
	return s.nativeTokens, s.err
}
```

Append these tests:

```go
func TestNativeEndpointOK(t *testing.T) {
	priceUSD := "3200.50"
	svc := &stubService{nativeTokens: []wallet.Token{{
		Symbol:     "ETH",
		Name:       "Ethereum",
		Decimals:   18,
		RawBalance: "0",
		Balance:    "0",
		IsNative:   true,
		Price: &wallet.Price{
			Currency:      "usd",
			Value:         "3200.50",
			LastUpdatedAt: "2026-07-22T12:30:00Z",
		},
		PriceUSD: &priceUSD,
	}}}

	rec := doGet(NewRouter(svc), "/v1/native")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var got []wallet.Token
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if got == nil || len(got) != 1 {
		t.Fatalf("tokens = %+v, want a non-nil one-element array", got)
	}
	if got[0].Symbol != "ETH" || !got[0].IsNative {
		t.Errorf("token = %+v, want native ETH", got[0])
	}
	if got[0].TokenAddress != nil || got[0].Price == nil || got[0].Price.Value != "3200.50" {
		t.Errorf("native token shape is wrong: %+v", got[0])
	}
}

func TestNativeEndpointUnavailable(t *testing.T) {
	svc := &stubService{err: wallet.ErrNativeTokenUnavailable}
	rec := doGet(NewRouter(svc), "/v1/native")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if got.Error != "native token data unavailable" {
		t.Errorf("error = %q, want %q", got.Error, "native token data unavailable")
	}
}

func TestNativeEndpointUnexpectedError(t *testing.T) {
	svc := &stubService{err: context.Canceled}
	rec := doGet(NewRouter(svc), "/v1/native")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body)
	}
}
```

- [ ] **Step 2: Run the endpoint tests and verify they fail**

Run:

```bash
go test ./internal/api -run TestNativeEndpoint -count=1
```

Expected: FAIL because `/v1/native` is not registered, so each request receives `404` instead of the asserted status.

- [ ] **Step 3: Register the service method and route**

Update `WalletService` in `internal/api/router.go`:

```go
type WalletService interface {
	GetTokens(ctx context.Context, address string) (*wallet.TokenPortfolio, error)
	GetNativeTokens(ctx context.Context) ([]wallet.Token, error)
	GetTransactions(ctx context.Context, address string, limit int, pageKey string) (*wallet.TransactionPage, error)
}
```

Register the new route before the address routes in `NewRouter`:

```go
mux.HandleFunc("GET /v1/native", h.getNativeTokens)
```

- [ ] **Step 4: Implement the handler and error mapping**

Add this handler before `getTokens` in `internal/api/handlers.go`:

```go
// getNativeTokens returns native ETH metadata and price from the current LI.FI snapshot.
//
// @Summary      Native tokens
// @Description  Native ETH metadata and USD price from the current LI.FI token-list snapshot.
// @Tags         tokens
// @Produce      json
// @Success      200  {array}   wallet.Token
// @Failure      500  {object}  api.ErrorResponse
// @Failure      503  {object}  api.ErrorResponse
// @Router       /native [get]
func (h *handlers) getNativeTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := h.svc.GetNativeTokens(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}
```

Add this case at the top of `writeServiceError`'s switch:

```go
case errors.Is(err, wallet.ErrNativeTokenUnavailable):
	writeError(w, http.StatusServiceUnavailable, "native token data unavailable")
```

- [ ] **Step 5: Format and run the endpoint tests**

Run:

```bash
gofmt -w internal/api/router.go internal/api/handlers.go internal/api/handlers_test.go
go test ./internal/api -run TestNativeEndpoint -count=1
```

Expected: PASS for all three native endpoint tests.

- [ ] **Step 6: Add a failing OpenAPI array-schema assertion**

Append this block inside `TestServeOpenAPISpec`, after the existing tokens-path assertion in `internal/api/openapi_test.go`:

```go
	nativePath, ok := paths["/native"].(map[string]any)
	if !ok {
		t.Fatalf("spec missing native path; got paths %v", paths)
	}
	get, ok := nativePath["get"].(map[string]any)
	if !ok {
		t.Fatalf("native path has no GET operation: %v", nativePath)
	}
	responses, ok := get["responses"].(map[string]any)
	if !ok {
		t.Fatalf("native GET has no responses: %v", get)
	}
	okResponse, ok := responses["200"].(map[string]any)
	if !ok {
		t.Fatalf("native GET has no 200 response: %v", responses)
	}
	schema, ok := okResponse["schema"].(map[string]any)
	if !ok || schema["type"] != "array" {
		t.Fatalf("native 200 schema = %v, want array", okResponse["schema"])
	}
	items, ok := schema["items"].(map[string]any)
	if !ok || items["$ref"] != "#/definitions/wallet-api_internal_wallet.Token" {
		t.Errorf("native array items = %v, want wallet.Token reference", schema["items"])
	}
```

- [ ] **Step 7: Run the OpenAPI test and verify the committed spec is stale**

Run:

```bash
go test ./internal/api -run TestServeOpenAPISpec -count=1
```

Expected: FAIL with `spec missing native path` because the embedded generated specification has not been refreshed yet.

- [ ] **Step 8: Regenerate OpenAPI artifacts**

Run:

```bash
make docs
```

Expected: `internal/apidocs/swagger.json`, `internal/apidocs/swagger.yaml`, and `docs/api/openapi.json` are regenerated; `/native` has a `200` schema with `type: array` and items referencing `wallet.Token`.

- [ ] **Step 9: Update the README endpoint documentation**

Replace the endpoint table in `README.md` with:

```markdown
| Method & path | Description |
|---|---|
| `GET /v1/native` | Native ETH metadata and USD price from the refreshed LI.FI snapshot |
| `GET /v1/addresses/{address}/tokens` | Token portfolio — native ETH + ERC-20, with metadata and prices |
| `GET /v1/addresses/{address}/transactions` | Transaction history (asset transfers), paginated via `limit` & `pageKey` |
```

After the address-validation sentence, add:

```markdown
`GET /v1/native` returns a bare token array. It currently contains one ETH item,
allowing more native-token entries to be added later without changing the
top-level response type. Metadata and USD price are read from the existing LI.FI
snapshot, so the endpoint makes no request-time provider call.
```

- [ ] **Step 10: Run API, documentation, and full regression checks**

Run:

```bash
gofmt -w internal/api/openapi_test.go
go test ./internal/api -count=1
go test ./...
go vet ./...
make docs-check
git diff --check
```

Expected: all API and repository tests PASS; `go vet` exits 0; `make docs-check` exits 0 with no generated-spec drift; `git diff --check` emits no output.

- [ ] **Step 11: Commit the endpoint and documentation**

```bash
git add internal/api/router.go internal/api/handlers.go internal/api/handlers_test.go internal/api/openapi_test.go internal/apidocs/swagger.json internal/apidocs/swagger.yaml docs/api/openapi.json README.md
git commit -m "feat(api): add native token endpoint"
```

- [ ] **Step 12: Verify the final repository state**

Run:

```bash
go test ./...
go vet ./...
make docs-check
git status --short
```

Expected: tests PASS, vet exits 0, docs are current, and `git status --short` emits no output.

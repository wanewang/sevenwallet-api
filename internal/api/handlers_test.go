package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"wallet-api/internal/wallet"
)

type stubService struct {
	portfolio    *wallet.TokenPortfolio
	markets      *wallet.TokenMarketPortfolio
	page         *wallet.TransactionPage
	nativeTokens []wallet.Token
	err          error
	lastLimit    int
	lastPage     string
}

func (s *stubService) GetTokens(ctx context.Context, address string) (*wallet.TokenPortfolio, error) {
	return s.portfolio, s.err
}
func (s *stubService) GetTokenMarkets(ctx context.Context, address string) (*wallet.TokenMarketPortfolio, error) {
	return s.markets, s.err
}
func (s *stubService) GetNativeTokens(ctx context.Context) ([]wallet.Token, error) {
	return s.nativeTokens, s.err
}
func (s *stubService) GetTransactions(ctx context.Context, address string, limit int, pageKey string) (*wallet.TransactionPage, error) {
	s.lastLimit, s.lastPage = limit, pageKey
	return s.page, s.err
}

func doGet(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

const validAddr = "0x1234567890abcdef1234567890abcdef12345678"

func TestTokensEndpointOK(t *testing.T) {
	svc := &stubService{portfolio: &wallet.TokenPortfolio{Address: validAddr, Network: "eth-mainnet"}}
	rec := doGet(NewRouter(svc), "/v1/addresses/"+validAddr+"/tokens")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var got wallet.TokenPortfolio
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if got.Address != validAddr {
		t.Errorf("address = %q", got.Address)
	}
}

func TestTokenResponseIncludesNullableMarketFields(t *testing.T) {
	svc := &stubService{portfolio: &wallet.TokenPortfolio{
		Address: validAddr, Network: "eth-mainnet",
		Tokens: []wallet.Token{{Symbol: "ETH", IsNative: true}},
	}}
	rec := doGet(NewRouter(svc), "/v1/addresses/"+validAddr+"/tokens")
	var body struct {
		Tokens []map[string]any `json:"tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"change24hPercent", "marketCapUSD", "marketDataUpdatedAt"} {
		value, ok := body.Tokens[0][key]
		if !ok || value != nil {
			t.Errorf("%s present=%v value=%v", key, ok, value)
		}
	}
}

func TestTokensEndpointRejectsBadAddress(t *testing.T) {
	svc := &stubService{}
	rec := doGet(NewRouter(svc), "/v1/addresses/not-an-address/tokens")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestTokenMarketsEndpointOK(t *testing.T) {
	price := "3210.45"
	svc := &stubService{markets: &wallet.TokenMarketPortfolio{
		Wallet: validAddr, Network: "eth-mainnet",
		Tokens: []wallet.TokenMarket{{
			Symbol: "ETH", Name: "Ethereum", Decimals: 18, Balance: "1",
			CG: &wallet.CoinGeckoMarket{ID: "ethereum", PriceUSD: &price},
		}},
	}}
	rec := doGet(NewRouter(svc), "/v1/tokens/"+validAddr)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var got wallet.TokenMarketPortfolio
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Wallet != validAddr || len(got.Tokens) != 1 || got.Tokens[0].CG == nil || got.Tokens[0].CMC != nil {
		t.Fatalf("response = %#v", got)
	}
	var raw struct {
		Tokens []map[string]any `json:"tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if value, ok := raw.Tokens[0]["cmc"]; !ok || value != nil {
		t.Fatalf("cmc present=%v value=%v", ok, value)
	}
	cg := raw.Tokens[0]["cg"].(map[string]any)
	if value, ok := cg["change24hPercent"]; !ok || value != nil {
		t.Fatalf("cg.change24hPercent present=%v value=%v", ok, value)
	}
}

func TestTokenMarketsEndpointRejectsInvalidWallet(t *testing.T) {
	rec := doGet(NewRouter(&stubService{}), "/v1/tokens/bad")
	if rec.Code != http.StatusBadRequest || rec.Body.String() != "{\"error\":\"invalid wallet\"}\n" {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
}

func TestTokenMarketsOldQueryRouteIsRemoved(t *testing.T) {
	for _, path := range []string{"/v1/tokens", "/v1/tokens?wallet=" + validAddr} {
		rec := doGet(NewRouter(&stubService{}), path)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status=%d, want 404", path, rec.Code)
		}
	}
}

func TestTokenMarketsEndpointMapsMissingCacheAndStoreErrors(t *testing.T) {
	cases := []struct {
		err  error
		code int
		body string
	}{
		{wallet.ErrWalletNotCached, http.StatusNotFound, "{\"error\":\"wallet token cache not found\"}\n"},
		{wallet.ErrStore, http.StatusServiceUnavailable, "{\"error\":\"storage unavailable\"}\n"},
	}
	for _, tc := range cases {
		rec := doGet(NewRouter(&stubService{err: tc.err}), "/v1/tokens/"+validAddr)
		if rec.Code != tc.code || rec.Body.String() != tc.body {
			t.Fatalf("err=%v: status=%d body=%s", tc.err, rec.Code, rec.Body)
		}
	}
}

func TestTransactionsParsesQueryParams(t *testing.T) {
	svc := &stubService{page: &wallet.TransactionPage{Address: validAddr}}
	rec := doGet(NewRouter(svc), "/v1/addresses/"+validAddr+"/transactions?limit=5&pageKey=abc")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if svc.lastLimit != 5 || svc.lastPage != "abc" {
		t.Errorf("params not parsed: limit=%d pageKey=%q", svc.lastLimit, svc.lastPage)
	}
}

func TestTransactionsDefaultLimit(t *testing.T) {
	svc := &stubService{page: &wallet.TransactionPage{Address: validAddr}}
	doGet(NewRouter(svc), "/v1/addresses/"+validAddr+"/transactions")
	if svc.lastLimit != 25 {
		t.Errorf("default limit = %d, want 25", svc.lastLimit)
	}
}

func TestTransactionsLimitBounds(t *testing.T) {
	cases := []struct {
		query string
		want  int
	}{
		{"?limit=0", 25},    // non-positive → default
		{"?limit=-1", 25},   // non-positive → default
		{"?limit=abc", 25},  // unparseable → default
		{"?limit=101", 100}, // over-max → clamped
		{"?limit=100", 100}, // at-max → passthrough
	}
	for _, c := range cases {
		svc := &stubService{page: &wallet.TransactionPage{Address: validAddr}}
		doGet(NewRouter(svc), "/v1/addresses/"+validAddr+"/transactions"+c.query)
		if svc.lastLimit != c.want {
			t.Errorf("%s: limit = %d, want %d", c.query, svc.lastLimit, c.want)
		}
	}
}

func TestUpstreamErrorMapsTo502(t *testing.T) {
	svc := &stubService{err: wallet.ErrUpstream}
	rec := doGet(NewRouter(svc), "/v1/addresses/"+validAddr+"/tokens")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}

func TestStoreErrorMapsTo503(t *testing.T) {
	svc := &stubService{err: wallet.ErrStore}
	rec := doGet(NewRouter(svc), "/v1/addresses/"+validAddr+"/tokens")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestErrorResponseShape(t *testing.T) {
	svc := &stubService{}
	rec := doGet(NewRouter(svc), "/v1/addresses/not-an-address/tokens")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if got.Error != "invalid address" {
		t.Errorf("error = %q, want %q", got.Error, "invalid address")
	}
}

func TestValidAddress(t *testing.T) {
	if !ValidAddress(validAddr) {
		t.Error("valid address rejected")
	}
	for _, bad := range []string{"", "0x123", "1234567890abcdef1234567890abcdef12345678", "0xZZZ4567890abcdef1234567890abcdef12345678"} {
		if ValidAddress(bad) {
			t.Errorf("invalid address accepted: %q", bad)
		}
	}
}

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

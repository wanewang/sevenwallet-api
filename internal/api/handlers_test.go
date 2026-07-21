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
	page         *wallet.TransactionPage
	nativeTokens []wallet.Token
	err          error
	lastLimit    int
	lastPage     string
}

func (s *stubService) GetTokens(ctx context.Context, address string) (*wallet.TokenPortfolio, error) {
	return s.portfolio, s.err
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

func TestTokensEndpointRejectsBadAddress(t *testing.T) {
	svc := &stubService{}
	rec := doGet(NewRouter(svc), "/v1/addresses/not-an-address/tokens")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
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

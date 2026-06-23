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
	portfolio *wallet.TokenPortfolio
	page      *wallet.TransactionPage
	err       error
	lastLimit int
	lastPage  string
}

func (s *stubService) GetTokens(ctx context.Context, address string) (*wallet.TokenPortfolio, error) {
	return s.portfolio, s.err
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

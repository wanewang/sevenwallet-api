package lifi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetTokensParsesEnvelope(t *testing.T) {
	const body = `{"tokens":{"1":[
		{"address":"0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48","symbol":"USDC","name":"USD Coin","decimals":6,"coinKey":"USDC","logoURI":"https://logo/usdc.png","priceUSD":"1.0001"},
		{"address":"0x0000000000000000000000000000000000000000","symbol":"ETH","name":"Ethereum","decimals":18,"coinKey":"ETH","logoURI":"https://logo/eth.png","priceUSD":"3200.50"}
	]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("chain"); got != "ETH" {
			t.Errorf("chain query = %q, want ETH", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := New(srv.URL)
	tokens, err := c.GetTokens(context.Background(), "ETH")
	if err != nil {
		t.Fatalf("GetTokens: %v", err)
	}
	if len(tokens) != 2 {
		t.Fatalf("got %d tokens, want 2", len(tokens))
	}
	if tokens[0].Symbol != "USDC" || tokens[0].Decimals != 6 || tokens[0].LogoURI != "https://logo/usdc.png" || tokens[0].PriceUSD != "1.0001" || tokens[0].CoinKey != "USDC" {
		t.Errorf("USDC mapping wrong: %+v", tokens[0])
	}
}

func TestGetTokensNon2xxErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	if _, err := New(srv.URL).GetTokens(context.Background(), "ETH"); err == nil {
		t.Fatal("expected error on non-2xx status")
	}
}

func TestGetTokensFlattensUnknownChain(t *testing.T) {
	const body = `{"tokens":{"137":[{"address":"0xaaa","symbol":"AAA","decimals":18}],"10":[{"address":"0xbbb","symbol":"BBB","decimals":18}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	tokens, err := New(srv.URL).GetTokens(context.Background(), "DAI")
	if err != nil {
		t.Fatalf("GetTokens: %v", err)
	}
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens flattened across chains, got %d", len(tokens))
	}
}

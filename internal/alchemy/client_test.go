package alchemy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetTokensParsesPortfolioResponse(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_, _ = io.WriteString(w, `{
		  "data": {
		    "tokens": [
		      {"address":"0xabc","network":"eth-mainnet","tokenAddress":null,
		       "tokenBalance":"1500000000000000000",
		       "tokenMetadata":{"decimals":18,"name":"Ethereum","symbol":"ETH"},
		       "tokenPrices":[{"currency":"usd","value":"3200.50","lastUpdatedAt":"2026-06-23T00:00:00Z"}],
		       "error":null},
		      {"address":"0xabc","network":"eth-mainnet","tokenAddress":"0xA0b8...",
		       "tokenBalance":"12500000",
		       "tokenMetadata":{"decimals":6,"name":"USD Coin","symbol":"USDC"},
		       "tokenPrices":[],"error":null}
		    ],
		    "pageKey": null
		  }
		}`)
	}))
	defer srv.Close()

	c := New("key123", "eth-mainnet")
	c.tokensURL = srv.URL

	tokens, err := c.GetTokens(context.Background(), "0xABC", "eth-mainnet")
	if err != nil {
		t.Fatalf("GetTokens error: %v", err)
	}
	// Request body assertions.
	if gotBody["withMetadata"] != true || gotBody["withPrices"] != true ||
		gotBody["includeNativeTokens"] != true || gotBody["includeErc20Tokens"] != true {
		t.Errorf("request flags wrong: %v", gotBody)
	}
	if len(tokens) != 2 {
		t.Fatalf("got %d tokens, want 2", len(tokens))
	}
	if tokens[0].TokenAddress != nil {
		t.Errorf("native token should have nil TokenAddress")
	}
	if tokens[0].Symbol != "ETH" || tokens[0].Decimals != 18 || tokens[0].RawBalance != "1500000000000000000" {
		t.Errorf("native token mis-parsed: %+v", tokens[0])
	}
	if tokens[0].Price == nil || tokens[0].Price.Value != "3200.50" {
		t.Errorf("native price mis-parsed: %+v", tokens[0].Price)
	}
	if tokens[1].TokenAddress == nil || tokens[1].Symbol != "USDC" {
		t.Errorf("erc20 mis-parsed: %+v", tokens[1])
	}
	if tokens[1].Price != nil {
		t.Errorf("empty tokenPrices should yield nil Price, got %+v", tokens[1].Price)
	}
}

func TestGetTokensSendsAddressAndNetwork(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"eth-mainnet"`) {
			t.Errorf("network not in body: %s", body)
		}
		_, _ = io.WriteString(w, `{"data":{"tokens":[],"pageKey":null}}`)
	}))
	defer srv.Close()
	c := New("key123", "eth-mainnet")
	c.tokensURL = srv.URL
	if _, err := c.GetTokens(context.Background(), "0xABC", "eth-mainnet"); err != nil {
		t.Fatal(err)
	}
}

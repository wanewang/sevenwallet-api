package wallet

import (
	"context"
	"errors"
	"testing"
	"time"

	"wallet-api/internal/alchemy"
)

func usdc(addr string) *string { return &addr }

func TestGetTokensCacheMissFetchesAndSaves(t *testing.T) {
	fa := &fakeAlchemy{tokens: []alchemy.Token{
		{TokenAddress: nil, Symbol: "ETH", Name: "Ethereum", Decimals: 18, RawBalance: "1500000000000000000",
			Price: &alchemy.Price{Currency: "usd", Value: "3200.50", LastUpdatedAt: "2026-06-23T00:00:00Z"}},
		{TokenAddress: usdc("0xA0B8"), Symbol: "USDC", Name: "USD Coin", Decimals: 6, RawBalance: "12500000"},
	}}
	ts := &fakeTokenStore{}
	svc := NewService(fa, ts, &fakeTxCache{}, "eth-mainnet", time.Minute)

	p, err := svc.GetTokens(context.Background(), "0xABC")
	if err != nil {
		t.Fatalf("GetTokens error: %v", err)
	}
	if fa.tokenCalls != 1 || ts.saveCalls != 1 {
		t.Errorf("expected 1 fetch + 1 save, got fetch=%d save=%d", fa.tokenCalls, ts.saveCalls)
	}
	if p.Address != "0xabc" || p.Network != "eth-mainnet" {
		t.Errorf("portfolio header wrong: %+v", p)
	}
	if len(p.Tokens) != 2 {
		t.Fatalf("got %d tokens, want 2", len(p.Tokens))
	}
	if !p.Tokens[0].IsNative || p.Tokens[0].Balance != "1.5" || p.Tokens[0].RawBalance != "1500000000000000000" {
		t.Errorf("native token normalization wrong: %+v", p.Tokens[0])
	}
	if p.Tokens[1].IsNative || p.Tokens[1].Balance != "12.5" {
		t.Errorf("erc20 normalization wrong: %+v", p.Tokens[1])
	}
}

func TestGetTokensCacheHitSkipsAlchemy(t *testing.T) {
	cached := &TokenPortfolio{Address: "0xabc", Network: "eth-mainnet", Tokens: []Token{{Symbol: "ETH"}}}
	fa := &fakeAlchemy{}
	ts := &fakeTokenStore{saved: cached, fresh: true}
	svc := NewService(fa, ts, &fakeTxCache{}, "eth-mainnet", time.Minute)

	p, err := svc.GetTokens(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if fa.tokenCalls != 0 {
		t.Errorf("cache hit should not call alchemy, got %d calls", fa.tokenCalls)
	}
	if p != cached {
		t.Errorf("expected cached portfolio returned")
	}
}

func TestGetTokensWrapsUpstreamError(t *testing.T) {
	fa := &fakeAlchemy{err: context.DeadlineExceeded}
	svc := NewService(fa, &fakeTokenStore{}, &fakeTxCache{}, "eth-mainnet", time.Minute)
	_, err := svc.GetTokens(context.Background(), "0xABC")
	if err == nil || !errorsIs(err, ErrUpstream) {
		t.Errorf("expected ErrUpstream, got %v", err)
	}
}

func errorsIs(err, target error) bool { return errors.Is(err, target) }

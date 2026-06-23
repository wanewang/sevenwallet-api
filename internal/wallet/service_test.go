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

func TestGetTokensWrapsSaveError(t *testing.T) {
	fa := &fakeAlchemy{tokens: []alchemy.Token{
		{TokenAddress: nil, Symbol: "ETH", Decimals: 18, RawBalance: "1500000000000000000"},
	}}
	ts := &fakeTokenStore{saveErr: errors.New("db down")}
	svc := NewService(fa, ts, &fakeTxCache{}, "eth-mainnet", time.Minute)

	_, err := svc.GetTokens(context.Background(), "0xABC")
	if err == nil || !errorsIs(err, ErrStore) {
		t.Errorf("expected ErrStore from SaveTokens failure, got %v", err)
	}
}

func errorsIs(err, target error) bool { return errors.Is(err, target) }

func TestGetTransactionsCacheMissFetchesAndSaves(t *testing.T) {
	fa := &fakeAlchemy{transfers: alchemy.TransfersResult{Transfers: []alchemy.Transfer{
		{Hash: "0x1", From: "0xabc", To: "0xdef", Asset: "ETH", Value: "0.5", BlockNum: "0x20", Category: "external"},
	}}}
	tc := &fakeTxCache{}
	svc := NewService(fa, &fakeTokenStore{}, tc, "eth-mainnet", time.Minute)

	page, err := svc.GetTransactions(context.Background(), "0xABC", 25, "")
	if err != nil {
		t.Fatal(err)
	}
	if fa.txCalls != 1 || tc.saveCalls != 1 {
		t.Errorf("expected 1 fetch + 1 save, got fetch=%d save=%d", fa.txCalls, tc.saveCalls)
	}
	if page.Address != "0xabc" || len(page.Transfers) != 1 || page.Transfers[0].Hash != "0x1" {
		t.Errorf("page wrong: %+v", page)
	}
}

func TestGetTransactionsCacheHit(t *testing.T) {
	cached := &TransactionPage{Address: "0xabc", Transfers: []Transfer{{Hash: "0xcached"}}}
	fa := &fakeAlchemy{}
	tc := &fakeTxCache{saved: cached, fresh: true}
	svc := NewService(fa, &fakeTokenStore{}, tc, "eth-mainnet", time.Minute)

	page, err := svc.GetTransactions(context.Background(), "0xABC", 25, "")
	if err != nil {
		t.Fatal(err)
	}
	if fa.txCalls != 0 || page != cached {
		t.Errorf("expected cache hit, got fetch=%d", fa.txCalls)
	}
}

func TestGetTransactionsPageKeyBypassesCache(t *testing.T) {
	fa := &fakeAlchemy{transfers: alchemy.TransfersResult{Transfers: []alchemy.Transfer{{Hash: "0x2"}}}}
	tc := &fakeTxCache{saved: &TransactionPage{Transfers: []Transfer{{Hash: "0xcached"}}}, fresh: true}
	svc := NewService(fa, &fakeTokenStore{}, tc, "eth-mainnet", time.Minute)

	page, err := svc.GetTransactions(context.Background(), "0xABC", 25, "PAGEKEY123")
	if err != nil {
		t.Fatal(err)
	}
	if fa.txCalls != 1 {
		t.Errorf("pageKey should force a fetch, got %d", fa.txCalls)
	}
	if tc.saveCalls != 0 {
		t.Errorf("pageKey fetches must not be cached, got %d saves", tc.saveCalls)
	}
	if len(page.Transfers) != 1 || page.Transfers[0].Hash != "0x2" {
		t.Errorf("expected fresh page, got %+v", page)
	}
}

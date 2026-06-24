package wallet

import (
	"context"
	"time"

	"wallet-api/internal/alchemy"
)

// fakeAlchemy returns canned data and records call counts.
type fakeAlchemy struct {
	tokens     []alchemy.Token
	transfers  alchemy.TransfersResult
	tokenCalls int
	txCalls    int
	err        error
}

func (f *fakeAlchemy) GetTokens(ctx context.Context, address, network string) ([]alchemy.Token, error) {
	f.tokenCalls++
	return f.tokens, f.err
}

func (f *fakeAlchemy) GetTransfers(ctx context.Context, address string, limit int, pageKey string) (alchemy.TransfersResult, error) {
	f.txCalls++
	return f.transfers, f.err
}

// fakeTokenStore is an in-memory TokenStore.
type fakeTokenStore struct {
	saved      *TokenPortfolio
	fresh      bool
	saveCalls  int
	getErr     error
	saveErr    error
}

func (s *fakeTokenStore) GetFreshTokens(ctx context.Context, address, network string, ttl time.Duration) (*TokenPortfolio, bool, error) {
	if s.getErr != nil {
		return nil, false, s.getErr
	}
	if s.fresh && s.saved != nil {
		return s.saved, true, nil
	}
	return nil, false, nil
}

func (s *fakeTokenStore) SaveTokens(ctx context.Context, p *TokenPortfolio) error {
	s.saveCalls++
	if s.saveErr != nil {
		return s.saveErr
	}
	s.saved = p
	return nil
}

// fakeTxCache is an in-memory TxCache.
type fakeTxCache struct {
	saved     *TransactionPage
	fresh     bool
	saveCalls int
}

func (c *fakeTxCache) GetFreshTransactions(ctx context.Context, address, params string, ttl time.Duration) (*TransactionPage, bool, error) {
	if c.fresh && c.saved != nil {
		return c.saved, true, nil
	}
	return nil, false, nil
}

func (c *fakeTxCache) SaveTransactions(ctx context.Context, address, params string, page *TransactionPage) error {
	c.saveCalls++
	c.saved = page
	return nil
}

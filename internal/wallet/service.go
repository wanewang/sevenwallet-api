package wallet

import (
	"context"
	"fmt"
	"time"

	"wallet-api/internal/alchemy"
)

// Service orchestrates cache-first reads over Alchemy + Postgres.
type Service struct {
	alchemy AlchemyClient
	tokens  TokenStore
	txs     TxCache
	network string
	ttl     time.Duration
	now     func() time.Time
}

// NewService builds a Service with a real-time clock.
func NewService(a AlchemyClient, ts TokenStore, tc TxCache, network string, ttl time.Duration) *Service {
	return &Service{alchemy: a, tokens: ts, txs: tc, network: network, ttl: ttl, now: time.Now}
}

// GetTokens returns the address's token portfolio, served from the DB snapshot
// when fresh and otherwise fetched from Alchemy and written through to the DB.
func (s *Service) GetTokens(ctx context.Context, address string) (*TokenPortfolio, error) {
	addr := NormalizeAddress(address)
	if p, ok, err := s.tokens.GetFreshTokens(ctx, addr, s.network, s.ttl); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStore, err)
	} else if ok {
		return p, nil
	}
	raw, err := s.alchemy.GetTokens(ctx, addr, s.network)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUpstream, err)
	}
	tokens, err := normalizeTokens(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUpstream, err)
	}
	p := &TokenPortfolio{
		Address:   addr,
		Network:   s.network,
		FetchedAt: s.now().UTC(),
		Tokens:    tokens,
	}
	if err := s.tokens.SaveTokens(ctx, p); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStore, err)
	}
	return p, nil
}

// normalizeTokens converts raw Alchemy tokens into domain tokens with scaled balances.
func normalizeTokens(raw []alchemy.Token) ([]Token, error) {
	out := make([]Token, 0, len(raw))
	for _, r := range raw {
		rawDec, scaled, err := ScaleBalance(r.RawBalance, r.Decimals)
		if err != nil {
			return nil, err
		}
		t := Token{
			TokenAddress: r.TokenAddress,
			Symbol:       r.Symbol,
			Name:         r.Name,
			Decimals:     r.Decimals,
			RawBalance:   rawDec,
			Balance:      scaled,
			IsNative:     r.TokenAddress == nil,
		}
		if r.Price != nil {
			t.Price = &Price{Currency: r.Price.Currency, Value: r.Price.Value, LastUpdatedAt: r.Price.LastUpdatedAt}
		}
		out = append(out, t)
	}
	return out, nil
}

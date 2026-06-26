package wallet

import (
	"context"
	"fmt"
	"strings"
	"time"

	"wallet-api/internal/alchemy"
	"wallet-api/internal/lifi"
)

// Service orchestrates cache-first reads over Alchemy + Postgres.
type Service struct {
	alchemy   AlchemyClient
	tokens    TokenStore
	txs       TxCache
	allow     Allowlist
	validator Validator
	network   string
	ttl       time.Duration
	now       func() time.Time
}

// NewService builds a Service with a real-time clock.
func NewService(a AlchemyClient, ts TokenStore, tc TxCache, allow Allowlist, validator Validator, network string, ttl time.Duration) *Service {
	return &Service{alchemy: a, tokens: ts, txs: tc, allow: allow, validator: validator, network: network, ttl: ttl, now: time.Now}
}

// GetTokens returns the address's token portfolio, served from the DB snapshot
// when fresh and otherwise fetched from Alchemy and written through to the DB.
func (s *Service) GetTokens(ctx context.Context, address string) (*TokenPortfolio, error) {
	addr := NormalizeAddress(address)
	if p, ok, err := s.tokens.GetFreshTokens(ctx, addr, s.network, s.ttl); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStore, err)
	} else if ok {
		return s.filterTokens(ctx, p), nil
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
	return s.filterTokens(ctx, p), nil
}

// GetTransactions returns a page of transfer history for address. The first page
// (no pageKey) is served cache-first and written through; pageKey requests bypass
// the cache.
func (s *Service) GetTransactions(ctx context.Context, address string, limit int, pageKey string) (*TransactionPage, error) {
	addr := NormalizeAddress(address)
	params := fmt.Sprintf("limit=%d", limit)

	if pageKey == "" {
		if p, ok, err := s.txs.GetFreshTransactions(ctx, addr, params, s.ttl); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrStore, err)
		} else if ok {
			return s.filterTransfers(p), nil
		}
	}

	res, err := s.alchemy.GetTransfers(ctx, addr, limit, pageKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUpstream, err)
	}
	page := &TransactionPage{
		Address:     addr,
		Transfers:   mapTransfers(res.Transfers),
		NextPageKey: res.PageKey,
	}
	if pageKey == "" {
		if err := s.txs.SaveTransactions(ctx, addr, params, page); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrStore, err)
		}
	}
	return s.filterTransfers(page), nil
}

func mapTransfers(in []alchemy.Transfer) []Transfer {
	out := make([]Transfer, 0, len(in))
	for _, t := range in {
		out = append(out, Transfer{
			Hash: t.Hash, From: t.From, To: t.To, Asset: t.Asset,
			Value: t.Value, BlockNum: t.BlockNum, Category: t.Category,
		})
	}
	return out
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

// filterTokens keeps native tokens, keeps + enriches LI.FI-listed ERC-20s, and
// for unlisted ERC-20s consults the Validator: valid tokens are kept + enriched
// from Moralis metadata, everything else (invalid or error) is dropped.
func (s *Service) filterTokens(ctx context.Context, p *TokenPortfolio) *TokenPortfolio {
	out := &TokenPortfolio{Address: p.Address, Network: p.Network, FetchedAt: p.FetchedAt}
	out.Tokens = make([]Token, 0, len(p.Tokens))
	for _, t := range p.Tokens {
		if t.IsNative || t.TokenAddress == nil {
			out.Tokens = append(out.Tokens, t)
			continue
		}
		if lt, ok := s.allow.LookupByAddress(*t.TokenAddress); ok {
			out.Tokens = append(out.Tokens, enrichToken(t, lt))
			continue
		}
		v, err := s.validator.Validate(ctx, *t.TokenAddress)
		if err != nil || !v.Valid {
			continue // fail-closed: invalid or unknown tokens are dropped
		}
		out.Tokens = append(out.Tokens, enrichFromValidation(t, v))
	}
	return out
}

// enrichFromValidation overlays Moralis-derived metadata onto t. When decimals
// change, Balance is re-derived from RawBalance so the scaled value stays correct.
func enrichFromValidation(t Token, v Validation) Token {
	if v.Symbol != "" {
		t.Symbol = v.Symbol
	}
	if v.Name != "" {
		t.Name = v.Name
	}
	if v.LogoURI != "" {
		t.LogoURI = strptr(v.LogoURI)
	}
	if v.Decimals > 0 && v.Decimals != t.Decimals {
		if _, scaled, err := ScaleBalance(t.RawBalance, v.Decimals); err == nil {
			t.Balance = scaled
		}
		t.Decimals = v.Decimals
	}
	return t
}

// enrichToken overlays LI.FI metadata onto t. When decimals change, Balance is
// re-derived from RawBalance so the scaled value stays correct.
func enrichToken(t Token, lt lifi.ListToken) Token {
	if lt.Symbol != "" {
		t.Symbol = lt.Symbol
	}
	if lt.Name != "" {
		t.Name = lt.Name
	}
	if lt.LogoURI != "" {
		t.LogoURI = strptr(lt.LogoURI)
	}
	if lt.CoinKey != "" {
		t.CoinKey = strptr(lt.CoinKey)
	}
	if lt.PriceUSD != "" {
		t.PriceUSD = strptr(lt.PriceUSD)
	}
	if lt.Decimals > 0 && lt.Decimals != t.Decimals {
		if _, scaled, err := ScaleBalance(t.RawBalance, lt.Decimals); err == nil {
			t.Balance = scaled
		}
		t.Decimals = lt.Decimals
	}
	return t
}

// filterTransfers keeps native ETH and transfers whose asset symbol is in the
// allowlist; everything else is dropped (best-effort symbol match).
func (s *Service) filterTransfers(page *TransactionPage) *TransactionPage {
	out := &TransactionPage{Address: page.Address, NextPageKey: page.NextPageKey}
	out.Transfers = make([]Transfer, 0, len(page.Transfers))
	for _, t := range page.Transfers {
		if strings.EqualFold(t.Asset, "ETH") || s.allow.HasSymbol(t.Asset) {
			out.Transfers = append(out.Transfers, t)
		}
	}
	return out
}

func strptr(s string) *string { return &s }

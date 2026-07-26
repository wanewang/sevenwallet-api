// Package marketcompare coordinates provider lookups for cache-only portfolios.
package marketcompare

import (
	"context"
	"time"

	"wallet-api/internal/wallet"
)

type CoinGecko interface {
	LookupFresh(context.Context, []wallet.Token) []*wallet.CoinGeckoMarket
}

type CoinMarketCap interface {
	LookupFresh(context.Context, []wallet.Token) []*wallet.CoinMarketCapMarket
}

// Service runs both independent providers under one response-time budget.
type Service struct {
	cg      CoinGecko
	cmc     CoinMarketCap
	timeout time.Duration
}

func New(cg CoinGecko, cmc CoinMarketCap, timeout time.Duration) *Service {
	return &Service{cg: cg, cmc: cmc, timeout: timeout}
}

var _ wallet.MarketComparator = (*Service)(nil)

func (s *Service) Compare(ctx context.Context, tokens []wallet.Token) []wallet.MarketPair {
	pairs := make([]wallet.MarketPair, len(tokens))
	if len(tokens) == 0 || s == nil {
		return pairs
	}
	if ctx == nil {
		ctx = context.Background()
	}
	lookupCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	cgCh := make(chan []*wallet.CoinGeckoMarket, 1)
	cmcCh := make(chan []*wallet.CoinMarketCapMarket, 1)
	if s.cg != nil {
		go func() { cgCh <- s.cg.LookupFresh(lookupCtx, tokens) }()
	} else {
		cgCh <- nil
	}
	if s.cmc != nil {
		go func() { cmcCh <- s.cmc.LookupFresh(lookupCtx, tokens) }()
	} else {
		cmcCh <- nil
	}

	var cg []*wallet.CoinGeckoMarket
	var cmc []*wallet.CoinMarketCapMarket
	cgPending, cmcPending := true, true
	for cgPending || cmcPending {
		select {
		case cg = <-cgCh:
			cgPending = false
		case cmc = <-cmcCh:
			cmcPending = false
		case <-lookupCtx.Done():
			if cgPending {
				select {
				case cg = <-cgCh:
					cgPending = false
				default:
				}
			}
			if cmcPending {
				select {
				case cmc = <-cmcCh:
					cmcPending = false
				default:
				}
			}
			return merge(pairs, cg, cmc)
		}
	}
	return merge(pairs, cg, cmc)
}

func merge(pairs []wallet.MarketPair, cg []*wallet.CoinGeckoMarket, cmc []*wallet.CoinMarketCapMarket) []wallet.MarketPair {
	for i := range pairs {
		if i < len(cg) {
			pairs[i].CG = cg[i]
		}
		if i < len(cmc) {
			pairs[i].CMC = cmc[i]
		}
	}
	return pairs
}

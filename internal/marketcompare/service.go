// Package marketcompare coordinates provider lookups for cache-only portfolios.
package marketcompare

import (
	"context"
	"sync"
	"time"

	"wallet-api/internal/ptr"
	"wallet-api/internal/wallet"
)

type CoinGecko interface {
	LookupFreshWithProgress(context.Context, []wallet.Token, func([]*wallet.CoinGeckoMarket)) []*wallet.CoinGeckoMarket
}

type CoinMarketCap interface {
	LookupFreshWithProgress(context.Context, []wallet.Token, func([]*wallet.CoinMarketCapMarket)) []*wallet.CoinMarketCapMarket
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

	cg := newProgressState(cloneCoinGeckoMarket)
	cmc := newProgressState(cloneCoinMarketCapMarket)
	cgDone := make(chan struct{})
	cmcDone := make(chan struct{})
	if s.cg != nil {
		go func() {
			defer close(cgDone)
			final := s.cg.LookupFreshWithProgress(lookupCtx, tokens, func(values []*wallet.CoinGeckoMarket) {
				if lookupCtx.Err() == nil {
					cg.publish(values)
				}
			})
			if lookupCtx.Err() == nil {
				cg.publish(final)
			}
		}()
	} else {
		close(cgDone)
	}
	if s.cmc != nil {
		go func() {
			defer close(cmcDone)
			final := s.cmc.LookupFreshWithProgress(lookupCtx, tokens, func(values []*wallet.CoinMarketCapMarket) {
				if lookupCtx.Err() == nil {
					cmc.publish(values)
				}
			})
			if lookupCtx.Err() == nil {
				cmc.publish(final)
			}
		}()
	} else {
		close(cmcDone)
	}

	for cgDone != nil || cmcDone != nil {
		select {
		case <-cgDone:
			cgDone = nil
		case <-cmcDone:
			cmcDone = nil
		case <-lookupCtx.Done():
			return merge(pairs, cg.snapshot(), cmc.snapshot())
		}
	}
	return merge(pairs, cg.snapshot(), cmc.snapshot())
}

// progressState owns immutable provider snapshots across the lookup goroutine
// and the response goroutine. Publishing and reading both clone deeply, so
// work that finishes after the deadline cannot mutate the emitted response.
type progressState[T any] struct {
	mu     sync.Mutex
	values []T
	clone  func(T) T
}

func newProgressState[T any](clone func(T) T) *progressState[T] {
	return &progressState[T]{clone: clone}
}

func (s *progressState[T]) publish(values []T) {
	cloned := cloneSlice(values, s.clone)
	s.mu.Lock()
	s.values = cloned
	s.mu.Unlock()
}

func (s *progressState[T]) snapshot() []T {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneSlice(s.values, s.clone)
}

func cloneSlice[T any](values []T, clone func(T) T) []T {
	if values == nil {
		return nil
	}
	out := make([]T, len(values))
	for i, value := range values {
		out[i] = clone(value)
	}
	return out
}

func cloneCoinGeckoMarket(value *wallet.CoinGeckoMarket) *wallet.CoinGeckoMarket {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.PriceUSD = ptr.Clone(value.PriceUSD)
	cloned.Change24HPercent = ptr.Clone(value.Change24HPercent)
	return &cloned
}

func cloneCoinMarketCapMarket(value *wallet.CoinMarketCapMarket) *wallet.CoinMarketCapMarket {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.PriceUSD = ptr.Clone(value.PriceUSD)
	cloned.Change24HPercent = ptr.Clone(value.Change24HPercent)
	return &cloned
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

package marketcompare

import (
	"context"
	"testing"
	"time"

	"wallet-api/internal/wallet"
)

type fakeCG struct {
	delay time.Duration
	out   []*wallet.CoinGeckoMarket
}

func (f fakeCG) LookupFresh(ctx context.Context, _ []wallet.Token) []*wallet.CoinGeckoMarket {
	select {
	case <-time.After(f.delay):
		return f.out
	case <-ctx.Done():
		return nil
	}
}

type fakeCMC struct {
	delay time.Duration
	out   []*wallet.CoinMarketCapMarket
}

func (f fakeCMC) LookupFresh(ctx context.Context, _ []wallet.Token) []*wallet.CoinMarketCapMarket {
	select {
	case <-time.After(f.delay):
		return f.out
	case <-ctx.Done():
		return nil
	}
}

func TestCompareRunsProvidersConcurrentlyAndMergesPartialFields(t *testing.T) {
	price := "1"
	change := 2.0
	s := New(
		fakeCG{delay: 20 * time.Millisecond, out: []*wallet.CoinGeckoMarket{{ID: "cg", PriceUSD: &price}}},
		fakeCMC{delay: 20 * time.Millisecond, out: []*wallet.CoinMarketCapMarket{{ID: 7, Change24HPercent: &change}}},
		30*time.Millisecond,
	)
	got := s.Compare(context.Background(), []wallet.Token{{Symbol: "T"}})
	if got[0].CG == nil || got[0].CMC == nil || got[0].CG.PriceUSD == nil || got[0].CMC.Change24HPercent == nil {
		t.Fatalf("pair = %#v", got[0])
	}
}

func TestCompareSharedDeadlinePreservesCompletedProvider(t *testing.T) {
	s := New(
		fakeCG{delay: time.Millisecond, out: []*wallet.CoinGeckoMarket{{ID: "cg"}}},
		fakeCMC{delay: time.Second, out: []*wallet.CoinMarketCapMarket{{ID: 7}}},
		20*time.Millisecond,
	)
	got := s.Compare(context.Background(), []wallet.Token{{Symbol: "T"}})
	if got[0].CG == nil || got[0].CMC != nil {
		t.Fatalf("pair = %#v", got[0])
	}
}

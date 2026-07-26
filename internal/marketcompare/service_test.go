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

func (f fakeCG) LookupFreshWithProgress(ctx context.Context, _ []wallet.Token, _ func([]*wallet.CoinGeckoMarket)) []*wallet.CoinGeckoMarket {
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

func (f fakeCMC) LookupFreshWithProgress(ctx context.Context, _ []wallet.Token, _ func([]*wallet.CoinMarketCapMarket)) []*wallet.CoinMarketCapMarket {
	select {
	case <-time.After(f.delay):
		return f.out
	case <-ctx.Done():
		return nil
	}
}

type persistingCG struct {
	started chan struct{}
	stopped chan struct{}
}

func (f *persistingCG) LookupFreshWithProgress(ctx context.Context, _ []wallet.Token, progress func([]*wallet.CoinGeckoMarket)) []*wallet.CoinGeckoMarket {
	price := "1"
	result := []*wallet.CoinGeckoMarket{{ID: "completed", PriceUSD: &price}}
	progress(result)
	close(f.started)

	// Stand in for a context-aware persistence call that is active when the
	// shared comparison deadline expires.
	<-ctx.Done()
	result[0].ID = "late"
	*result[0].PriceUSD = "late"
	close(f.stopped)
	return result
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

func TestCompareDeadlinePreservesPublishedBatchWhilePersistenceStops(t *testing.T) {
	provider := &persistingCG{started: make(chan struct{}), stopped: make(chan struct{})}
	s := New(provider, nil, 20*time.Millisecond)

	startedAt := time.Now()
	got := s.Compare(context.Background(), []wallet.Token{{Symbol: "T"}})
	if elapsed := time.Since(startedAt); elapsed > 200*time.Millisecond {
		t.Fatalf("Compare took %v, want bounded deadline response", elapsed)
	}
	select {
	case <-provider.started:
	default:
		t.Fatal("provider persistence never started")
	}
	if got[0].CG == nil || got[0].CG.ID != "completed" || got[0].CG.PriceUSD == nil || *got[0].CG.PriceUSD != "1" {
		t.Fatalf("pair = %#v, want completed pre-persistence snapshot", got[0])
	}

	select {
	case <-provider.stopped:
	case <-time.After(time.Second):
		t.Fatal("provider did not stop after deadline cancellation")
	}
	if got[0].CG.ID != "completed" || *got[0].CG.PriceUSD != "1" {
		t.Fatalf("late provider mutation changed response: %#v", got[0].CG)
	}
}

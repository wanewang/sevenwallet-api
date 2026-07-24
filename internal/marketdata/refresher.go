package marketdata

import (
	"context"
	"fmt"
	"log"
	"time"

	"wallet-api/internal/coingecko"
)

const bootstrapFallbackTimeout = 5 * time.Second

type CoinListClient interface {
	ListCoins(context.Context) ([]coingecko.Coin, error)
}

type CatalogStore interface {
	ReplaceCoinMappings(context.Context, []CoinMapping) error
	LoadCoinMappings(context.Context) ([]CoinMapping, bool, error)
}

type Refresher struct {
	client   CoinListClient
	store    CatalogStore
	holder   *Holder
	interval time.Duration
	now      func() time.Time
	logf     func(string, ...any)
}

func NewRefresher(client CoinListClient, store CatalogStore, holder *Holder, interval time.Duration) *Refresher {
	return &Refresher{client: client, store: store, holder: holder, interval: interval, now: time.Now, logf: log.Printf}
}

// Bootstrap installs a fresh catalog when possible and otherwise falls back to
// the last non-empty PostgreSQL snapshot without failing startup.
func (r *Refresher) Bootstrap(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	mappings, err := r.fetch(ctx)
	if err == nil {
		if err := r.store.ReplaceCoinMappings(ctx, mappings); err == nil {
			r.holder.Set(NewCatalog(mappings))
			r.logf("marketdata: bootstrapped from CoinGecko (%d mappings)", len(mappings))
			return
		} else {
			r.logf("marketdata: bootstrap persist failed: %v", err)
		}
	} else {
		r.logf("marketdata: bootstrap fetch/build failed: %v", err)
	}

	// The fetch may have exhausted the caller's startup deadline. Give the
	// durable fallback its own bounded attempt while retaining context values.
	fallbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bootstrapFallbackTimeout)
	defer cancel()
	if mappings, ok, err := r.store.LoadCoinMappings(fallbackCtx); err != nil {
		r.logf("marketdata: bootstrap postgres load failed: %v", err)
	} else if ok && len(mappings) > 0 {
		r.holder.Set(NewCatalog(mappings))
		r.logf("marketdata: bootstrapped from postgres (%d mappings)", len(mappings))
		return
	} else {
		r.logf("marketdata: no non-empty catalog source available")
	}
	r.logf("marketdata: bootstrap left catalog empty")
}

// Run refreshes the catalog on each tick until ctx is canceled.
func (r *Refresher) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.refresh(ctx)
		}
	}
}

func (r *Refresher) refresh(ctx context.Context) {
	mappings, err := r.fetch(ctx)
	if err != nil {
		r.logf("marketdata: refresh failed, keeping prior catalog: %v", err)
		return
	}
	if err := r.store.ReplaceCoinMappings(ctx, mappings); err != nil {
		r.logf("marketdata: refresh persist failed, keeping prior catalog: %v", err)
		return
	}
	r.holder.Set(NewCatalog(mappings))
	r.logf("marketdata: refreshed catalog (%d mappings)", len(mappings))
}

func (r *Refresher) fetch(ctx context.Context) ([]CoinMapping, error) {
	coins, err := r.client.ListCoins(ctx)
	if err != nil {
		return nil, err
	}
	mappings := BuildMappings(coins, r.now())
	if len(mappings) == 0 {
		return nil, fmt.Errorf("CoinGecko returned no usable catalog mappings")
	}
	return mappings, nil
}

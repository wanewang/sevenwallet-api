package cmcmarket

import (
	"context"
	"fmt"
	"log"
	"time"

	"wallet-api/internal/coinmarketcap"
)

const bootstrapFallbackTimeout = 5 * time.Second

type CoinListClient interface {
	ListCoinsPage(context.Context, int, int) ([]coinmarketcap.Coin, error)
}

type CatalogStore interface {
	ReplaceCoinMarketCapMappings(context.Context, []CoinMapping) error
	LoadCoinMarketCapMappings(context.Context) ([]CoinMapping, bool, error)
}

// Refresher keeps the complete supported-platform CMC catalog current.
type Refresher struct {
	client    CoinListClient
	store     CatalogStore
	holder    *Holder
	supported map[int64]struct{}
	interval  time.Duration
	pageLimit int
	now       func() time.Time
	logf      func(string, ...any)
}

func NewRefresher(client CoinListClient, store CatalogStore, holder *Holder, supported map[int64]struct{}, interval time.Duration) *Refresher {
	return &Refresher{client: client, store: store, holder: holder, supported: supported, interval: interval, pageLimit: coinmarketcap.MapPageLimit, now: time.Now, logf: log.Printf}
}

func (r *Refresher) Bootstrap(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	mappings, err := r.fetch(ctx)
	if err == nil {
		if err := r.store.ReplaceCoinMarketCapMappings(ctx, mappings); err == nil {
			r.holder.Set(NewCatalog(mappings))
			r.logf("cmcmarket: bootstrapped from CoinMarketCap (%d mappings)", len(mappings))
			return
		} else {
			r.logf("cmcmarket: bootstrap persist failed: %v", err)
		}
	} else {
		r.logf("cmcmarket: bootstrap fetch/build failed: %v", err)
	}

	fallbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bootstrapFallbackTimeout)
	defer cancel()
	if mappings, ok, err := r.store.LoadCoinMarketCapMappings(fallbackCtx); err != nil {
		r.logf("cmcmarket: bootstrap postgres load failed: %v", err)
	} else if ok && len(mappings) > 0 {
		r.holder.Set(NewCatalog(mappings))
		r.logf("cmcmarket: bootstrapped from postgres (%d mappings)", len(mappings))
		return
	}
	r.logf("cmcmarket: bootstrap left catalog empty")
}

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
		r.logf("cmcmarket: refresh failed, keeping prior catalog: %v", err)
		return
	}
	if err := r.store.ReplaceCoinMarketCapMappings(ctx, mappings); err != nil {
		r.logf("cmcmarket: refresh persist failed, keeping prior catalog: %v", err)
		return
	}
	r.holder.Set(NewCatalog(mappings))
	r.logf("cmcmarket: refreshed catalog (%d mappings)", len(mappings))
}

func (r *Refresher) fetch(ctx context.Context) ([]CoinMapping, error) {
	var coins []coinmarketcap.Coin
	for start := 1; ; start += r.pageLimit {
		page, err := r.client.ListCoinsPage(ctx, start, r.pageLimit)
		if err != nil {
			return nil, err
		}
		coins = append(coins, page...)
		if len(page) < r.pageLimit {
			break
		}
	}
	mappings := BuildMappings(coins, r.supported, r.now().UTC())
	if len(mappings) == 0 {
		return nil, fmt.Errorf("CoinMarketCap returned no usable supported-platform mappings")
	}
	return mappings, nil
}

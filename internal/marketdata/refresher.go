package marketdata

import (
	"context"
	"fmt"
	"log"
	"time"

	"wallet-api/internal/coingecko"
	"wallet-api/internal/marketcatalog"
)

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
	marketcatalog.Bootstrap[CoinMapping](ctx, r, "CoinGecko")
}

// Run refreshes the catalog on each tick until ctx is canceled.
func (r *Refresher) Run(ctx context.Context) {
	marketcatalog.Run[CoinMapping](ctx, r, r.interval)
}

// Fetch is the only genuinely CoinGecko-specific step: one call returns the
// whole coin list. Bootstrap, fallback, and tick behaviour come from
// marketcatalog.
func (r *Refresher) Fetch(ctx context.Context) ([]CoinMapping, error) {
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

func (r *Refresher) Replace(ctx context.Context, mappings []CoinMapping) error {
	return r.store.ReplaceCoinMappings(ctx, mappings)
}

func (r *Refresher) Load(ctx context.Context) ([]CoinMapping, bool, error) {
	return r.store.LoadCoinMappings(ctx)
}

func (r *Refresher) Install(mappings []CoinMapping) { r.holder.Set(NewCatalog(mappings)) }

func (r *Refresher) Logf(format string, args ...any) { r.logf("marketdata: "+format, args...) }

// refresh performs a single refresh cycle.
func (r *Refresher) refresh(ctx context.Context) { marketcatalog.Refresh[CoinMapping](ctx, r) }

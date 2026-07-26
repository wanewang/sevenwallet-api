package cmcmarket

import (
	"context"
	"fmt"
	"log"
	"time"

	"wallet-api/internal/coinmarketcap"
	"wallet-api/internal/marketcatalog"
)

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
	marketcatalog.Bootstrap[CoinMapping](ctx, r, "CoinMarketCap")
}

func (r *Refresher) Run(ctx context.Context) {
	marketcatalog.Run[CoinMapping](ctx, r, r.interval)
}

// Fetch is the only genuinely CMC-specific step: the map endpoint paginates,
// unlike CoinGecko's single call. Bootstrap, fallback, and tick behaviour come
// from marketcatalog.
func (r *Refresher) Fetch(ctx context.Context) ([]CoinMapping, error) {
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

func (r *Refresher) Replace(ctx context.Context, mappings []CoinMapping) error {
	return r.store.ReplaceCoinMarketCapMappings(ctx, mappings)
}

func (r *Refresher) Load(ctx context.Context) ([]CoinMapping, bool, error) {
	return r.store.LoadCoinMarketCapMappings(ctx)
}

func (r *Refresher) Install(mappings []CoinMapping) { r.holder.Set(NewCatalog(mappings)) }

func (r *Refresher) Logf(format string, args ...any) { r.logf("cmcmarket: "+format, args...) }

// refresh performs a single refresh cycle.
func (r *Refresher) refresh(ctx context.Context) { marketcatalog.Refresh[CoinMapping](ctx, r) }

// Package marketcatalog holds the catalog lifecycle every market provider
// shares: bootstrap from the provider, fall back to the last good durable
// snapshot, then refresh on a tick. Only the fetch step differs by provider.
package marketcatalog

import (
	"context"
	"time"
)

// BootstrapFallbackTimeout bounds the durable-fallback attempt after a failed
// provider fetch. Declared once; both catalog refreshers use it.
const BootstrapFallbackTimeout = 5 * time.Second

// Source is the provider-specific half of catalog refreshing: how to
// fetch a catalog, how to persist it, how to read the last good one back, and
// how to install it. Only Fetch genuinely differs between providers — CoinGecko
// takes one call, CoinMarketCap paginates.
type Source[M any] interface {
	Fetch(context.Context) ([]M, error)
	Replace(context.Context, []M) error
	Load(context.Context) ([]M, bool, error)
	Install([]M)
	Logf(string, ...any)
}

// Bootstrap installs a fresh catalog when possible and otherwise falls back to
// the last non-empty durable snapshot. It never fails startup.
func Bootstrap[M any](ctx context.Context, src Source[M], provider string) {
	if ctx == nil {
		ctx = context.Background()
	}
	if mappings, err := src.Fetch(ctx); err != nil {
		src.Logf("bootstrap fetch/build failed: %v", err)
	} else if err := src.Replace(ctx, mappings); err != nil {
		src.Logf("bootstrap persist failed: %v", err)
	} else {
		src.Install(mappings)
		src.Logf("bootstrapped from %s (%d mappings)", provider, len(mappings))
		return
	}

	// The fetch may have exhausted the caller's startup deadline. Give the
	// durable fallback its own bounded attempt while retaining context values.
	fallbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), BootstrapFallbackTimeout)
	defer cancel()
	if mappings, ok, err := src.Load(fallbackCtx); err != nil {
		src.Logf("bootstrap postgres load failed: %v", err)
	} else if ok && len(mappings) > 0 {
		src.Install(mappings)
		src.Logf("bootstrapped from postgres (%d mappings)", len(mappings))
		return
	} else {
		src.Logf("no non-empty catalog source available")
	}
	src.Logf("bootstrap left catalog empty")
}

// Run refreshes the catalog on each tick until ctx is canceled. A failed tick
// keeps the previously installed catalog rather than emptying it.
func Run[M any](ctx context.Context, src Source[M], interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			Refresh(ctx, src)
		}
	}
}

// Refresh performs one refresh cycle, keeping the prior catalog on any failure.
func Refresh[M any](ctx context.Context, src Source[M]) {
	mappings, err := src.Fetch(ctx)
	if err != nil {
		src.Logf("refresh failed, keeping prior catalog: %v", err)
		return
	}
	if err := src.Replace(ctx, mappings); err != nil {
		src.Logf("refresh persist failed, keeping prior catalog: %v", err)
		return
	}
	src.Install(mappings)
	src.Logf("refreshed catalog (%d mappings)", len(mappings))
}

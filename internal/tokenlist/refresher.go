package tokenlist

import (
	"context"
	"fmt"
	"log"
	"time"

	"wallet-api/internal/lifi"
)

// LifiClient fetches the token list for a chain.
type LifiClient interface {
	GetTokens(ctx context.Context, chain string) ([]lifi.ListToken, error)
}

// Store persists and loads a token list (satisfied by both Postgres and Redis).
type Store interface {
	SaveTokenList(ctx context.Context, chain string, tokens []lifi.ListToken, fetchedAt time.Time) error
	LoadTokenList(ctx context.Context, chain string) ([]lifi.ListToken, time.Time, bool, error)
}

// Refresher keeps the holder's snapshot current from LI.FI, persisting to Redis and Postgres.
type Refresher struct {
	client   LifiClient
	redis    Store
	pg       Store
	holder   *Holder
	chain    string
	interval time.Duration
	now      func() time.Time
	logf     func(string, ...any)
}

// NewRefresher builds a Refresher with a real clock and the standard logger.
func NewRefresher(client LifiClient, redis, pg Store, holder *Holder, chain string, interval time.Duration) *Refresher {
	return &Refresher{
		client: client, redis: redis, pg: pg, holder: holder,
		chain: chain, interval: interval, now: time.Now, logf: log.Printf,
	}
}

// Bootstrap loads an initial snapshot before serving: fetch LI.FI, else Redis,
// else Postgres. Returns an error only if no source has a list.
func (r *Refresher) Bootstrap(ctx context.Context) error {
	if tokens, err := r.client.GetTokens(ctx, r.chain); err != nil {
		r.logf("tokenlist: bootstrap fetch failed: %v", err)
	} else if len(tokens) == 0 {
		r.logf("tokenlist: bootstrap fetch returned 0 tokens, falling back")
	} else {
		now := r.now()
		r.persist(ctx, tokens, now)
		r.holder.Set(NewSnapshot(r.chain, tokens, now))
		return nil
	}
	if tokens, fetchedAt, ok, err := r.redis.LoadTokenList(ctx, r.chain); err != nil {
		r.logf("tokenlist: redis load failed during bootstrap: %v", err)
	} else if ok {
		r.logf("tokenlist: bootstrapped from redis (%d tokens)", len(tokens))
		r.holder.Set(NewSnapshot(r.chain, tokens, fetchedAt))
		return nil
	}
	if tokens, fetchedAt, ok, err := r.pg.LoadTokenList(ctx, r.chain); err != nil {
		r.logf("tokenlist: postgres load failed during bootstrap: %v", err)
	} else if ok {
		r.logf("tokenlist: bootstrapped from postgres (%d tokens)", len(tokens))
		r.holder.Set(NewSnapshot(r.chain, tokens, fetchedAt))
		return nil
	}
	return fmt.Errorf("token list unavailable from lifi, redis, and postgres")
}

// Run refreshes on each tick until ctx is canceled.
func (r *Refresher) Run(ctx context.Context) {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.refresh(ctx)
		}
	}
}

// refresh fetches once and swaps the snapshot; on fetch error or empty result it keeps the prior one.
func (r *Refresher) refresh(ctx context.Context) {
	tokens, err := r.client.GetTokens(ctx, r.chain)
	if err != nil {
		r.logf("tokenlist: refresh failed, keeping prior snapshot: %v", err)
		return
	}
	if len(tokens) == 0 {
		r.logf("tokenlist: refresh returned 0 tokens, keeping prior snapshot")
		return
	}
	now := r.now()
	r.persist(ctx, tokens, now)
	r.holder.Set(NewSnapshot(r.chain, tokens, now))
}

// persist writes to Postgres then Redis, logging (not failing) on either error.
func (r *Refresher) persist(ctx context.Context, tokens []lifi.ListToken, fetchedAt time.Time) {
	if err := r.pg.SaveTokenList(ctx, r.chain, tokens, fetchedAt); err != nil {
		r.logf("tokenlist: postgres persist failed: %v", err)
	}
	if err := r.redis.SaveTokenList(ctx, r.chain, tokens, fetchedAt); err != nil {
		r.logf("tokenlist: redis persist failed: %v", err)
	}
}

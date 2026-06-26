// Package tokenvalidity decides whether an unlisted ERC-20 is a legitimate
// token, using a three-tier cache: Redis (hot) -> Postgres (permanent) -> Moralis.
package tokenvalidity

import (
	"context"
	"time"

	"wallet-api/internal/moralis"
	"wallet-api/internal/wallet"
)

// Record is the cached verdict + metadata for one contract.
type Record struct {
	PossibleSpam bool
	Verified     bool
	Symbol       string
	Name         string
	Logo         string
	Decimals     int
	FetchedAt    time.Time
}

// MoralisClient fetches token metadata for a contract.
type MoralisClient interface {
	GetTokenMetadata(ctx context.Context, address string) (moralis.Metadata, error)
}

// Cache is the Redis hot tier (TTL-bounded). Implemented by rediscache.Cache.
type Cache interface {
	LoadTokenMeta(ctx context.Context, chain, address string) (Record, bool, error)
	SaveTokenMeta(ctx context.Context, chain, address string, r Record, ttl time.Duration) error
}

// Store is the permanent Postgres tier. Implemented by store.Postgres.
type Store interface {
	GetTokenMeta(ctx context.Context, chain, address string) (Record, bool, error)
	SaveTokenMeta(ctx context.Context, chain, address string, r Record) error
}

// Checker resolves contract validity through Redis -> Postgres -> Moralis.
type Checker struct {
	moralis  MoralisClient
	cache    Cache
	store    Store
	chain    string
	recheck  time.Duration
	redisTTL time.Duration
	now      func() time.Time
}

// NewChecker builds a Checker. recheck is the Postgres freshness window;
// redisTTL is the Redis hot-cache TTL.
func NewChecker(m MoralisClient, c Cache, s Store, chain string, recheck, redisTTL time.Duration) *Checker {
	return &Checker{moralis: m, cache: c, store: s, chain: chain, recheck: recheck, redisTTL: redisTTL, now: time.Now}
}

// Validate returns the validity + enrichment metadata for an unlisted ERC-20.
func (c *Checker) Validate(ctx context.Context, address string) (wallet.Validation, error) {
	// Tier 1: Redis hot cache.
	if r, ok, err := c.cache.LoadTokenMeta(ctx, c.chain, address); err == nil && ok {
		return validationFromRecord(r), nil
	}

	// Tier 2: Postgres permanent store.
	var stale *Record
	if r, ok, err := c.store.GetTokenMeta(ctx, c.chain, address); err == nil && ok {
		if c.now().Sub(r.FetchedAt) < c.recheck {
			_ = c.cache.SaveTokenMeta(ctx, c.chain, address, r, c.redisTTL)
			return validationFromRecord(r), nil
		}
		rec := r
		stale = &rec
	}

	// Tier 3: Moralis.
	m, err := c.moralis.GetTokenMetadata(ctx, address)
	if err != nil {
		if stale != nil {
			_ = c.cache.SaveTokenMeta(ctx, c.chain, address, *stale, c.redisTTL)
			return validationFromRecord(*stale), nil
		}
		return wallet.Validation{Valid: false}, err
	}
	r := Record{
		PossibleSpam: m.PossibleSpam,
		Verified:     m.VerifiedContract,
		Symbol:       m.Symbol,
		Name:         m.Name,
		Logo:         m.Logo,
		Decimals:     m.Decimals,
		FetchedAt:    c.now().UTC(),
	}
	_ = c.store.SaveTokenMeta(ctx, c.chain, address, r)
	_ = c.cache.SaveTokenMeta(ctx, c.chain, address, r, c.redisTTL)
	return validationFromRecord(r), nil
}

// validationFromRecord applies the validity rule and maps metadata for enrichment.
func validationFromRecord(r Record) wallet.Validation {
	return wallet.Validation{
		Valid:    !r.PossibleSpam && r.Verified,
		Symbol:   r.Symbol,
		Name:     r.Name,
		LogoURI:  r.Logo,
		Decimals: r.Decimals,
	}
}

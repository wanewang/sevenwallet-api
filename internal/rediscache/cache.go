// Package rediscache stores the LI.FI token list in Redis as one JSON blob per chain.
package rediscache

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"wallet-api/internal/lifi"
	"wallet-api/internal/tokenvalidity"
)

// Cache is a Redis-backed token-list store.
type Cache struct {
	client *redis.Client
	ttl    time.Duration
}

// New parses a redis URL (redis://host:port/db) and builds a Cache.
func New(redisURL string, ttl time.Duration) (*Cache, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	return &Cache{client: redis.NewClient(opt), ttl: ttl}, nil
}

// Ping verifies connectivity.
func (c *Cache) Ping(ctx context.Context) error { return c.client.Ping(ctx).Err() }

// Close releases the client.
func (c *Cache) Close() error { return c.client.Close() }

func key(chain string) string { return "lifi:tokens:" + chain }

type payload struct {
	FetchedAt time.Time        `json:"fetchedAt"`
	Tokens    []lifi.ListToken `json:"tokens"`
}

// SaveTokenList writes the list with the configured safety TTL.
func (c *Cache) SaveTokenList(ctx context.Context, chain string, tokens []lifi.ListToken, fetchedAt time.Time) error {
	b, err := json.Marshal(payload{FetchedAt: fetchedAt, Tokens: tokens})
	if err != nil {
		return err
	}
	return c.client.Set(ctx, key(chain), b, c.ttl).Err()
}

// LoadTokenList returns the cached list, if present.
func (c *Cache) LoadTokenList(ctx context.Context, chain string) ([]lifi.ListToken, time.Time, bool, error) {
	b, err := c.client.Get(ctx, key(chain)).Bytes()
	if err == redis.Nil {
		return nil, time.Time{}, false, nil
	}
	if err != nil {
		return nil, time.Time{}, false, err
	}
	var p payload
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, time.Time{}, false, err
	}
	return p.Tokens, p.FetchedAt, true, nil
}

func metaKey(chain, address string) string {
	return "tokenmeta:" + chain + ":" + strings.ToLower(address)
}

// SaveTokenMeta caches a verdict/metadata record with the given TTL.
func (c *Cache) SaveTokenMeta(ctx context.Context, chain, address string, r tokenvalidity.Record, ttl time.Duration) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return c.client.Set(ctx, metaKey(chain, address), b, ttl).Err()
}

// LoadTokenMeta returns the cached verdict/metadata record, if present.
func (c *Cache) LoadTokenMeta(ctx context.Context, chain, address string) (tokenvalidity.Record, bool, error) {
	b, err := c.client.Get(ctx, metaKey(chain, address)).Bytes()
	if err == redis.Nil {
		return tokenvalidity.Record{}, false, nil
	}
	if err != nil {
		return tokenvalidity.Record{}, false, err
	}
	var r tokenvalidity.Record
	if err := json.Unmarshal(b, &r); err != nil {
		return tokenvalidity.Record{}, false, err
	}
	return r, true, nil
}

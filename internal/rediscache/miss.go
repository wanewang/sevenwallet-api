package rediscache

import (
	"context"
	"time"

	"wallet-api/internal/marketkey"
)

// The negative cache lives in its own namespace, separate from the market
// records. A miss records only that the provider had nothing for a token at a
// point in time — it carries no market payload, is never written to PostgreSQL,
// and expires on its own clock rather than the market TTL. Redis key expiry is
// the whole liveness mechanism: if the key is there, the miss is live.

func coinGeckoMissKey(k marketkey.Key) string { return "coingecko:miss:" + k.Chain + ":" + k.TokenKey }

func cmcMissKey(k marketkey.Key) string { return "cmc:miss:" + k.Chain + ":" + k.TokenKey }

// LoadCoinGeckoMisses returns the subset of keys with a live CoinGecko miss.
func (c *Cache) LoadCoinGeckoMisses(ctx context.Context, keys []marketkey.Key) (map[marketkey.Key]struct{}, error) {
	return c.loadMisses(ctx, keys, coinGeckoMissKey)
}

// SaveCoinGeckoMisses records a CoinGecko miss for each key under ttl.
func (c *Cache) SaveCoinGeckoMisses(ctx context.Context, keys []marketkey.Key, ttl time.Duration) error {
	return c.saveMisses(ctx, keys, ttl, coinGeckoMissKey)
}

// LoadCoinMarketCapMisses returns the subset of keys with a live CMC miss.
func (c *Cache) LoadCoinMarketCapMisses(ctx context.Context, keys []marketkey.Key) (map[marketkey.Key]struct{}, error) {
	return c.loadMisses(ctx, keys, cmcMissKey)
}

// SaveCoinMarketCapMisses records a CMC miss for each key under ttl.
func (c *Cache) SaveCoinMarketCapMisses(ctx context.Context, keys []marketkey.Key, ttl time.Duration) error {
	return c.saveMisses(ctx, keys, ttl, cmcMissKey)
}

func (c *Cache) loadMisses(ctx context.Context, keys []marketkey.Key, redisKey func(marketkey.Key) string) (map[marketkey.Key]struct{}, error) {
	result := make(map[marketkey.Key]struct{}, len(keys))
	if len(keys) == 0 {
		return result, nil
	}
	redisKeys := make([]string, len(keys))
	for i, key := range keys {
		redisKeys[i] = redisKey(key)
	}
	values, err := c.client.MGet(ctx, redisKeys...).Result()
	if err != nil {
		return nil, err
	}
	for i, value := range values {
		// Presence is the entire signal; the stored timestamp is for humans
		// reading the key, not for the freshness decision.
		if value == nil {
			continue
		}
		result[keys[i]] = struct{}{}
	}
	return result, nil
}

func (c *Cache) saveMisses(ctx context.Context, keys []marketkey.Key, ttl time.Duration, redisKey func(marketkey.Key) string) error {
	if len(keys) == 0 || ttl <= 0 {
		return nil
	}
	pipe := c.client.Pipeline()
	stamp := time.Now().UTC().Format(time.RFC3339)
	for _, key := range keys {
		pipe.Set(ctx, redisKey(key), stamp, ttl)
	}
	_, err := pipe.Exec(ctx)
	return err
}

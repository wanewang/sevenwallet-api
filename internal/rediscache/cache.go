// Package rediscache stores LI.FI, Moralis, and CoinGecko cache records in Redis.
package rediscache

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"wallet-api/internal/lifi"
	"wallet-api/internal/marketdata"
	"wallet-api/internal/tokenvalidity"
)

// Cache is a Redis-backed store for LI.FI, Moralis, and CoinGecko records.
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

type marketPayload struct {
	marketdata.Record
	FetchedAtUnixNano int64 `json:"fetchedAtUnixNano"`
}

const marketCASLua = `
local current = redis.call('GET', KEYS[1])
if current then
  local ok, decoded = pcall(cjson.decode, current)
  if ok and decoded['fetchedAtUnixNano'] ~= nil then
    if tonumber(ARGV[2]) < tonumber(decoded['fetchedAtUnixNano']) then
      return 0
    end
  end
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[3])
return 1
`

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

func marketKey(k marketdata.Key) string {
	return "coingecko:market:" + k.Chain + ":" + k.TokenKey
}

// LoadMarketData loads requested CoinGecko records with one Redis MGET.
// Malformed individual entries are logged and treated as misses.
func (c *Cache) LoadMarketData(ctx context.Context, keys []marketdata.Key) (map[marketdata.Key]marketdata.Record, error) {
	result := make(map[marketdata.Key]marketdata.Record, len(keys))
	if len(keys) == 0 {
		return result, nil
	}

	redisKeys := make([]string, len(keys))
	for i, key := range keys {
		redisKeys[i] = marketKey(key)
	}
	values, err := c.client.MGet(ctx, redisKeys...).Result()
	if err != nil {
		return nil, err
	}
	for i, value := range values {
		if value == nil {
			continue
		}
		var raw []byte
		switch v := value.(type) {
		case string:
			raw = []byte(v)
		case []byte:
			raw = v
		default:
			log.Printf("rediscache: malformed CoinGecko market data key %q: unexpected Redis value %T", redisKeys[i], value)
			continue
		}

		var payload marketPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			log.Printf("rediscache: malformed CoinGecko market data key %q: %v", redisKeys[i], err)
			continue
		}
		payload.Record.Key = keys[i]
		result[keys[i]] = payload.Record
	}
	return result, nil
}

// SaveMarketData writes positive-TTL CoinGecko records in one pipeline. Each
// Lua operation atomically rejects a fetch older than the stored record.
func (c *Cache) SaveMarketData(ctx context.Context, writes []marketdata.CacheWrite) error {
	pipe := c.client.Pipeline()
	queued := 0
	for _, write := range writes {
		if write.TTL <= 0 {
			continue
		}
		payload, err := json.Marshal(marketPayload{
			Record:            write.Record,
			FetchedAtUnixNano: write.Record.FetchedAt.UnixNano(),
		})
		if err != nil {
			return err
		}
		ttlMilliseconds := write.TTL.Milliseconds()
		if ttlMilliseconds < 1 {
			ttlMilliseconds = 1
		}
		pipe.Eval(ctx, marketCASLua, []string{marketKey(write.Record.Key)},
			string(payload), strconv.FormatInt(write.Record.FetchedAt.UnixNano(), 10), strconv.FormatInt(ttlMilliseconds, 10))
		queued++
	}
	if queued == 0 {
		return nil
	}
	_, err := pipe.Exec(ctx)
	return err
}

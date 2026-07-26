// Package marketpipeline holds the one lookup cascade every market provider
// runs: Redis, then PostgreSQL for whatever Redis missed, then the provider API
// for whatever both missed. Providers supply only what genuinely differs —
// their identifier type, record type, persistence calls, batch limit, and what
// counts as a usable payload.
package marketpipeline

import (
	"cmp"
	"context"
	"slices"
	"time"

	"wallet-api/internal/marketkey"
)

// Write pairs a record with the Redis TTL chosen for it.
type Write[R any] struct {
	Record R
	TTL    time.Duration
}

// Provider is everything the cascade needs that varies between providers.
//
// Apply reports whether the record was usable. The cascade marks a key complete
// and persists the record only when Apply returns true, which is what keeps a
// payload carrying no data from occupying a result slot or being written back.
type Provider[ID cmp.Ordered, R any] interface {
	LoadCache(context.Context, []marketkey.Key) (map[marketkey.Key]R, error)
	SaveCache(context.Context, []Write[R]) error
	LoadStore(context.Context, []marketkey.Key) (map[marketkey.Key]R, error)
	SaveStore(context.Context, []R) error

	// Fetch retrieves one batch, no larger than BatchLimit.
	Fetch(context.Context, []ID) (map[ID]R, error)
	BatchLimit() int

	// Matches reports whether a stored record belongs to the expected ID.
	Matches(record R, expected ID) bool
	FetchedAt(record R) time.Time
	// WithKey stamps the key back onto a record loaded from storage.
	WithKey(record R, key marketkey.Key) R

	// Apply writes the record into the caller's output at every index, and
	// reports whether it held anything usable *to this caller*. Two callers can
	// disagree about the same record — a market-cap-only record is usable for
	// enrichment and useless for a price comparison — so this drives the result
	// and persistence, never the negative cache.
	Apply(indexes []int, record R) bool

	// HasData reports whether the provider returned anything at all for this
	// record, independent of which caller asked. This is what drives the
	// negative cache: a record no caller could ever use means the provider has
	// nothing for that token, which is a fact worth remembering.
	HasData(record R) bool

	// LoadMisses returns the subset of keys with a live negative-cache entry.
	LoadMisses(context.Context, []marketkey.Key) (map[marketkey.Key]struct{}, error)
	SaveMisses(context.Context, []marketkey.Key, time.Duration) error
	MissTTL() time.Duration

	TTL() time.Duration
	// Now supplies the clock, so tests can pin freshness boundaries.
	Now() time.Time
	Logf(string, ...any)
}

// Request is one resolved lookup: which keys, in what order, which provider ID
// each maps to, and which output indexes each key feeds.
type Request[ID cmp.Ordered] struct {
	KeyOrder     []marketkey.Key
	ExpectedID   map[marketkey.Key]ID
	IndexesByKey map[marketkey.Key][]int
}

// Options carries behaviour the cascade only sometimes needs.
type Options[R any] struct {
	// Stale, when non-nil, receives every expired record the cascade saw for a
	// key that no tier completed. Enrichment uses it to fall back to old data;
	// fresh-only lookups leave it nil and expired records are simply dropped.
	Stale func(key marketkey.Key, record R)
}

// Run executes the cascade and reports which keys a tier completed. It never
// returns an error: every failure is logged and degrades the result rather than
// discarding what other tiers produced. Callers with a stale fallback use the
// returned set to decide which keys still need one.
func Run[ID cmp.Ordered, R any](ctx context.Context, p Provider[ID, R], req Request[ID], opts Options[R]) map[marketkey.Key]bool {
	complete := make(map[marketkey.Key]bool, len(req.KeyOrder))
	if len(req.KeyOrder) == 0 {
		return complete
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ttl := p.TTL()
	now := p.Now().UTC()

	// Tier 1: Redis.
	cached := load(ctx, req.KeyOrder, p.LoadCache, p.Logf, "redis load failed")
	for _, key := range req.KeyOrder {
		record, ok := matching(p, cached, key, req.ExpectedID[key])
		if !ok {
			continue
		}
		if !marketkey.Fresh(p.FetchedAt(record), now, ttl) {
			opts.noteStale(key, record)
			continue
		}
		if p.Apply(req.IndexesByKey[key], record) {
			complete[key] = true
		}
	}

	// Tier 2: PostgreSQL, promoting anything fresh back into Redis.
	remaining := incomplete(req.KeyOrder, complete)
	if len(remaining) > 0 {
		stored := load(ctx, remaining, p.LoadStore, p.Logf, "postgres load failed")
		promotions := make([]Write[R], 0, len(remaining))
		for _, key := range remaining {
			record, ok := matching(p, stored, key, req.ExpectedID[key])
			if !ok {
				continue
			}
			fetchedAt := p.FetchedAt(record)
			if !marketkey.Fresh(fetchedAt, now, ttl) {
				opts.noteStale(key, record)
				continue
			}
			if !p.Apply(req.IndexesByKey[key], record) {
				continue
			}
			complete[key] = true
			if left := marketkey.RemainingTTL(fetchedAt, now, ttl); left > 0 {
				promotions = append(promotions, Write[R]{Record: record, TTL: left})
			}
		}
		if len(promotions) > 0 {
			if err := p.SaveCache(ctx, promotions); err != nil {
				p.Logf("redis promotion failed: %v", err)
			}
		}
	}

	// Tier 3: the provider itself.
	fetchMissing(ctx, p, req, complete)
	return complete
}

func fetchMissing[ID cmp.Ordered, R any](ctx context.Context, p Provider[ID, R], req Request[ID], complete map[marketkey.Key]bool) {
	// Keys with a live negative-cache entry are dropped before batching: the
	// provider had nothing for them recently, so asking again wastes a call.
	missed, err := p.LoadMisses(ctx, incomplete(req.KeyOrder, complete))
	if err != nil {
		p.Logf("miss load failed: %v", err)
		missed = nil
	}

	groups := make(map[ID][]marketkey.Key)
	for _, key := range req.KeyOrder {
		if complete[key] {
			continue
		}
		if _, suppressed := missed[key]; suppressed {
			continue
		}
		id, ok := req.ExpectedID[key]
		if !ok {
			continue
		}
		groups[id] = append(groups[id], key)
	}
	if len(groups) == 0 {
		return
	}

	ids := make([]ID, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	limit := p.BatchLimit()
	if limit < 1 {
		limit = 1
	}
	for start := 0; start < len(ids); start += limit {
		if ctx.Err() != nil {
			return
		}
		end := start + limit
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]

		records, err := p.Fetch(ctx, batch)
		if err != nil {
			p.Logf("price batch failed for %d IDs: %v", len(batch), err)
			if ctx.Err() != nil {
				return
			}
			continue
		}

		fetched := make([]R, 0, len(batch))
		misses := make([]marketkey.Key, 0)
		for _, id := range batch {
			record, ok := records[id]
			// The provider answered the batch but had nothing for this ID, or
			// answered with an empty payload: remember that so the next request
			// does not ask again. A failed batch never reaches here.
			if !ok || !p.HasData(record) {
				misses = append(misses, groups[id]...)
				continue
			}
			// One provider ID can serve several keys, so each key gets the
			// record stamped with its own key before being applied or stored.
			for _, key := range groups[id] {
				if complete[key] {
					continue
				}
				keyed := p.WithKey(record, key)
				if !p.Apply(req.IndexesByKey[key], keyed) {
					continue
				}
				complete[key] = true
				fetched = append(fetched, keyed)
			}
		}
		if len(misses) > 0 {
			if err := p.SaveMisses(ctx, misses, p.MissTTL()); err != nil {
				p.Logf("miss save failed: %v", err)
			}
		}
		if len(fetched) == 0 {
			continue
		}
		// Persistence failures are logged, never fatal: the caller already has
		// these records applied and the remaining batches still run.
		if err := p.SaveStore(ctx, fetched); err != nil {
			p.Logf("postgres save failed: %v", err)
		}
		writes := make([]Write[R], len(fetched))
		for i, record := range fetched {
			writes[i] = Write[R]{Record: record, TTL: p.TTL()}
		}
		if err := p.SaveCache(ctx, writes); err != nil {
			p.Logf("redis save failed: %v", err)
		}
	}
}

func load[R any](
	ctx context.Context,
	keys []marketkey.Key,
	loader func(context.Context, []marketkey.Key) (map[marketkey.Key]R, error),
	logf func(string, ...any),
	failure string,
) map[marketkey.Key]R {
	loaded, err := loader(ctx, keys)
	if err != nil {
		logf("%s: %v", failure, err)
		return nil
	}
	return loaded
}

func matching[ID cmp.Ordered, R any](p Provider[ID, R], records map[marketkey.Key]R, key marketkey.Key, expected ID) (R, bool) {
	record, ok := records[key]
	if !ok || !p.Matches(record, expected) {
		var zero R
		return zero, false
	}
	return p.WithKey(record, key), true
}

func incomplete(keys []marketkey.Key, complete map[marketkey.Key]bool) []marketkey.Key {
	remaining := make([]marketkey.Key, 0, len(keys))
	for _, key := range keys {
		if !complete[key] {
			remaining = append(remaining, key)
		}
	}
	return remaining
}

func (o Options[R]) noteStale(key marketkey.Key, record R) {
	if o.Stale != nil {
		o.Stale(key, record)
	}
}

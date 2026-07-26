package marketpipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"wallet-api/internal/marketkey"
)

// rec is a minimal stand-in for a provider record: an ID, a payload that may be
// absent, and a fetch time.
type rec struct {
	id        string
	key       marketkey.Key
	payload   string // empty means "carried no usable data"
	fetchedAt time.Time
}

type fakeProvider struct {
	ttl   time.Duration
	now   time.Time
	limit int

	cache map[marketkey.Key]rec
	store map[marketkey.Key]rec
	api   map[string]rec

	cacheErr, storeErr, fetchErr         error
	saveCacheErr, saveStoreErr           error
	cacheLoads, storeLoads               int
	batches                              [][]string
	cacheWrites, promotions, storeWrites []rec
	applied                              map[int]rec
	cancelAfterFirstBatch                bool
	misses                               map[marketkey.Key]struct{}
	savedMisses                          []marketkey.Key
	savedMissTTL                         time.Duration
	missTTL                              time.Duration
	cancel                               context.CancelFunc
	promotionTTLs                        []time.Duration
}

func newFake(ttl time.Duration, now time.Time) *fakeProvider {
	return &fakeProvider{
		ttl: ttl, now: now, limit: 2, missTTL: 2 * time.Hour,
		misses:  map[marketkey.Key]struct{}{},
		cache:   map[marketkey.Key]rec{},
		store:   map[marketkey.Key]rec{},
		api:     map[string]rec{},
		applied: map[int]rec{},
	}
}

func (f *fakeProvider) LoadCache(_ context.Context, keys []marketkey.Key) (map[marketkey.Key]rec, error) {
	f.cacheLoads++
	if f.cacheErr != nil {
		return nil, f.cacheErr
	}
	return subset(f.cache, keys), nil
}

func (f *fakeProvider) LoadStore(_ context.Context, keys []marketkey.Key) (map[marketkey.Key]rec, error) {
	f.storeLoads++
	if f.storeErr != nil {
		return nil, f.storeErr
	}
	return subset(f.store, keys), nil
}

func (f *fakeProvider) SaveCache(_ context.Context, writes []Write[rec]) error {
	for _, w := range writes {
		f.cacheWrites = append(f.cacheWrites, w.Record)
		// A promotion carries less than a full TTL.
		if w.TTL != f.ttl {
			f.promotions = append(f.promotions, w.Record)
			f.promotionTTLs = append(f.promotionTTLs, w.TTL)
		}
	}
	return f.saveCacheErr
}

func (f *fakeProvider) SaveStore(_ context.Context, records []rec) error {
	f.storeWrites = append(f.storeWrites, records...)
	return f.saveStoreErr
}

func (f *fakeProvider) Fetch(_ context.Context, ids []string) (map[string]rec, error) {
	batch := append([]string(nil), ids...)
	f.batches = append(f.batches, batch)
	if f.cancelAfterFirstBatch && f.cancel != nil && len(f.batches) == 1 {
		f.cancel()
	}
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	out := map[string]rec{}
	for _, id := range ids {
		if r, ok := f.api[id]; ok {
			out[id] = r
		}
	}
	return out, nil
}

func (f *fakeProvider) LoadMisses(_ context.Context, keys []marketkey.Key) (map[marketkey.Key]struct{}, error) {
	out := map[marketkey.Key]struct{}{}
	for _, k := range keys {
		if _, ok := f.misses[k]; ok {
			out[k] = struct{}{}
		}
	}
	return out, nil
}

func (f *fakeProvider) SaveMisses(_ context.Context, keys []marketkey.Key, ttl time.Duration) error {
	f.savedMisses = append(f.savedMisses, keys...)
	f.savedMissTTL = ttl
	return nil
}

func (f *fakeProvider) HasData(r rec) bool     { return r.payload != "" }
func (f *fakeProvider) MissTTL() time.Duration { return f.missTTL }

func (f *fakeProvider) BatchLimit() int                      { return f.limit }
func (f *fakeProvider) Matches(r rec, expected string) bool  { return r.id == expected }
func (f *fakeProvider) FetchedAt(r rec) time.Time            { return r.fetchedAt }
func (f *fakeProvider) WithKey(r rec, key marketkey.Key) rec { r.key = key; return r }
func (f *fakeProvider) TTL() time.Duration                   { return f.ttl }
func (f *fakeProvider) Now() time.Time                       { return f.now }
func (f *fakeProvider) Logf(string, ...any)                  {}

func (f *fakeProvider) Apply(indexes []int, r rec) bool {
	if r.payload == "" {
		return false
	}
	for _, i := range indexes {
		f.applied[i] = r
	}
	return true
}

func subset(src map[marketkey.Key]rec, keys []marketkey.Key) map[marketkey.Key]rec {
	out := map[marketkey.Key]rec{}
	for _, k := range keys {
		if r, ok := src[k]; ok {
			out[k] = r
		}
	}
	return out
}

func keyFor(addr string) marketkey.Key { return marketkey.ContractKey("ethereum", addr) }

func requestFor(pairs map[marketkey.Key]string, order []marketkey.Key) Request[string] {
	req := Request[string]{
		KeyOrder:     order,
		ExpectedID:   map[marketkey.Key]string{},
		IndexesByKey: map[marketkey.Key][]int{},
	}
	for i, k := range order {
		req.ExpectedID[k] = pairs[k]
		req.IndexesByKey[k] = []int{i}
	}
	return req
}

func TestRedisHitSkipsLaterTiers(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	f := newFake(30*time.Minute, now)
	k := keyFor("0xabc")
	f.cache[k] = rec{id: "usdc", payload: "1.00", fetchedAt: now.Add(-time.Minute)}

	Run(context.Background(), f, requestFor(map[marketkey.Key]string{k: "usdc"}, []marketkey.Key{k}), Options[rec]{})

	if f.storeLoads != 0 {
		t.Errorf("postgres queried %d times, want 0", f.storeLoads)
	}
	if len(f.batches) != 0 {
		t.Errorf("provider called %d times, want 0", len(f.batches))
	}
	if f.applied[0].payload != "1.00" {
		t.Errorf("result not applied from Redis, got %+v", f.applied[0])
	}
}

func TestExpiredRecordIsAMissNotAFallback(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	f := newFake(30*time.Minute, now)
	k := keyFor("0xabc")
	// Exactly at the boundary counts as expired.
	f.cache[k] = rec{id: "usdc", payload: "stale", fetchedAt: now.Add(-30 * time.Minute)}
	f.api["usdc"] = rec{id: "usdc", payload: "fresh", fetchedAt: now}

	Run(context.Background(), f, requestFor(map[marketkey.Key]string{k: "usdc"}, []marketkey.Key{k}), Options[rec]{})

	if f.applied[0].payload != "fresh" {
		t.Errorf("expired record was served, got %q", f.applied[0].payload)
	}
	if f.storeLoads != 1 {
		t.Errorf("postgres queried %d times, want 1", f.storeLoads)
	}
}

func TestMismatchedIDIsDiscarded(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	f := newFake(30*time.Minute, now)
	k := keyFor("0xabc")
	f.cache[k] = rec{id: "someone-else", payload: "wrong", fetchedAt: now}
	f.api["usdc"] = rec{id: "usdc", payload: "right", fetchedAt: now}

	Run(context.Background(), f, requestFor(map[marketkey.Key]string{k: "usdc"}, []marketkey.Key{k}), Options[rec]{})

	if f.applied[0].payload != "right" {
		t.Errorf("mismatched record was served, got %q", f.applied[0].payload)
	}
}

func TestPromotionCarriesRemainingTTLOnly(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	f := newFake(30*time.Minute, now)
	k := keyFor("0xabc")
	f.store[k] = rec{id: "usdc", payload: "1.00", fetchedAt: now.Add(-10 * time.Minute)}

	Run(context.Background(), f, requestFor(map[marketkey.Key]string{k: "usdc"}, []marketkey.Key{k}), Options[rec]{})

	if len(f.promotionTTLs) != 1 {
		t.Fatalf("promotions = %d, want 1", len(f.promotionTTLs))
	}
	if want := 20 * time.Minute; f.promotionTTLs[0] != want {
		t.Errorf("promotion TTL = %v, want %v", f.promotionTTLs[0], want)
	}
	if len(f.batches) != 0 {
		t.Errorf("provider called despite a fresh postgres record")
	}
}

func TestNullPayloadNeitherCompletesNorPersists(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	f := newFake(30*time.Minute, now)
	k := keyFor("0xabc")
	f.api["usdc"] = rec{id: "usdc", payload: "", fetchedAt: now}

	Run(context.Background(), f, requestFor(map[marketkey.Key]string{k: "usdc"}, []marketkey.Key{k}), Options[rec]{})

	if _, ok := f.applied[0]; ok {
		t.Errorf("null payload occupied a result slot")
	}
	if len(f.storeWrites) != 0 {
		t.Errorf("null payload written to postgres: %+v", f.storeWrites)
	}
	if len(f.cacheWrites) != 0 {
		t.Errorf("null payload written to redis: %+v", f.cacheWrites)
	}
}

func TestBatchingRespectsLimitAndOrdersIDs(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	f := newFake(30*time.Minute, now)
	f.limit = 2

	order := []marketkey.Key{keyFor("0xc"), keyFor("0xa"), keyFor("0xb"), keyFor("0xd"), keyFor("0xe")}
	ids := map[marketkey.Key]string{
		order[0]: "c", order[1]: "a", order[2]: "b", order[3]: "d", order[4]: "e",
	}
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		f.api[id] = rec{id: id, payload: id, fetchedAt: now}
	}

	Run(context.Background(), f, requestFor(ids, order), Options[rec]{})

	if len(f.batches) != 3 {
		t.Fatalf("batches = %d, want 3", len(f.batches))
	}
	want := [][]string{{"a", "b"}, {"c", "d"}, {"e"}}
	for i, batch := range f.batches {
		if len(batch) > f.limit {
			t.Errorf("batch %d has %d IDs, over the limit of %d", i, len(batch), f.limit)
		}
		if len(batch) != len(want[i]) {
			t.Fatalf("batch %d = %v, want %v", i, batch, want[i])
		}
		for j := range batch {
			if batch[j] != want[i][j] {
				t.Errorf("batch %d = %v, want %v (IDs must be ordered)", i, batch, want[i])
			}
		}
	}
}

func TestCancellationStopsFurtherBatchesButKeepsResults(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	f := newFake(30*time.Minute, now)
	f.limit = 1
	f.cancelAfterFirstBatch = true

	order := []marketkey.Key{keyFor("0xa"), keyFor("0xb"), keyFor("0xc")}
	ids := map[marketkey.Key]string{order[0]: "a", order[1]: "b", order[2]: "c"}
	for _, id := range []string{"a", "b", "c"} {
		f.api[id] = rec{id: id, payload: id, fetchedAt: now}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.cancel = cancel

	Run(ctx, f, requestFor(ids, order), Options[rec]{})

	if len(f.batches) != 1 {
		t.Errorf("batches = %d, want 1 (cancellation must stop the loop)", len(f.batches))
	}
	if f.applied[0].payload != "a" {
		t.Errorf("results from the completed batch were discarded, got %+v", f.applied[0])
	}
}

func TestFailedBatchDoesNotAbortRemainingBatches(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	f := newFake(30*time.Minute, now)
	f.limit = 1
	f.fetchErr = errors.New("provider unavailable")

	order := []marketkey.Key{keyFor("0xa"), keyFor("0xb"), keyFor("0xc")}
	ids := map[marketkey.Key]string{order[0]: "a", order[1]: "b", order[2]: "c"}

	Run(context.Background(), f, requestFor(ids, order), Options[rec]{})

	if len(f.batches) != 3 {
		t.Errorf("batches = %d, want 3 (a failing batch must not abort the rest)", len(f.batches))
	}
}

func TestSaveFailuresDoNotDiscardResults(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	f := newFake(30*time.Minute, now)
	f.saveCacheErr = errors.New("redis down")
	f.saveStoreErr = errors.New("postgres down")
	k := keyFor("0xabc")
	f.api["usdc"] = rec{id: "usdc", payload: "1.00", fetchedAt: now}

	Run(context.Background(), f, requestFor(map[marketkey.Key]string{k: "usdc"}, []marketkey.Key{k}), Options[rec]{})

	if f.applied[0].payload != "1.00" {
		t.Errorf("persistence failure discarded the fetched record, got %+v", f.applied[0])
	}
}

func TestLoadFailuresFallThroughToNextTier(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	f := newFake(30*time.Minute, now)
	f.cacheErr = errors.New("redis down")
	f.storeErr = errors.New("postgres down")
	k := keyFor("0xabc")
	f.api["usdc"] = rec{id: "usdc", payload: "1.00", fetchedAt: now}

	Run(context.Background(), f, requestFor(map[marketkey.Key]string{k: "usdc"}, []marketkey.Key{k}), Options[rec]{})

	if f.applied[0].payload != "1.00" {
		t.Errorf("tier failures did not fall through to the provider, got %+v", f.applied[0])
	}
}

func TestStaleHookSeesExpiredRecordsAndIsOptional(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	k := keyFor("0xabc")

	t.Run("hook receives expired records from both tiers", func(t *testing.T) {
		f := newFake(30*time.Minute, now)
		f.cache[k] = rec{id: "usdc", payload: "old-redis", fetchedAt: now.Add(-time.Hour)}
		f.store[k] = rec{id: "usdc", payload: "old-postgres", fetchedAt: now.Add(-2 * time.Hour)}

		var seen []string
		Run(context.Background(), f, requestFor(map[marketkey.Key]string{k: "usdc"}, []marketkey.Key{k}), Options[rec]{
			Stale: func(_ marketkey.Key, r rec) { seen = append(seen, r.payload) },
		})

		if len(seen) != 2 {
			t.Fatalf("stale hook saw %v, want both expired records", seen)
		}
	})

	t.Run("nil hook simply drops them", func(t *testing.T) {
		f := newFake(30*time.Minute, now)
		f.cache[k] = rec{id: "usdc", payload: "old", fetchedAt: now.Add(-time.Hour)}

		Run(context.Background(), f, requestFor(map[marketkey.Key]string{k: "usdc"}, []marketkey.Key{k}), Options[rec]{})

		if _, ok := f.applied[0]; ok {
			t.Errorf("expired record was applied with no stale hook")
		}
	})
}

// One provider ID can back several keys. Each persisted record must carry its
// own key, or the write lands under the wrong key — or under none at all.
func TestFetchedRecordsArePersistedWithTheirOwnKey(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	f := newFake(30*time.Minute, now)
	weth := keyFor("0xc02a")
	alias := keyFor("0xdead")
	order := []marketkey.Key{weth, alias}

	f.api["ether"] = rec{id: "ether", payload: "3000", fetchedAt: now}

	Run(context.Background(), f, requestFor(map[marketkey.Key]string{weth: "ether", alias: "ether"}, order), Options[rec]{})

	if len(f.storeWrites) != 2 {
		t.Fatalf("store writes = %d, want 2 (one per key)", len(f.storeWrites))
	}
	got := map[marketkey.Key]bool{}
	for _, r := range f.storeWrites {
		got[r.key] = true
	}
	for _, want := range order {
		if !got[want] {
			t.Errorf("no record persisted for key %+v; wrote %+v", want, f.storeWrites)
		}
	}
}

func TestLiveMissSuppressesTheProviderCall(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	f := newFake(30*time.Minute, now)
	k := keyFor("0xabc")
	f.misses[k] = struct{}{}
	f.api["usdc"] = rec{id: "usdc", payload: "1.00", fetchedAt: now}

	Run(context.Background(), f, requestFor(map[marketkey.Key]string{k: "usdc"}, []marketkey.Key{k}), Options[rec]{})

	if len(f.batches) != 0 {
		t.Errorf("provider called %d times despite a live miss", len(f.batches))
	}
	if _, ok := f.applied[0]; ok {
		t.Errorf("suppressed key produced a result")
	}
}

// An expired miss is simply absent from LoadMisses — Redis TTL does the expiry.
func TestExpiredMissReleasesTheKey(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	f := newFake(30*time.Minute, now)
	k := keyFor("0xabc")
	f.api["usdc"] = rec{id: "usdc", payload: "1.00", fetchedAt: now}

	Run(context.Background(), f, requestFor(map[marketkey.Key]string{k: "usdc"}, []marketkey.Key{k}), Options[rec]{})

	if len(f.batches) != 1 {
		t.Errorf("batches = %d, want 1 once the miss has expired", len(f.batches))
	}
	if f.applied[0].payload != "1.00" {
		t.Errorf("result = %+v, want the freshly fetched record", f.applied[0])
	}
}

func TestFreshRecordOutranksLiveMiss(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	k := keyFor("0xabc")

	for _, tier := range []string{"redis", "postgres"} {
		t.Run(tier, func(t *testing.T) {
			f := newFake(30*time.Minute, now)
			f.misses[k] = struct{}{}
			fresh := rec{id: "usdc", payload: "1.00", fetchedAt: now}
			if tier == "redis" {
				f.cache[k] = fresh
			} else {
				f.store[k] = fresh
			}

			Run(context.Background(), f, requestFor(map[marketkey.Key]string{k: "usdc"}, []marketkey.Key{k}), Options[rec]{})

			if f.applied[0].payload != "1.00" {
				t.Errorf("live miss shadowed a fresh %s record: %+v", tier, f.applied[0])
			}
		})
	}
}

func TestMissIsRecordedOnlyWhenTheProviderHasNothing(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	k := keyFor("0xabc")

	t.Run("absent from the response", func(t *testing.T) {
		f := newFake(30*time.Minute, now)
		Run(context.Background(), f, requestFor(map[marketkey.Key]string{k: "usdc"}, []marketkey.Key{k}), Options[rec]{})
		if len(f.savedMisses) != 1 || f.savedMisses[0] != k {
			t.Errorf("misses = %+v, want the unanswered key", f.savedMisses)
		}
		if f.savedMissTTL != f.missTTL {
			t.Errorf("miss TTL = %v, want %v", f.savedMissTTL, f.missTTL)
		}
	})

	t.Run("empty payload counts as nothing", func(t *testing.T) {
		f := newFake(30*time.Minute, now)
		f.api["usdc"] = rec{id: "usdc", payload: "", fetchedAt: now}
		Run(context.Background(), f, requestFor(map[marketkey.Key]string{k: "usdc"}, []marketkey.Key{k}), Options[rec]{})
		if len(f.savedMisses) != 1 {
			t.Errorf("misses = %+v, want one for the empty payload", f.savedMisses)
		}
	})

	// A failed batch says nothing about whether the provider has the token.
	t.Run("batch error records nothing", func(t *testing.T) {
		f := newFake(30*time.Minute, now)
		f.fetchErr = errors.New("provider unavailable")
		Run(context.Background(), f, requestFor(map[marketkey.Key]string{k: "usdc"}, []marketkey.Key{k}), Options[rec]{})
		if len(f.savedMisses) != 0 {
			t.Errorf("misses = %+v, want none after a batch error", f.savedMisses)
		}
	})

	t.Run("usable data records nothing", func(t *testing.T) {
		f := newFake(30*time.Minute, now)
		f.api["usdc"] = rec{id: "usdc", payload: "1.00", fetchedAt: now}
		Run(context.Background(), f, requestFor(map[marketkey.Key]string{k: "usdc"}, []marketkey.Key{k}), Options[rec]{})
		if len(f.savedMisses) != 0 {
			t.Errorf("misses = %+v, want none when the provider had data", f.savedMisses)
		}
	})
}

// A miss never satisfies a market read: it suppresses the provider call but is
// not itself a record, so the result slot stays empty rather than being filled.
func TestMissNeverSatisfiesAMarketRead(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	f := newFake(30*time.Minute, now)
	k := keyFor("0xabc")
	f.misses[k] = struct{}{}

	Run(context.Background(), f, requestFor(map[marketkey.Key]string{k: "usdc"}, []marketkey.Key{k}), Options[rec]{})

	if _, ok := f.applied[0]; ok {
		t.Errorf("a miss produced a result: %+v", f.applied[0])
	}
	if len(f.cacheWrites) != 0 || len(f.storeWrites) != 0 {
		t.Errorf("a miss caused market writes: cache=%+v store=%+v", f.cacheWrites, f.storeWrites)
	}
}

func TestKeyIsStampedOntoLoadedRecords(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	f := newFake(30*time.Minute, now)
	k := keyFor("0xabc")
	f.cache[k] = rec{id: "usdc", payload: "1.00", fetchedAt: now}

	Run(context.Background(), f, requestFor(map[marketkey.Key]string{k: "usdc"}, []marketkey.Key{k}), Options[rec]{})

	if f.applied[0].key != k {
		t.Errorf("key not stamped onto loaded record: %+v", f.applied[0])
	}
}

package tokenvalidity

import (
	"context"
	"errors"
	"testing"
	"time"

	"wallet-api/internal/moralis"
)

type fakeMoralis struct {
	meta  moralis.Metadata
	err   error
	calls int
}

func (f *fakeMoralis) GetTokenMetadata(ctx context.Context, address string) (moralis.Metadata, error) {
	f.calls++
	return f.meta, f.err
}

type fakeCache struct {
	rec    Record
	hit    bool
	saved  int
	loaded int
}

func (f *fakeCache) LoadTokenMeta(ctx context.Context, chain, address string) (Record, bool, error) {
	f.loaded++
	return f.rec, f.hit, nil
}
func (f *fakeCache) SaveTokenMeta(ctx context.Context, chain, address string, r Record, ttl time.Duration) error {
	f.saved++
	f.rec, f.hit = r, true
	return nil
}

type fakeStore struct {
	rec   Record
	hit   bool
	saved int
}

func (f *fakeStore) GetTokenMeta(ctx context.Context, chain, address string) (Record, bool, error) {
	return f.rec, f.hit, nil
}
func (f *fakeStore) SaveTokenMeta(ctx context.Context, chain, address string, r Record) error {
	f.saved++
	f.rec, f.hit = r, true
	return nil
}

var fixedNow = time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

func newChecker(m MoralisClient, c Cache, s Store) *Checker {
	ch := NewChecker(m, c, s, "eth", 7*24*time.Hour, 24*time.Hour)
	ch.now = func() time.Time { return fixedNow }
	return ch
}

func TestValidateRedisHitSkipsStoreAndMoralis(t *testing.T) {
	cache := &fakeCache{hit: true, rec: Record{Verified: true, Symbol: "PEPE", Logo: "L"}}
	m := &fakeMoralis{}
	store := &fakeStore{}
	got, err := newChecker(m, cache, store).Validate(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.Symbol != "PEPE" || got.LogoURI != "L" {
		t.Errorf("validation wrong: %+v", got)
	}
	if m.calls != 0 || store.saved != 0 {
		t.Errorf("redis hit should skip moralis(%d)/store(%d)", m.calls, store.saved)
	}
}

func TestValidateFreshStoreHitRepopulatesRedis(t *testing.T) {
	cache := &fakeCache{}
	store := &fakeStore{hit: true, rec: Record{Verified: true, Symbol: "USDC", FetchedAt: fixedNow.Add(-24 * time.Hour)}}
	m := &fakeMoralis{}
	got, err := newChecker(m, cache, store).Validate(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.Symbol != "USDC" {
		t.Errorf("validation wrong: %+v", got)
	}
	if m.calls != 0 {
		t.Errorf("fresh store hit should skip moralis, calls=%d", m.calls)
	}
	if cache.saved != 1 {
		t.Errorf("expected redis repopulated, saved=%d", cache.saved)
	}
}

func TestValidateStaleStoreTriggersMoralisAndPersists(t *testing.T) {
	cache := &fakeCache{}
	store := &fakeStore{hit: true, rec: Record{Verified: false, FetchedAt: fixedNow.Add(-8 * 24 * time.Hour)}}
	m := &fakeMoralis{meta: moralis.Metadata{Symbol: "PEPE", Logo: "L", Decimals: 18, VerifiedContract: true}}
	got, err := newChecker(m, cache, store).Validate(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.Symbol != "PEPE" {
		t.Errorf("stale should refresh from moralis: %+v", got)
	}
	if m.calls != 1 || store.saved != 1 || cache.saved != 1 {
		t.Errorf("expected moralis+persist, m=%d store=%d cache=%d", m.calls, store.saved, cache.saved)
	}
}

func TestValidateMissAllFetchesAndPersists(t *testing.T) {
	cache := &fakeCache{}
	store := &fakeStore{}
	m := &fakeMoralis{meta: moralis.Metadata{Symbol: "PEPE", VerifiedContract: true, PossibleSpam: false}}
	got, err := newChecker(m, cache, store).Validate(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Valid {
		t.Errorf("expected valid: %+v", got)
	}
	if store.saved != 1 || cache.saved != 1 {
		t.Errorf("expected persisted to store+cache, store=%d cache=%d", store.saved, cache.saved)
	}
}

func TestValidatePossibleSpamIsInvalid(t *testing.T) {
	m := &fakeMoralis{meta: moralis.Metadata{VerifiedContract: true, PossibleSpam: true}}
	got, err := newChecker(m, &fakeCache{}, &fakeStore{}).Validate(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if got.Valid {
		t.Error("possible_spam token must be invalid")
	}
}

func TestValidateUnverifiedIsInvalid(t *testing.T) {
	m := &fakeMoralis{meta: moralis.Metadata{VerifiedContract: false, PossibleSpam: false}}
	got, err := newChecker(m, &fakeCache{}, &fakeStore{}).Validate(context.Background(), "0xABC")
	if err != nil {
		t.Fatal(err)
	}
	if got.Valid {
		t.Error("unverified token must be invalid")
	}
}

func TestValidateMoralisErrorWithStaleUsesStale(t *testing.T) {
	store := &fakeStore{hit: true, rec: Record{Verified: true, Symbol: "USDC", FetchedAt: fixedNow.Add(-30 * 24 * time.Hour)}}
	m := &fakeMoralis{err: errors.New("boom")}
	got, err := newChecker(m, &fakeCache{}, store).Validate(context.Background(), "0xABC")
	if err != nil {
		t.Fatalf("stale fallback should not error: %v", err)
	}
	if !got.Valid || got.Symbol != "USDC" {
		t.Errorf("expected stale verdict used: %+v", got)
	}
}

func TestValidateMoralisErrorWithoutRecordFailsClosed(t *testing.T) {
	m := &fakeMoralis{err: errors.New("boom")}
	got, err := newChecker(m, &fakeCache{}, &fakeStore{}).Validate(context.Background(), "0xABC")
	if err == nil {
		t.Fatal("expected error when no record and moralis fails")
	}
	if got.Valid {
		t.Error("must be invalid when moralis fails and nothing cached")
	}
}

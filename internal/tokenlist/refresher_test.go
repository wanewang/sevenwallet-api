package tokenlist

import (
	"context"
	"errors"
	"testing"
	"time"

	"wallet-api/internal/lifi"
)

type fakeLifi struct {
	tokens []lifi.ListToken
	err    error
	calls  int
}

func (f *fakeLifi) GetTokens(ctx context.Context, chain string) ([]lifi.ListToken, error) {
	f.calls++
	return f.tokens, f.err
}

type fakeStore struct {
	tokens    []lifi.ListToken
	fetchedAt time.Time
	present   bool
	saveCalls int
	saveErr   error
	loadErr   error
}

func (s *fakeStore) SaveTokenList(ctx context.Context, chain string, tokens []lifi.ListToken, fetchedAt time.Time) error {
	s.saveCalls++
	if s.saveErr != nil {
		return s.saveErr
	}
	s.tokens, s.fetchedAt, s.present = tokens, fetchedAt, true
	return nil
}

func (s *fakeStore) LoadTokenList(ctx context.Context, chain string) ([]lifi.ListToken, time.Time, bool, error) {
	if s.loadErr != nil {
		return nil, time.Time{}, false, s.loadErr
	}
	if !s.present {
		return nil, time.Time{}, false, nil
	}
	return s.tokens, s.fetchedAt, true, nil
}

var oneToken = []lifi.ListToken{{Address: "0xA0B8", Symbol: "USDC", Decimals: 6}}

func newRefresherForTest(l LifiClient, redis, pg Store, h *Holder) *Refresher {
	r := NewRefresher(l, redis, pg, h, "ETH", time.Hour)
	r.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	r.logf = func(string, ...any) {}
	return r
}

func TestBootstrapFetchSuccessPersistsAndSets(t *testing.T) {
	l := &fakeLifi{tokens: oneToken}
	redis, pg := &fakeStore{}, &fakeStore{}
	var h Holder
	r := newRefresherForTest(l, redis, pg, &h)

	if err := r.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if redis.saveCalls != 1 || pg.saveCalls != 1 {
		t.Errorf("expected both stores written, redis=%d pg=%d", redis.saveCalls, pg.saveCalls)
	}
	if h.Current() == nil || h.Current().Count() != 1 {
		t.Error("snapshot not set from fetch")
	}
}

func TestBootstrapFallsBackToRedisThenPostgres(t *testing.T) {
	// Fetch fails, Redis has the list.
	l := &fakeLifi{err: errors.New("lifi down")}
	redis := &fakeStore{tokens: oneToken, fetchedAt: time.Now(), present: true}
	pg := &fakeStore{}
	var h Holder
	r := newRefresherForTest(l, redis, pg, &h)
	if err := r.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap (redis fallback): %v", err)
	}
	if h.Current() == nil || h.Current().Count() != 1 {
		t.Error("snapshot not set from redis fallback")
	}

	// Fetch fails, Redis empty, Postgres has the list.
	redis2, pg2 := &fakeStore{}, &fakeStore{tokens: oneToken, fetchedAt: time.Now(), present: true}
	var h2 Holder
	r2 := newRefresherForTest(&fakeLifi{err: errors.New("down")}, redis2, pg2, &h2)
	if err := r2.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap (pg fallback): %v", err)
	}
	if h2.Current() == nil || h2.Current().Count() != 1 {
		t.Error("snapshot not set from postgres fallback")
	}
}

func TestBootstrapErrorsWhenNothingAvailable(t *testing.T) {
	r := newRefresherForTest(&fakeLifi{err: errors.New("down")}, &fakeStore{}, &fakeStore{}, &Holder{})
	if err := r.Bootstrap(context.Background()); err == nil {
		t.Fatal("expected error when no source has a list")
	}
}

func TestRefreshSwapsSnapshotOnSuccess(t *testing.T) {
	l := &fakeLifi{tokens: oneToken}
	redis, pg := &fakeStore{}, &fakeStore{}
	var h Holder
	r := newRefresherForTest(l, redis, pg, &h)
	r.refresh(context.Background())
	if h.Current() == nil || h.Current().Count() != 1 {
		t.Error("refresh did not set snapshot")
	}
}

func TestRefreshRetainsPriorSnapshotOnError(t *testing.T) {
	var h Holder
	h.Set(NewSnapshot("ETH", oneToken, time.Now()))
	prior := h.Current()
	r := newRefresherForTest(&fakeLifi{err: errors.New("down")}, &fakeStore{}, &fakeStore{}, &h)
	r.refresh(context.Background())
	if h.Current() != prior {
		t.Error("failed refresh must keep the prior snapshot")
	}
}

func TestBootstrapEmptyFetchFallsBackToStore(t *testing.T) {
	// fakeLifi returns empty slice (no error); redis has a non-empty list.
	l := &fakeLifi{tokens: nil}
	redis := &fakeStore{tokens: oneToken, fetchedAt: time.Now(), present: true}
	pg := &fakeStore{}
	var h Holder
	r := newRefresherForTest(l, redis, pg, &h)

	if err := r.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if h.Current() == nil || h.Current().Count() != 1 {
		t.Errorf("expected snapshot from redis, got count=%d", func() int {
			if h.Current() == nil {
				return -1
			}
			return h.Current().Count()
		}())
	}
	// Empty fetch must NOT have written to any store.
	if redis.saveCalls != 0 || pg.saveCalls != 0 {
		t.Errorf("empty fetch must not persist: redis saveCalls=%d pg saveCalls=%d", redis.saveCalls, pg.saveCalls)
	}
}

func TestRefreshEmptyFetchKeepsPriorSnapshot(t *testing.T) {
	var h Holder
	h.Set(NewSnapshot("ETH", oneToken, time.Now()))
	prior := h.Current()

	redis, pg := &fakeStore{}, &fakeStore{}
	r := newRefresherForTest(&fakeLifi{tokens: []lifi.ListToken{}}, redis, pg, &h)
	r.refresh(context.Background())

	if h.Current() != prior {
		t.Error("empty fetch must keep the prior snapshot")
	}
	if redis.saveCalls != 0 || pg.saveCalls != 0 {
		t.Errorf("empty fetch must not persist: redis saveCalls=%d pg saveCalls=%d", redis.saveCalls, pg.saveCalls)
	}
}

func TestBootstrapEmptyRedisFallsBackToPostgres(t *testing.T) {
	// Fetch fails; Redis reports present (ok=true) but with zero tokens;
	// Postgres has a real list. Bootstrap must skip the empty Redis list.
	l := &fakeLifi{err: errors.New("lifi down")}
	redis := &fakeStore{present: true, tokens: nil} // ok=true, empty
	pg := &fakeStore{present: true, tokens: oneToken, fetchedAt: time.Now()}
	var h Holder
	r := newRefresherForTest(l, redis, pg, &h)

	if err := r.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if h.Current() == nil || h.Current().Count() != 1 {
		t.Errorf("expected snapshot from postgres (count 1), got %v", h.Current())
	}
}

func TestBootstrapAllStoresPresentButEmptyReturnsError(t *testing.T) {
	// Fetch fails; both stores report present (ok=true) but empty. There is no
	// usable list anywhere, so Bootstrap must error and leave the holder unset
	// rather than installing an all-hiding empty snapshot.
	l := &fakeLifi{err: errors.New("lifi down")}
	redis := &fakeStore{present: true, tokens: nil}
	pg := &fakeStore{present: true, tokens: []lifi.ListToken{}}
	var h Holder
	r := newRefresherForTest(l, redis, pg, &h)

	if err := r.Bootstrap(context.Background()); err == nil {
		t.Fatal("expected error when every source is empty")
	}
	if h.Current() != nil {
		t.Errorf("holder must stay unset when no non-empty list exists, got %v", h.Current())
	}
}

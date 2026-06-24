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

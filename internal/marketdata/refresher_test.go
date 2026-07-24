package marketdata

import (
	"context"
	"errors"
	"testing"
	"time"

	"wallet-api/internal/coingecko"
)

type fakeCoinListClient struct {
	coins  []coingecko.Coin
	err    error
	calls  int
	called chan struct{}
	cancel context.CancelFunc
}

func (f *fakeCoinListClient) ListCoins(context.Context) ([]coingecko.Coin, error) {
	f.calls++
	if f.cancel != nil {
		f.cancel()
	}
	if f.called != nil {
		select {
		case f.called <- struct{}{}:
		default:
		}
	}
	return f.coins, f.err
}

type fakeCatalogStore struct {
	mappings     []CoinMapping
	present      bool
	replaceErr   error
	loadErr      error
	replaceCalls int
	replaced     chan struct{}
	loadCtxErr   error
	loadDeadline bool
}

func (s *fakeCatalogStore) ReplaceCoinMappings(_ context.Context, mappings []CoinMapping) error {
	s.replaceCalls++
	if s.replaceErr != nil {
		return s.replaceErr
	}
	s.mappings = append([]CoinMapping(nil), mappings...)
	s.present = true
	if s.replaced != nil {
		select {
		case s.replaced <- struct{}{}:
		default:
		}
	}
	return nil
}

func (s *fakeCatalogStore) LoadCoinMappings(ctx context.Context) ([]CoinMapping, bool, error) {
	s.loadCtxErr = ctx.Err()
	_, s.loadDeadline = ctx.Deadline()
	if s.loadErr != nil {
		return nil, false, s.loadErr
	}
	if !s.present {
		return nil, false, nil
	}
	return append([]CoinMapping(nil), s.mappings...), true, nil
}

var refresherCoins = []coingecko.Coin{{ID: "ethereum", Name: "Ethereum", Symbol: "eth"}}
var refresherMappings = []CoinMapping{{ID: "ethereum", Name: "Ethereum", Symbol: "eth", Chain: "eth", Address: NativeAddress}}

func newTestRefresher(client CoinListClient, store CatalogStore, holder *Holder) *Refresher {
	r := NewRefresher(client, store, holder, time.Hour)
	r.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	r.logf = func(string, ...any) {}
	return r
}

func TestBootstrapFetchSuccessPersistsAndSets(t *testing.T) {
	store := &fakeCatalogStore{}
	var holder Holder
	r := newTestRefresher(&fakeCoinListClient{coins: refresherCoins}, store, &holder)

	r.Bootstrap(context.Background())

	if store.replaceCalls != 1 || len(store.mappings) != 1 {
		t.Fatalf("fetch was not persisted: replaceCalls=%d mappings=%#v", store.replaceCalls, store.mappings)
	}
	if got := holder.Current(); got == nil || got.Count() != len(refresherMappings) {
		t.Fatalf("fetch was not installed: %#v", got)
	}
}

func TestBootstrapEmptyTransformedFetchFallsBackToPostgres(t *testing.T) {
	store := &fakeCatalogStore{mappings: refresherMappings, present: true}
	var holder Holder
	r := newTestRefresher(&fakeCoinListClient{coins: []coingecko.Coin{{ID: " "}}}, store, &holder)

	r.Bootstrap(context.Background())

	if store.replaceCalls != 0 {
		t.Fatalf("empty transformed fetch was persisted: %d calls", store.replaceCalls)
	}
	if got := holder.Current(); got == nil || got.Count() != len(refresherMappings) {
		t.Fatalf("postgres fallback was not installed: %#v", got)
	}
}

func TestBootstrapBlankSymbolNativeFetchPreservesPriorSnapshot(t *testing.T) {
	prior := NewCatalog(refresherMappings)
	holder := Holder{}
	holder.Set(prior)
	store := &fakeCatalogStore{mappings: refresherMappings, present: true}
	r := newTestRefresher(&fakeCoinListClient{coins: []coingecko.Coin{{ID: "malformed-native", Symbol: " "}}}, store, &holder)

	r.Bootstrap(context.Background())

	if store.replaceCalls != 0 {
		t.Fatalf("blank-symbol native fetch was persisted: %d calls", store.replaceCalls)
	}
	if got := holder.Current(); got == nil || got.Count() != prior.Count() {
		t.Fatalf("bootstrap did not preserve prior holder snapshot: got %#v", got)
	}
	if resolved, ok := holder.ResolveNative("eth", nil); !ok || resolved != "ethereum" {
		t.Fatalf("bootstrap lost prior native lookup: (%q, %v)", resolved, ok)
	}
	if !store.present || len(store.mappings) != len(refresherMappings) {
		t.Fatalf("prior postgres snapshot changed: present=%v mappings=%#v", store.present, store.mappings)
	}
}

func TestBootstrapFetchFailureFallsBackToPostgres(t *testing.T) {
	store := &fakeCatalogStore{mappings: refresherMappings, present: true}
	var holder Holder
	r := newTestRefresher(&fakeCoinListClient{err: errors.New("CoinGecko down")}, store, &holder)

	r.Bootstrap(context.Background())

	if got := holder.Current(); got == nil || got.Count() != len(refresherMappings) {
		t.Fatalf("postgres fallback was not installed: %#v", got)
	}
}

func TestBootstrapExpiredFetchContextStillFallsBackToPostgres(t *testing.T) {
	store := &fakeCatalogStore{mappings: refresherMappings, present: true}
	var holder Holder
	ctx, cancel := context.WithCancel(context.Background())
	r := newTestRefresher(&fakeCoinListClient{err: context.Canceled, cancel: cancel}, store, &holder)

	r.Bootstrap(ctx)

	if got := holder.Current(); got == nil || got.Count() != len(refresherMappings) {
		t.Fatalf("postgres fallback was not installed after fetch cancellation: %#v", got)
	}
	if store.loadCtxErr != nil {
		t.Fatalf("postgres fallback received canceled context: %v", store.loadCtxErr)
	}
	if !store.loadDeadline {
		t.Fatal("postgres fallback context has no bounded deadline")
	}
}

func TestBootstrapReplaceFailureFallsBackToPostgres(t *testing.T) {
	store := &fakeCatalogStore{
		mappings:   refresherMappings,
		present:    true,
		replaceErr: errors.New("postgres write failed"),
	}
	var holder Holder
	r := newTestRefresher(&fakeCoinListClient{coins: refresherCoins}, store, &holder)

	r.Bootstrap(context.Background())

	if got := holder.Current(); got == nil || got.Count() != len(refresherMappings) {
		t.Fatalf("postgres fallback was not installed: %#v", got)
	}
}

func TestBootstrapWithNoSourceIsNonFatalAndLeavesHolderEmpty(t *testing.T) {
	var holder Holder
	r := newTestRefresher(&fakeCoinListClient{err: errors.New("CoinGecko down")}, &fakeCatalogStore{}, &holder)

	r.Bootstrap(context.Background())

	if holder.Current() != nil {
		t.Fatalf("holder should remain empty, got %#v", holder.Current())
	}
}

func TestRefresherRunSwapsCatalogAfterSuccessfulRefresh(t *testing.T) {
	store := &fakeCatalogStore{replaced: make(chan struct{}, 1)}
	var holder Holder
	r := newTestRefresher(&fakeCoinListClient{coins: refresherCoins}, store, &holder)
	r.interval = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()
	select {
	case <-store.replaced:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("refresh did not complete")
	}
	cancel()
	<-done

	if got := holder.Current(); got == nil || got.Count() != len(refresherMappings) {
		t.Fatalf("successful refresh was not installed: %#v", got)
	}
}

func TestFailedRefreshPreservesExactPriorCatalogPointer(t *testing.T) {
	var holder Holder
	prior := NewCatalog(refresherMappings)
	holder.Set(prior)
	store := &fakeCatalogStore{replaceErr: errors.New("postgres write failed")}
	r := newTestRefresher(&fakeCoinListClient{coins: refresherCoins}, store, &holder)

	r.refresh(context.Background())

	if holder.Current() != prior {
		t.Fatalf("failed refresh changed holder pointer: got %p want %p", holder.Current(), prior)
	}
}

func TestBlankSymbolNativeRefreshPreservesPriorSnapshot(t *testing.T) {
	prior := NewCatalog(refresherMappings)
	holder := Holder{}
	holder.Set(prior)
	store := &fakeCatalogStore{mappings: refresherMappings, present: true}
	r := newTestRefresher(&fakeCoinListClient{coins: []coingecko.Coin{{ID: "malformed-native", Symbol: " "}}}, store, &holder)

	r.refresh(context.Background())

	if store.replaceCalls != 0 {
		t.Fatalf("blank-symbol native refresh was persisted: %d calls", store.replaceCalls)
	}
	if holder.Current() != prior {
		t.Fatalf("refresh changed prior holder pointer: got %p want %p", holder.Current(), prior)
	}
	if !store.present || len(store.mappings) != len(refresherMappings) {
		t.Fatalf("prior postgres snapshot changed: present=%v mappings=%#v", store.present, store.mappings)
	}
}

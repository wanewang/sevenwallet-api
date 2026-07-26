package cmcmarket

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"wallet-api/internal/coinmarketcap"
)

type fakeListClient struct {
	pages  [][]coinmarketcap.Coin
	errAt  int
	starts []int
}

func (f *fakeListClient) ListCoinsPage(_ context.Context, start, _ int) ([]coinmarketcap.Coin, error) {
	f.starts = append(f.starts, start)
	index := len(f.starts) - 1
	if f.errAt > 0 && len(f.starts) == f.errAt {
		return nil, errors.New("down")
	}
	if index >= len(f.pages) {
		return nil, nil
	}
	return f.pages[index], nil
}

type fakeCatalogStore struct {
	mappings     []CoinMapping
	present      bool
	replaceErr   error
	replaceCalls int
}

func (s *fakeCatalogStore) ReplaceCoinMarketCapMappings(_ context.Context, mappings []CoinMapping) error {
	s.replaceCalls++
	if s.replaceErr != nil {
		return s.replaceErr
	}
	s.mappings = append([]CoinMapping(nil), mappings...)
	s.present = true
	return nil
}

func (s *fakeCatalogStore) LoadCoinMarketCapMappings(context.Context) ([]CoinMapping, bool, error) {
	return append([]CoinMapping(nil), s.mappings...), s.present, nil
}

func cmcCoin(id int64, address string) coinmarketcap.Coin {
	return coinmarketcap.Coin{ID: id, Symbol: "TKN", IsActive: 1, Platform: &coinmarketcap.Platform{ID: 1, TokenAddress: address}}
}

func testRefresher(client CoinListClient, store CatalogStore, holder *Holder) *Refresher {
	r := NewRefresher(client, store, holder, map[int64]struct{}{1: {}}, time.Hour)
	r.pageLimit = 2
	r.now = func() time.Time { return time.Unix(100, 0).UTC() }
	r.logf = func(string, ...any) {}
	return r
}

func TestRefresherFetchesAllPagesBeforeInstall(t *testing.T) {
	client := &fakeListClient{pages: [][]coinmarketcap.Coin{{cmcCoin(1, "0x1"), cmcCoin(2, "0x2")}, {cmcCoin(3, "0x3")}}}
	store := &fakeCatalogStore{}
	var holder Holder
	testRefresher(client, store, &holder).Bootstrap(context.Background())
	if !reflect.DeepEqual(client.starts, []int{1, 3}) || store.replaceCalls != 1 || holder.Count() != 3 {
		t.Fatalf("starts=%v replaces=%d count=%d", client.starts, store.replaceCalls, holder.Count())
	}
}

func TestRefresherPageFailurePreservesPriorAndFallsBack(t *testing.T) {
	prior := []CoinMapping{{ID: 9, PlatformID: 1, Address: "0x9", FetchedAt: time.Unix(1, 0).UTC()}}
	store := &fakeCatalogStore{mappings: prior, present: true}
	var holder Holder
	client := &fakeListClient{pages: [][]coinmarketcap.Coin{{cmcCoin(1, "0x1"), cmcCoin(2, "0x2")}}, errAt: 2}
	testRefresher(client, store, &holder).Bootstrap(context.Background())
	if store.replaceCalls != 0 || holder.Count() != 1 {
		t.Fatalf("replaces=%d count=%d", store.replaceCalls, holder.Count())
	}
}

func TestRefresherNoSourceIsNonFatal(t *testing.T) {
	var holder Holder
	testRefresher(&fakeListClient{errAt: 1}, &fakeCatalogStore{}, &holder).Bootstrap(context.Background())
	if holder.Current() != nil {
		t.Fatal("holder should remain empty")
	}
}

func TestRefreshPersistFailurePreservesPointer(t *testing.T) {
	prior := NewCatalog([]CoinMapping{{ID: 9, PlatformID: 1, Address: "0x9"}})
	var holder Holder
	holder.Set(prior)
	store := &fakeCatalogStore{replaceErr: errors.New("write")}
	r := testRefresher(&fakeListClient{pages: [][]coinmarketcap.Coin{{cmcCoin(1, "0x1")}}}, store, &holder)
	r.refresh(context.Background())
	if holder.Current() != prior {
		t.Fatal("prior catalog changed")
	}
}

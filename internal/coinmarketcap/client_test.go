package coinmarketcap

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestListCoinsPageBuildsActiveKeylessRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/public-api/v1/cryptocurrency/map" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query(); got.Get("listing_status") != "active" || got.Get("start") != "5001" || got.Get("limit") != "5000" || got.Get("aux") != "platform,is_active" {
			t.Errorf("query = %v", got)
		}
		if got := r.Header.Get("X-CMC_PRO_API_KEY"); got != "" {
			t.Errorf("unexpected API key header %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":1660,"name":"Monolith","symbol":"TKN","is_active":1,"platform":{"id":1,"token_address":"0xAa"}}],"status":{"error_code":0}}`))
	}))
	defer server.Close()

	coins, err := New(server.URL+"/public-api").ListCoinsPage(context.Background(), 5001, 5000)
	if err != nil {
		t.Fatalf("ListCoinsPage: %v", err)
	}
	if len(coins) != 1 || coins[0].ID != 1660 || coins[0].Platform == nil || coins[0].Platform.ID != 1 {
		t.Fatalf("coins = %+v", coins)
	}
}

func TestGetPricesPreservesNumbersAndRequiredQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query(); got.Get("ids") != "1,1027,1660" || got.Get("convert") != "USD" || got.Get("include_percent_change_24h") != "true" {
			t.Errorf("query = %v", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":1660,"price":0.0012300,"percent_change_24h":-1.25}],"status":{"error_code":"0"}}`))
	}))
	defer server.Close()

	prices, err := New(server.URL).GetPrices(context.Background(), []int64{1, 1027, 1660})
	if err != nil {
		t.Fatalf("GetPrices: %v", err)
	}
	if got := prices[1660].Price.String(); got != "0.0012300" {
		t.Fatalf("price = %q", got)
	}
	if got := prices[1660].PercentChange24H.String(); got != "-1.25" {
		t.Fatalf("change = %q", got)
	}
}

func TestGetPricesEmptySkipsHTTP(t *testing.T) {
	c := New("http://invalid.invalid")
	got, err := c.GetPrices(context.Background(), nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestGetPricesRetriesRetryableStatus(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"data":[],"status":{"error_code":0}}`))
	}))
	defer server.Close()
	c := New(server.URL)
	c.sleep = func(context.Context, time.Duration) error { return nil }
	if _, err := c.GetPrices(context.Background(), []int64{1}); err != nil {
		t.Fatalf("GetPrices: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestGetPricesDoesNotRetryEnvelopeError(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"data":[],"status":{"error_code":1001,"error_message":"bad request"}}`))
	}))
	defer server.Close()
	_, err := New(server.URL).GetPrices(context.Background(), []int64{1})
	if err == nil || !strings.Contains(err.Error(), "bad request") || calls.Load() != 1 {
		t.Fatalf("calls=%d err=%v", calls.Load(), err)
	}
}

func TestGetPricesCancellationStopsRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	c := New(server.URL)
	c.sleep = func(ctx context.Context, _ time.Duration) error {
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.GetPrices(ctx, []int64{1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestClientRejectsInvalidSizes(t *testing.T) {
	c := New("http://example.test")
	if _, err := c.ListCoinsPage(context.Background(), 0, 1); err == nil {
		t.Fatal("expected invalid start")
	}
	if _, err := c.ListCoinsPage(context.Background(), 1, MapPageLimit+1); err == nil {
		t.Fatal("expected invalid limit")
	}
	ids := make([]int64, PriceBatchLimit+1)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	if _, err := c.GetPrices(context.Background(), ids); err == nil {
		t.Fatal("expected oversized batch error")
	}
	if !reflect.DeepEqual(ids[:2], []int64{1, 2}) {
		t.Fatal("test setup corrupted")
	}
}

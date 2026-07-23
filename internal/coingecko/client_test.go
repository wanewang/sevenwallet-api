package coingecko

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestListCoinsSendsPlatformFlagAndUserAgent(t *testing.T) {
	var gotQuery, gotAgent, gotAccept, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("include_platform")
		gotAgent = r.Header.Get("User-Agent")
		gotAccept = r.Header.Get("Accept")
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `[{"id":"usd-coin","symbol":"usdc","name":"USDC","platforms":{"ethereum":"0xA0B8"}}]`)
	}))
	defer srv.Close()

	coins, err := New(srv.URL, "wallet-api-test/1.0").ListCoins(context.Background())
	if err != nil {
		t.Fatalf("ListCoins: %v", err)
	}
	if gotPath != "/coins/list" || gotQuery != "true" || gotAgent != "wallet-api-test/1.0" || gotAccept != "application/json" || len(coins) != 1 {
		t.Fatalf("path=%q query=%q agent=%q accept=%q coins=%+v", gotPath, gotQuery, gotAgent, gotAccept, coins)
	}
}

func TestGetPricesSendsExpectedQueryFlags(t *testing.T) {
	var gotQuery map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = map[string]string{
			"ids":                     r.URL.Query().Get("ids"),
			"vs_currencies":           r.URL.Query().Get("vs_currencies"),
			"include_24hr_change":     r.URL.Query().Get("include_24hr_change"),
			"include_last_updated_at": r.URL.Query().Get("include_last_updated_at"),
			"include_market_cap":      r.URL.Query().Get("include_market_cap"),
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "ua").GetPrices(context.Background(), []string{"ethereum", "usd-coin"}); err != nil {
		t.Fatalf("GetPrices: %v", err)
	}
	want := map[string]string{
		"ids":                     "ethereum,usd-coin",
		"vs_currencies":           "usd",
		"include_24hr_change":     "true",
		"include_last_updated_at": "true",
		"include_market_cap":      "true",
	}
	if !reflect.DeepEqual(gotQuery, want) {
		t.Fatalf("query=%v want %v", gotQuery, want)
	}
}

func TestGetPricesRetriesRetryableFailures(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 4 {
			http.Error(w, "busy", http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, `{"ethereum":{"usd":3210.45,"usd_market_cap":387000000000,"usd_24h_change":-1.23,"last_updated_at":1784781600}}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "ua")
	var waits []time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	prices, err := c.GetPrices(context.Background(), []string{"ethereum"})
	if err != nil {
		t.Fatalf("GetPrices: %v", err)
	}
	if calls != 4 || !reflect.DeepEqual(waits, []time.Duration{time.Second, time.Second, time.Second}) || prices["ethereum"].USD.String() != "3210.45" {
		t.Fatalf("calls=%d waits=%v prices=%+v", calls, waits, prices)
	}
}

func TestGetPricesRetriesHTTP500FourTimes(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "busy", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(srv.URL, "ua")
	c.sleep = func(context.Context, time.Duration) error { return nil }
	_, err := c.GetPrices(context.Background(), []string{"ethereum"})
	if err == nil || calls != 4 {
		t.Fatalf("err=%v calls=%d, want terminal error after 4 calls", err, calls)
	}
	var statusErr *StatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("err=%v, want HTTP 500 StatusError", err)
	}
}

func TestGetPricesRetriesConnectionErrorsFourTimes(t *testing.T) {
	var calls int
	c := New("http://coingecko.invalid", "ua")
	c.httpClient = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("connection refused")
	})}
	c.sleep = func(context.Context, time.Duration) error { return nil }

	_, err := c.GetPrices(context.Background(), []string{"ethereum"})
	if err == nil || calls != 4 {
		t.Fatalf("err=%v calls=%d, want terminal error after 4 calls", err, calls)
	}
	if !strings.Contains(err.Error(), "get prices") || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err=%v, want operation and cause", err)
	}
}

func TestGetPricesRetriesSuccessBodyReadErrorsFourTimes(t *testing.T) {
	var calls int
	c := New("http://coingecko.invalid", "ua")
	c.httpClient = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       failingReadCloser{err: errors.New("body read failed")},
			Header:     make(http.Header),
		}, nil
	})}
	c.sleep = func(context.Context, time.Duration) error { return nil }

	_, err := c.GetPrices(context.Background(), []string{"ethereum"})
	if err == nil || calls != 4 {
		t.Fatalf("err=%v calls=%d, want terminal error after 4 calls", err, calls)
	}
	if !strings.Contains(err.Error(), "get prices") || !strings.Contains(err.Error(), "body read failed") {
		t.Fatalf("err=%v, want operation and body read cause", err)
	}
}

func TestGetPricesRetriesDecodeErrorsFourTimes(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{`)
	}))
	defer srv.Close()

	c := New(srv.URL, "ua")
	c.sleep = func(context.Context, time.Duration) error { return nil }
	_, err := c.GetPrices(context.Background(), []string{"ethereum"})
	if err == nil || calls != 4 {
		t.Fatalf("err=%v calls=%d, want terminal error after 4 calls", err, calls)
	}
	if !strings.Contains(err.Error(), "get prices") {
		t.Fatalf("err=%v, want operation in error", err)
	}
}

func TestGetPricesDoesNotRetryHTTP400(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer srv.Close()

	c := New(srv.URL, "ua")
	c.sleep = func(context.Context, time.Duration) error { t.Fatal("sleep called for HTTP 400"); return nil }
	_, err := c.GetPrices(context.Background(), []string{"ethereum"})
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d, want one terminal call", err, calls)
	}
	var statusErr *StatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("err=%v, want HTTP 400 StatusError", err)
	}
}

func TestGetPricesDoesNotRetryRequestConstructionErrors(t *testing.T) {
	c := New("://bad-url", "ua")
	sleeps := 0
	c.sleep = func(context.Context, time.Duration) error {
		sleeps++
		return nil
	}

	_, err := c.GetPrices(context.Background(), []string{"ethereum"})
	if err == nil {
		t.Fatal("GetPrices succeeded with malformed base URL")
	}
	if sleeps != 0 {
		t.Fatalf("sleep calls=%d, want 0 for terminal request-construction error", sleeps)
	}
}

func TestGetPricesDoesNotRetryHTTP600(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "unknown status", 600)
	}))
	defer srv.Close()

	c := New(srv.URL, "ua")
	c.sleep = func(context.Context, time.Duration) error {
		t.Fatal("sleep called for HTTP 600")
		return nil
	}
	_, err := c.GetPrices(context.Background(), []string{"ethereum"})
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d, want one terminal call", err, calls)
	}
	var statusErr *StatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != 600 {
		t.Fatalf("err=%v, want HTTP 600 StatusError", err)
	}
}

func TestGetPricesReturnsCanceledRetrySleep(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "busy", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	c := New(srv.URL, "ua")
	c.sleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}
	_, err := c.GetPrices(ctx, []string{"ethereum"})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("err=%v calls=%d, want canceled sleep after one call", err, calls)
	}
}

func TestGetPricesPreservesNullableResponseFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ethereum":{"usd":null,"usd_market_cap":null,"usd_24h_change":null,"last_updated_at":null}}`)
	}))
	defer srv.Close()

	prices, err := New(srv.URL, "ua").GetPrices(context.Background(), []string{"ethereum"})
	if err != nil {
		t.Fatalf("GetPrices: %v", err)
	}
	price := prices["ethereum"]
	if price.USD != nil || price.USDMarketCap != nil || price.USD24HChange != nil || price.LastUpdatedAt != nil {
		t.Fatalf("nullable fields not preserved: %+v", price)
	}
}

func TestGetPricesWithNoIDsReturnsEmptyMap(t *testing.T) {
	prices, err := New("://bad-url", "ua").GetPrices(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetPrices: %v", err)
	}
	if prices == nil || len(prices) != 0 {
		t.Fatalf("prices=%v, want non-nil empty map", prices)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type failingReadCloser struct {
	err error
}

func (f failingReadCloser) Read([]byte) (int, error) { return 0, f.err }

func (failingReadCloser) Close() error { return nil }

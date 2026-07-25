package coingecko

import (
	"context"
	"errors"
	"fmt"
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

func TestNewAllowsCatalogResponsesUpToFifteenSeconds(t *testing.T) {
	if got := New("https://coingecko.test", "ua").httpClient.Timeout; got != 15*time.Second {
		t.Fatalf("HTTP client timeout = %v, want 15s", got)
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
	var logs []string
	prices, err := New("://bad-url", "ua", WithLogf(func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	})).GetPrices(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetPrices: %v", err)
	}
	if prices == nil || len(prices) != 0 {
		t.Fatalf("prices=%v, want non-nil empty map", prices)
	}
	if len(logs) != 0 {
		t.Fatalf("logs=%v, want no diagnostics without a provider request", logs)
	}
}

func TestListCoinsDiagnosticsContainOnlySanitizedURL(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var logs []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if status != http.StatusOK {
					w.WriteHeader(status)
					return
				}
				_, _ = io.WriteString(w, `[{"id":"usd-coin","symbol":"usdc","name":"USDC","platforms":{"ethereum":"0xA0B8"}}]`)
			}))
			defer srv.Close()
			c := New(srv.URL, "ua", WithLogf(func(format string, args ...any) {
				logs = append(logs, fmt.Sprintf(format, args...))
			}))
			_, _ = c.ListCoins(context.Background())
			want := srv.URL + "/coins/list?include_platform=true"
			if len(logs) != 1 || logs[0] != want {
				t.Fatalf("logs=%q, want only %q", logs, want)
			}
			for _, forbidden := range []string{"usd-coin", "USDC", "status", "error", "provider_api"} {
				if strings.Contains(logs[0], forbidden) {
					t.Fatalf("coin-list log contains %q: %q", forbidden, logs[0])
				}
			}
		})
	}
}

func TestGetPricesDiagnosticsCoverSuccessAndFailures(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var logs []string
		c := New("https://coingecko.test", "ua", WithLogf(func(format string, args ...any) {
			logs = append(logs, fmt.Sprintf(format, args...))
		}))
		c.httpClient = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return testResponse(http.StatusOK, `{"ethereum":{"usd":3210.45}}`), nil
		})}
		if _, err := c.GetPrices(context.Background(), []string{"ethereum"}); err != nil {
			t.Fatalf("GetPrices: %v", err)
		}
		joined := strings.Join(logs, "\n")
		for _, want := range []string{"provider=coingecko", "operation=get_prices", "connecting method=GET", "ids=ethereum", `"ethereum":{"usd":3210.45`} {
			if !strings.Contains(joined, want) {
				t.Errorf("logs missing %q: %s", want, joined)
			}
		}
	})

	t.Run("status logs each retry", func(t *testing.T) {
		var logs []string
		c := New("https://coingecko.test", "ua", WithLogf(func(format string, args ...any) {
			logs = append(logs, fmt.Sprintf(format, args...))
		}))
		c.httpClient = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return testResponse(http.StatusTooManyRequests, "busy"), nil
		})}
		c.sleep = func(context.Context, time.Duration) error { return nil }
		_, _ = c.GetPrices(context.Background(), []string{"ethereum"})
		joined := strings.Join(logs, "\n")
		if got := strings.Count(joined, "operation=get_prices connecting"); got != maxPriceAttempts {
			t.Fatalf("connection logs=%d, want %d: %s", got, maxPriceAttempts, joined)
		}
		if got := strings.Count(joined, "failure status=429"); got != maxPriceAttempts {
			t.Fatalf("status logs=%d, want %d: %s", got, maxPriceAttempts, joined)
		}
	})

	t.Run("transport", func(t *testing.T) {
		var logs []string
		c := New("https://coingecko.test", "ua", WithLogf(func(format string, args ...any) {
			logs = append(logs, fmt.Sprintf(format, args...))
		}))
		c.httpClient = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		})}
		c.sleep = func(context.Context, time.Duration) error { return nil }
		_, _ = c.GetPrices(context.Background(), []string{"ethereum"})
		if joined := strings.Join(logs, "\n"); !strings.Contains(joined, "failure stage=transport") {
			t.Fatalf("logs = %s", joined)
		}
	})

	t.Run("decode", func(t *testing.T) {
		var logs []string
		c := New("https://coingecko.test", "ua", WithLogf(func(format string, args ...any) {
			logs = append(logs, fmt.Sprintf(format, args...))
		}))
		c.httpClient = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return testResponse(http.StatusOK, `{`), nil
		})}
		c.sleep = func(context.Context, time.Duration) error { return nil }
		_, _ = c.GetPrices(context.Background(), []string{"ethereum"})
		if joined := strings.Join(logs, "\n"); !strings.Contains(joined, "failure stage=decode") {
			t.Fatalf("logs = %s", joined)
		}
	})
}

func TestDiagnosticsPreserveCoinGeckoRequestsRetriesResultsAndErrors(t *testing.T) {
	type snapshot struct {
		method string
		url    string
		accept string
		agent  string
	}
	run := func(enabled bool, status int) ([]snapshot, []time.Duration, map[string]SimplePrice, error) {
		var requests []snapshot
		var opts []Option
		if enabled {
			opts = append(opts, WithLogf(func(string, ...any) {}))
		}
		c := New("https://coingecko.test/api/v3", "wallet-api-test", opts...)
		c.httpClient = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			requests = append(requests, snapshot{r.Method, r.URL.String(), r.Header.Get("Accept"), r.Header.Get("User-Agent")})
			if status != http.StatusOK {
				return testResponse(status, "busy"), nil
			}
			return testResponse(http.StatusOK, `{"ethereum":{"usd":3210.45}}`), nil
		})}
		var waits []time.Duration
		c.sleep = func(_ context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			return nil
		}
		prices, err := c.GetPrices(context.Background(), []string{"ethereum"})
		return requests, waits, prices, err
	}

	disabledRequests, disabledWaits, disabledValues, disabledErr := run(false, http.StatusOK)
	enabledRequests, enabledWaits, enabledValues, enabledErr := run(true, http.StatusOK)
	if !reflect.DeepEqual(disabledRequests, enabledRequests) || !reflect.DeepEqual(disabledWaits, enabledWaits) || !reflect.DeepEqual(disabledValues, enabledValues) || disabledErr != nil || enabledErr != nil {
		t.Fatalf("success differs: disabled=(%+v,%v,%+v,%v) enabled=(%+v,%v,%+v,%v)", disabledRequests, disabledWaits, disabledValues, disabledErr, enabledRequests, enabledWaits, enabledValues, enabledErr)
	}

	disabledRequests, disabledWaits, _, disabledErr = run(false, http.StatusInternalServerError)
	enabledRequests, enabledWaits, _, enabledErr = run(true, http.StatusInternalServerError)
	if !reflect.DeepEqual(disabledRequests, enabledRequests) || !reflect.DeepEqual(disabledWaits, enabledWaits) || len(disabledRequests) != maxPriceAttempts || disabledErr == nil || enabledErr == nil || disabledErr.Error() != enabledErr.Error() {
		t.Fatalf("failure differs: disabled=(%+v,%v,%v) enabled=(%+v,%v,%v)", disabledRequests, disabledWaits, disabledErr, enabledRequests, enabledWaits, enabledErr)
	}
}

func TestNilCoinGeckoDiagnosticHookIsNoOp(t *testing.T) {
	(&Client{}).diagnosticf("ignored %s", "message")
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func testResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

type failingReadCloser struct {
	err error
}

func (f failingReadCloser) Read([]byte) (int, error) { return 0, f.err }

func (failingReadCloser) Close() error { return nil }

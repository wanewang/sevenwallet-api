package coingecko

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"wallet-api/internal/providerlog"
)

const (
	maxPriceAttempts = 4
	retryDelay       = time.Second
	clientTimeout    = 15 * time.Second
)

// Client calls CoinGecko's catalog and simple-price APIs.
type Client struct {
	baseURL    string
	userAgent  string
	httpClient *http.Client
	sleep      func(context.Context, time.Duration) error
	logf       func(string, ...any)
}

// Option configures a CoinGecko Client.
type Option func(*Client)

// WithLogf enables provider diagnostics using logf. A nil hook is a no-op.
func WithLogf(logf func(string, ...any)) Option {
	return func(c *Client) { c.logf = logf }
}

type retryableError struct{ err error }

func (e *retryableError) Error() string { return e.err.Error() }

func (e *retryableError) Unwrap() error { return e.err }

// New builds a Client for the configured CoinGecko API base URL.
func New(baseURL, userAgent string, opts ...Option) *Client {
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		userAgent:  userAgent,
		httpClient: &http.Client{Timeout: clientTimeout},
		sleep:      sleep,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

func (c *Client) diagnosticf(format string, args ...any) {
	if c != nil && c.logf != nil {
		c.logf(format, args...)
	}
}

// ListCoins fetches CoinGecko's complete coin catalog with platform addresses.
func (c *Client) ListCoins(ctx context.Context) ([]Coin, error) {
	var coins []Coin
	query := url.Values{"include_platform": []string{"true"}}
	if err := c.getJSON(ctx, "list_coins", "/coins/list", query, &coins, true); err != nil {
		return nil, err
	}
	return coins, nil
}

// GetPrices fetches USD prices and market fields for the requested CoinGecko IDs.
func (c *Client) GetPrices(ctx context.Context, ids []string) (map[string]SimplePrice, error) {
	if len(ids) == 0 {
		return map[string]SimplePrice{}, nil
	}

	query := url.Values{
		"ids":                     []string{strings.Join(ids, ",")},
		"vs_currencies":           []string{"usd"},
		"include_24hr_change":     []string{"true"},
		"include_last_updated_at": []string{"true"},
		"include_market_cap":      []string{"true"},
	}
	var lastErr error
	for attempt := 1; attempt <= maxPriceAttempts; attempt++ {
		var prices map[string]SimplePrice
		err := c.getJSON(ctx, "get_prices", "/simple/price", query, &prices, false)
		if err == nil {
			return prices, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt == maxPriceAttempts || !retryable(err) {
			return nil, err
		}
		if err := c.sleep(ctx, retryDelay); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *Client) getJSON(ctx context.Context, operation, path string, query url.Values, out any, urlOnly bool) error {
	req, err := c.newRequest(ctx, path, query)
	if err != nil {
		wrapped := fmt.Errorf("coingecko %s request: %w", strings.ReplaceAll(operation, "_", " "), err)
		if !urlOnly {
			c.diagnosticError(operation, "request", wrapped)
		}
		return wrapped
	}
	if urlOnly {
		c.diagnosticf("%s", providerlog.URL(req.URL.String()))
	} else {
		c.diagnosticf(
			"provider_api provider=coingecko operation=%s connecting method=%s query=%q",
			operation,
			http.MethodGet,
			query.Encode(),
		)
	}
	res, err := c.httpClient.Do(req)
	if err != nil {
		wrapped := fmt.Errorf("coingecko %s request failed: %w", strings.ReplaceAll(operation, "_", " "), &retryableError{err: err})
		if !urlOnly {
			c.diagnosticError(operation, "transport", wrapped)
		}
		return wrapped
	}
	defer res.Body.Close()
	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, res.Body)
		wrapped := fmt.Errorf("coingecko %s: %w", strings.ReplaceAll(operation, "_", " "), &StatusError{StatusCode: res.StatusCode})
		if !urlOnly {
			c.diagnosticf("provider_api provider=coingecko operation=%s failure status=%d", operation, res.StatusCode)
		}
		return wrapped
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		wrapped := fmt.Errorf("coingecko %s response: %w", strings.ReplaceAll(operation, "_", " "), &retryableError{err: err})
		if !urlOnly {
			c.diagnosticError(operation, "decode", wrapped)
		}
		return wrapped
	}
	if !urlOnly {
		c.diagnosticf("provider_api provider=coingecko operation=%s result=%s", operation, providerlog.JSON(out))
	}
	return nil
}

func (c *Client) diagnosticError(operation, stage string, err error) {
	c.diagnosticf(
		"provider_api provider=coingecko operation=%s failure stage=%s error=%q",
		operation,
		stage,
		providerlog.Redact(err.Error()),
	)
}

func (c *Client) newRequest(ctx context.Context, path string, query url.Values) (*http.Request, error) {
	endpoint, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, err
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + path
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	return req, nil
}

func retryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var statusErr *StatusError
	if errors.As(err, &statusErr) {
		return statusErr.StatusCode == http.StatusTooManyRequests || (statusErr.StatusCode >= 500 && statusErr.StatusCode <= 599)
	}
	var retryErr *retryableError
	return errors.As(err, &retryErr)
}

func sleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

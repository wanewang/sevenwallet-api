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
}

type retryableError struct{ err error }

func (e *retryableError) Error() string { return e.err.Error() }

func (e *retryableError) Unwrap() error { return e.err }

// New builds a Client for the configured CoinGecko API base URL.
func New(baseURL, userAgent string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		userAgent:  userAgent,
		httpClient: &http.Client{Timeout: clientTimeout},
		sleep:      sleep,
	}
}

// ListCoins fetches CoinGecko's complete coin catalog with platform addresses.
func (c *Client) ListCoins(ctx context.Context) ([]Coin, error) {
	var coins []Coin
	query := url.Values{"include_platform": []string{"true"}}
	if err := c.getJSON(ctx, "list coins", "/coins/list", query, &coins); err != nil {
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
		err := c.getJSON(ctx, "get prices", "/simple/price", query, &prices)
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

func (c *Client) getJSON(ctx context.Context, operation, path string, query url.Values, out any) error {
	req, err := c.newRequest(ctx, path, query)
	if err != nil {
		return fmt.Errorf("coingecko %s request: %w", operation, err)
	}
	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("coingecko %s request failed: %w", operation, &retryableError{err: err})
	}
	defer res.Body.Close()
	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, res.Body)
		return fmt.Errorf("coingecko %s: %w", operation, &StatusError{StatusCode: res.StatusCode})
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("coingecko %s response: %w", operation, &retryableError{err: err})
	}
	return nil
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

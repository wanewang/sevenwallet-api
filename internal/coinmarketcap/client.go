// Package coinmarketcap calls CoinMarketCap's keyless catalog and price APIs.
package coinmarketcap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	MapPageLimit    = 5000
	PriceBatchLimit = 50
	maxAttempts     = 4
	retryDelay      = time.Second
	clientTimeout   = 15 * time.Second
)

// Client calls CoinMarketCap without credentials.
type Client struct {
	baseURL    string
	httpClient *http.Client
	sleep      func(context.Context, time.Duration) error
}

// New builds a keyless CoinMarketCap client.
func New(baseURL string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: clientTimeout},
		sleep:      sleepContext,
	}
}

type mapEnvelope struct {
	Data   []Coin    `json:"data"`
	Status apiStatus `json:"status"`
}

type priceEnvelope struct {
	Data   []SimplePrice `json:"data"`
	Status apiStatus     `json:"status"`
}

// ListCoinsPage fetches one active cryptocurrency-map page.
func (c *Client) ListCoinsPage(ctx context.Context, start, limit int) ([]Coin, error) {
	if start < 1 {
		return nil, fmt.Errorf("coinmarketcap map start must be positive")
	}
	if limit < 1 || limit > MapPageLimit {
		return nil, fmt.Errorf("coinmarketcap map limit must be between 1 and %d", MapPageLimit)
	}
	query := url.Values{
		"listing_status": []string{"active"},
		"start":          []string{strconv.Itoa(start)},
		"limit":          []string{strconv.Itoa(limit)},
		"aux":            []string{"platform,is_active"},
	}
	var envelope mapEnvelope
	if err := c.getJSON(ctx, "/v1/cryptocurrency/map", query, &envelope); err != nil {
		return nil, fmt.Errorf("coinmarketcap cryptocurrency map: %w", err)
	}
	if err := envelopeError(envelope.Status); err != nil {
		return nil, fmt.Errorf("coinmarketcap cryptocurrency map: %w", err)
	}
	return envelope.Data, nil
}

// GetPrices fetches USD price and 24-hour change for up to 50 CMC IDs.
func (c *Client) GetPrices(ctx context.Context, ids []int64) (map[int64]SimplePrice, error) {
	result := make(map[int64]SimplePrice, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	if len(ids) > PriceBatchLimit {
		return nil, fmt.Errorf("coinmarketcap price batch exceeds %d IDs", PriceBatchLimit)
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("coinmarketcap ID must be positive")
		}
		parts[i] = strconv.FormatInt(id, 10)
	}
	query := url.Values{
		"ids":                        []string{strings.Join(parts, ",")},
		"convert":                    []string{"USD"},
		"include_percent_change_24h": []string{"true"},
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		var envelope priceEnvelope
		err := c.getJSON(ctx, "/v1/simple/price", query, &envelope)
		if err == nil {
			err = envelopeError(envelope.Status)
		}
		if err == nil {
			for _, price := range envelope.Data {
				result[price.ID] = price
			}
			return result, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt == maxAttempts || !retryable(err) {
			return nil, fmt.Errorf("coinmarketcap simple price: %w", err)
		}
		if err := c.sleep(ctx, retryDelay); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

type retryableError struct{ err error }

func (e *retryableError) Error() string { return e.err.Error() }
func (e *retryableError) Unwrap() error { return e.err }

func (c *Client) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	req, err := c.newRequest(ctx, path, query)
	if err != nil {
		return err
	}
	res, err := c.httpClient.Do(req)
	if err != nil {
		return &retryableError{err: err}
	}
	defer res.Body.Close()
	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, res.Body)
		return &StatusError{StatusCode: res.StatusCode}
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return &retryableError{err: err}
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
	req.Header.Set("User-Agent", "wallet-api/1.0")
	return req, nil
}

func envelopeError(status apiStatus) error {
	if len(status.ErrorCode) == 0 || string(status.ErrorCode) == "null" {
		return nil
	}
	raw := strings.Trim(string(status.ErrorCode), `"`)
	code, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid CoinMarketCap error code %q", raw)
	}
	if code == 0 {
		return nil
	}
	message := ""
	if status.ErrorMessage != nil {
		message = *status.ErrorMessage
	}
	return &StatusError{ErrorCode: code, Message: message}
}

func retryable(err error) bool {
	var statusErr *StatusError
	if errors.As(err, &statusErr) {
		return statusErr.StatusCode == http.StatusTooManyRequests || statusErr.StatusCode >= 500
	}
	var transportErr *retryableError
	return errors.As(err, &transportErr)
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

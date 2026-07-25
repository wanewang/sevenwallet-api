package moralis

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
)

func testClient(srv *httptest.Server) *Client {
	return &Client{apiKey: "k", chain: "eth", baseURL: srv.URL, httpClient: srv.Client()}
}

func TestGetTokenMetadataParsesFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-API-Key"); got != "k" {
			t.Errorf("X-API-Key = %q, want k", got)
		}
		if got := r.URL.Query().Get("chain"); got != "eth" {
			t.Errorf("chain = %q, want eth", got)
		}
		if got := r.URL.Query().Get("addresses[]"); got != "0xABC" {
			t.Errorf("addresses[] = %q, want 0xABC", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"symbol":"PEPE","name":"Pepe","logo":"https://logo/pepe.png","decimals":"18","possible_spam":false,"verified_contract":true}]`))
	}))
	defer srv.Close()

	m, err := testClient(srv).GetTokenMetadata(context.Background(), "0xABC")
	if err != nil {
		t.Fatalf("GetTokenMetadata: %v", err)
	}
	want := Metadata{Symbol: "PEPE", Name: "Pepe", Logo: "https://logo/pepe.png", Decimals: 18, PossibleSpam: false, VerifiedContract: true}
	if m != want {
		t.Errorf("got %+v, want %+v", m, want)
	}
}

func TestGetTokenMetadataNullLogo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"symbol":"NEW","name":"NewToken","logo":null,"decimals":"6","possible_spam":true,"verified_contract":false}]`))
	}))
	defer srv.Close()

	m, err := testClient(srv).GetTokenMetadata(context.Background(), "0xABC")
	if err != nil {
		t.Fatalf("GetTokenMetadata: %v", err)
	}
	want := Metadata{Symbol: "NEW", Name: "NewToken", Logo: "", Decimals: 6, PossibleSpam: true, VerifiedContract: false}
	if m != want {
		t.Errorf("got %+v, want %+v", m, want)
	}
}

func TestGetTokenMetadataEmptyArrayIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	if _, err := testClient(srv).GetTokenMetadata(context.Background(), "0xABC"); err == nil {
		t.Fatal("expected error for empty array")
	}
}

func TestGetTokenMetadataNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	if _, err := testClient(srv).GetTokenMetadata(context.Background(), "0xABC"); err == nil {
		t.Fatal("expected error for 429")
	}
}

func TestGetTokenMetadataDiagnosticsCoverSuccessAndFailures(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var logs []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `[{"symbol":"USDC","name":"USD Coin","logo":"logo","decimals":"6","possible_spam":false,"verified_contract":true}]`)
		}))
		defer srv.Close()
		c := New("moralis-secret", "eth", WithLogf(func(format string, args ...any) {
			logs = append(logs, fmt.Sprintf(format, args...))
		}))
		c.baseURL = srv.URL
		if _, err := c.GetTokenMetadata(context.Background(), "0xABC"); err != nil {
			t.Fatalf("GetTokenMetadata: %v", err)
		}
		joined := strings.Join(logs, "\n")
		for _, want := range []string{"provider=moralis", "operation=get_token_metadata", "connecting method=GET", `chain="eth"`, `address="0xABC"`, `"Symbol":"USDC"`} {
			if !strings.Contains(joined, want) {
				t.Errorf("logs missing %q: %s", want, joined)
			}
		}
		if strings.Contains(joined, "moralis-secret") || strings.Contains(joined, "X-API-Key") {
			t.Fatalf("logs leaked API credential: %s", joined)
		}
	})

	t.Run("status", func(t *testing.T) {
		var logs []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer srv.Close()
		c := New("key", "eth", WithLogf(func(format string, args ...any) {
			logs = append(logs, fmt.Sprintf(format, args...))
		}))
		c.baseURL = srv.URL
		_, _ = c.GetTokenMetadata(context.Background(), "0xABC")
		if joined := strings.Join(logs, "\n"); !strings.Contains(joined, "failure status=429") {
			t.Fatalf("logs = %s", joined)
		}
	})

	t.Run("decode", func(t *testing.T) {
		var logs []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{`)
		}))
		defer srv.Close()
		c := New("key", "eth", WithLogf(func(format string, args ...any) {
			logs = append(logs, fmt.Sprintf(format, args...))
		}))
		c.baseURL = srv.URL
		_, _ = c.GetTokenMetadata(context.Background(), "0xABC")
		if joined := strings.Join(logs, "\n"); !strings.Contains(joined, "failure stage=decode") {
			t.Fatalf("logs = %s", joined)
		}
	})

	t.Run("transport redacts credential", func(t *testing.T) {
		var logs []string
		c := New("moralis-secret", "eth", WithLogf(func(format string, args ...any) {
			logs = append(logs, fmt.Sprintf(format, args...))
		}))
		c.httpClient = &http.Client{Transport: moralisRoundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("request with moralis-secret failed")
		})}
		_, _ = c.GetTokenMetadata(context.Background(), "0xABC")
		joined := strings.Join(logs, "\n")
		if !strings.Contains(joined, "failure stage=transport") || !strings.Contains(joined, "[REDACTED]") {
			t.Fatalf("logs = %s", joined)
		}
		if strings.Contains(joined, "moralis-secret") || strings.Contains(joined, "X-API-Key") {
			t.Fatalf("logs leaked API credential: %s", joined)
		}
	})
}

func TestDiagnosticsPreserveMoralisRequestResultsAndErrors(t *testing.T) {
	type snapshot struct {
		method string
		path   string
		query  string
		accept string
		apiKey string
	}
	run := func(t *testing.T, enabled bool, status int) (snapshot, Metadata, error) {
		t.Helper()
		var got snapshot
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = snapshot{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Accept"), r.Header.Get("X-API-Key")}
			if status != http.StatusOK {
				w.WriteHeader(status)
				return
			}
			_, _ = io.WriteString(w, `[{"symbol":"USDC","name":"USD Coin","logo":"logo","decimals":"6","possible_spam":false,"verified_contract":true}]`)
		}))
		defer srv.Close()
		var opts []Option
		if enabled {
			opts = append(opts, WithLogf(func(string, ...any) {}))
		}
		c := New("key", "eth", opts...)
		c.baseURL = srv.URL
		metadata, err := c.GetTokenMetadata(context.Background(), "0xABC")
		return got, metadata, err
	}

	disabledRequest, disabledValue, disabledErr := run(t, false, http.StatusOK)
	enabledRequest, enabledValue, enabledErr := run(t, true, http.StatusOK)
	if !reflect.DeepEqual(disabledRequest, enabledRequest) || disabledValue != enabledValue || disabledErr != nil || enabledErr != nil {
		t.Fatalf("success differs: disabled=(%+v,%+v,%v) enabled=(%+v,%+v,%v)", disabledRequest, disabledValue, disabledErr, enabledRequest, enabledValue, enabledErr)
	}

	disabledRequest, _, disabledErr = run(t, false, http.StatusBadGateway)
	enabledRequest, _, enabledErr = run(t, true, http.StatusBadGateway)
	if !reflect.DeepEqual(disabledRequest, enabledRequest) || disabledErr == nil || enabledErr == nil || disabledErr.Error() != enabledErr.Error() {
		t.Fatalf("failure differs: disabled=(%+v,%v) enabled=(%+v,%v)", disabledRequest, disabledErr, enabledRequest, enabledErr)
	}
}

func TestNilMoralisDiagnosticHookIsNoOp(t *testing.T) {
	(&Client{}).diagnosticf("ignored %s", "message")
}

type moralisRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f moralisRoundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

package moralis

import (
	"context"
	"net/http"
	"net/http/httptest"
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

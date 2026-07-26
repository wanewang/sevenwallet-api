package cmcmarket

import (
	"encoding/json"
	"testing"
	"time"

	"wallet-api/internal/marketdata"
)

func TestRecordFreshnessAndExactJSONPrice(t *testing.T) {
	now := time.Unix(2_000, 0).UTC()
	price := "1e-400"
	r := Record{Key: marketdata.ContractKey("ethereum", "0xABC"), CoinMarketCapID: 1660, PriceUSD: &price, FetchedAt: now.Add(-20 * time.Minute)}
	if !r.Fresh(now, 30*time.Minute) || r.RemainingTTL(now, 30*time.Minute) != 10*time.Minute {
		t.Fatal("unexpected freshness")
	}
	if r.Fresh(now, 20*time.Minute) {
		t.Fatal("exact boundary must be stale")
	}
	b, err := json.Marshal(r)
	if err != nil || !json.Valid(b) {
		t.Fatalf("marshal = %s err=%v", b, err)
	}
	var decoded Record
	if err := json.Unmarshal(b, &decoded); err != nil || decoded.PriceUSD == nil || *decoded.PriceUSD != price || decoded.CoinMarketCapID != 1660 {
		t.Fatalf("decoded = %#v err=%v", decoded, err)
	}
}

package marketdata

import (
	"testing"
	"time"
)

func TestRecordFreshnessAndRemainingTTL(t *testing.T) {
	now := time.Unix(2_000, 0).UTC()
	record := Record{FetchedAt: now.Add(-20 * time.Minute)}
	if !record.Fresh(now, 30*time.Minute) || record.RemainingTTL(now, 30*time.Minute) != 10*time.Minute {
		t.Fatalf("unexpected fresh record result")
	}
	if record.Fresh(now, 20*time.Minute) || record.RemainingTTL(now, 20*time.Minute) != 0 {
		t.Fatal("exact boundary must be stale")
	}
}

func TestKeysNormalizeDefinedParts(t *testing.T) {
	if got := ContractKey("Ethereum", "0xA0B8"); got != (Key{Chain: "ethereum", TokenKey: "0xa0b8"}) {
		t.Fatalf("contract key = %#v", got)
	}
	if got := NativeKey("Ethereum", "eth"); got != (Key{Chain: "ethereum", TokenKey: "native:ETH"}) {
		t.Fatalf("native key = %#v", got)
	}
}

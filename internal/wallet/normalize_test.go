package wallet

import "testing"

func TestNormalizeAddress(t *testing.T) {
	if got := NormalizeAddress("  0xAbC123  "); got != "0xabc123" {
		t.Errorf("got %q, want 0xabc123", got)
	}
}

func TestScaleBalance(t *testing.T) {
	cases := []struct {
		raw     string
		dec     int
		rawDec  string
		scaled  string
	}{
		{"0x16345785d8a0000", 18, "100000000000000000", "0.1"},
		{"1500000000000000000", 18, "1500000000000000000", "1.5"},
		{"12500000", 6, "12500000", "12.5"},
		{"1000000", 6, "1000000", "1"},
		{"0x0", 18, "0", "0"},
		{"", 18, "0", "0"},
		{"1", 18, "1", "0.000000000000000001"},
		{"1000000000000000001", 18, "1000000000000000001", "1.000000000000000001"},
		{"1000", 0, "1000", "1000"},
	}
	for _, c := range cases {
		rawDec, scaled, err := ScaleBalance(c.raw, c.dec)
		if err != nil {
			t.Fatalf("ScaleBalance(%q,%d) error: %v", c.raw, c.dec, err)
		}
		if rawDec != c.rawDec || scaled != c.scaled {
			t.Errorf("ScaleBalance(%q,%d) = (%q,%q), want (%q,%q)", c.raw, c.dec, rawDec, scaled, c.rawDec, c.scaled)
		}
	}
}

func TestScaleBalanceInvalid(t *testing.T) {
	if _, _, err := ScaleBalance("not-a-number", 18); err == nil {
		t.Fatal("expected error for invalid input")
	}
}

func TestScaleBalanceNegative(t *testing.T) {
	if _, _, err := ScaleBalance("-5", 18); err == nil {
		t.Fatal("expected error for negative balance")
	}
}

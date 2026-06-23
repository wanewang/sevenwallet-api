package wallet

import (
	"fmt"
	"math/big"
	"strings"
)

// NormalizeAddress trims whitespace and lowercases an address for storage/lookup.
func NormalizeAddress(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// ScaleBalance parses raw (a decimal or 0x-hex unsigned integer string) and
// returns its decimal-string form and the value divided by 10^decimals as a
// decimal string. Both results avoid floating point to preserve precision.
func ScaleBalance(raw string, decimals int) (string, string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		s = "0"
	}
	// base 0 auto-detects the 0x prefix; otherwise parses base 10.
	n, ok := new(big.Int).SetString(s, 0)
	if !ok {
		return "", "", fmt.Errorf("invalid balance %q", raw)
	}
	rawDecimal := n.String()
	if decimals <= 0 {
		return rawDecimal, rawDecimal, nil
	}
	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	q := new(big.Int)
	r := new(big.Int)
	q.DivMod(n, divisor, r)
	if r.Sign() == 0 {
		return rawDecimal, q.String(), nil
	}
	// Left-pad the remainder to `decimals` digits, then trim trailing zeros.
	frac := fmt.Sprintf("%0*s", decimals, r.String())
	frac = strings.TrimRight(frac, "0")
	return rawDecimal, q.String() + "." + frac, nil
}

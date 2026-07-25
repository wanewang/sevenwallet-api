// Package providerlog contains pure formatting helpers for provider diagnostics.
package providerlog

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

const redacted = "[REDACTED]"

// JSON formats a decoded provider result without returning an error to the
// provider control flow. Known secrets are removed from either representation.
func JSON(value any, secrets ...string) string {
	b, err := json.Marshal(value)
	if err != nil {
		return Redact(fmt.Sprintf("<result unavailable: %v>", err), secrets...)
	}
	return Redact(string(b), secrets...)
}

// Redact replaces every non-empty known secret in text.
func Redact(text string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, redacted)
		}
	}
	return text
}

// URL removes user information and values for credential-like query keys from
// a URL before it is written to diagnostics. Known secrets are also redacted.
func URL(raw string, secrets ...string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return Redact(raw, secrets...)
	}
	u.User = nil
	query := u.Query()
	for key := range query {
		if credentialKey(key) {
			query.Set(key, redacted)
		}
	}
	u.RawQuery = query.Encode()
	return Redact(u.String(), secrets...)
}

func credentialKey(key string) bool {
	normalized := strings.ToLower(key)
	for _, part := range []string{"api_key", "apikey", "authorization", "password", "secret", "signature", "token"} {
		if strings.Contains(normalized, part) {
			return true
		}
	}
	return false
}

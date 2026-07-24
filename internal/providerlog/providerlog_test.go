package providerlog

import (
	"errors"
	"strings"
	"testing"
)

func TestJSONRedactsKnownSecrets(t *testing.T) {
	got := JSON(map[string]string{"value": "before-secret-after"}, "secret")
	if strings.Contains(got, "secret") || !strings.Contains(got, redacted) {
		t.Fatalf("JSON() = %q, want redacted output", got)
	}
}

func TestJSONFormattingFailureDoesNotEscape(t *testing.T) {
	got := JSON(make(chan int))
	if !strings.Contains(got, "result unavailable") {
		t.Fatalf("JSON() = %q, want unavailable marker", got)
	}
}

func TestRedactIgnoresEmptySecrets(t *testing.T) {
	if got := Redact("unchanged", ""); got != "unchanged" {
		t.Fatalf("Redact() = %q", got)
	}
	if got := Redact(errors.New("contains key").Error(), "key"); got != "contains "+redacted {
		t.Fatalf("Redact() = %q", got)
	}
}

func TestURLRemovesCredentials(t *testing.T) {
	got := URL("https://user:password@example.test/coins/list?include_platform=true&api_key=secret", "secret")
	if strings.Contains(got, "user") || strings.Contains(got, "password") || strings.Contains(got, "secret") {
		t.Fatalf("URL() leaked credentials: %q", got)
	}
	if !strings.Contains(got, "include_platform=true") || !strings.Contains(got, "api_key=%5BREDACTED%5D") {
		t.Fatalf("URL() = %q, want safe query retained and credential redacted", got)
	}
}

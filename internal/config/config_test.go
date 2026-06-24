package config

import (
	"testing"
	"time"
)

func TestLoadFromAppliesDefaults(t *testing.T) {
	env := map[string]string{
		"ALCHEMY_API_KEY": "key123",
		"DATABASE_URL":    "postgres://localhost:5432/wallet",
	}
	cfg, err := loadFrom(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AlchemyNetwork != "eth-mainnet" {
		t.Errorf("network = %q, want eth-mainnet", cfg.AlchemyNetwork)
	}
	if cfg.CacheTTL != 300*time.Second {
		t.Errorf("ttl = %v, want 300s", cfg.CacheTTL)
	}
	if cfg.Port != "8080" {
		t.Errorf("port = %q, want 8080", cfg.Port)
	}
}

func TestLoadFromHonoursOverrides(t *testing.T) {
	env := map[string]string{
		"ALCHEMY_API_KEY":   "key123",
		"DATABASE_URL":      "postgres://db",
		"ALCHEMY_NETWORK":   "eth-sepolia",
		"CACHE_TTL_SECONDS": "30",
		"PORT":              "9000",
	}
	cfg, err := loadFrom(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AlchemyNetwork != "eth-sepolia" || cfg.CacheTTL != 30*time.Second || cfg.Port != "9000" {
		t.Errorf("overrides not applied: %+v", cfg)
	}
}

func TestLoadFromRequiresKeyAndDB(t *testing.T) {
	// All empty: should fail on ALCHEMY_API_KEY
	if _, err := loadFrom(func(string) string { return "" }); err == nil {
		t.Fatal("expected error when ALCHEMY_API_KEY/DATABASE_URL missing")
	}

	// Only ALCHEMY_API_KEY set: should fail on DATABASE_URL
	env := map[string]string{
		"ALCHEMY_API_KEY": "key123",
	}
	if _, err := loadFrom(func(k string) string { return env[k] }); err == nil {
		t.Fatal("expected error when DATABASE_URL missing")
	}
}

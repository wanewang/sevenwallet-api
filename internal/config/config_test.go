package config

import (
	"testing"
	"time"
)

func TestLoadFromAppliesDefaults(t *testing.T) {
	env := map[string]string{
		"ALCHEMY_API_KEY": "key123",
		"DATABASE_URL":    "postgres://localhost:5432/wallet",
		"REDIS_URL":       "redis://localhost:6379/0",
		"MORALIS_API_KEY": "mkey",
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
	if cfg.LifiTokensURL != "https://li.quest/v1/tokens" {
		t.Errorf("lifi url = %q, want default", cfg.LifiTokensURL)
	}
	if cfg.LifiChain != "ETH" {
		t.Errorf("lifi chain = %q, want ETH", cfg.LifiChain)
	}
	if cfg.LifiRefresh != 3600*time.Second {
		t.Errorf("lifi refresh = %v, want 3600s", cfg.LifiRefresh)
	}
	if cfg.RedisURL != "redis://localhost:6379/0" {
		t.Errorf("redis url = %q, want redis://localhost:6379/0", cfg.RedisURL)
	}
	if cfg.MoralisChain != "eth" {
		t.Errorf("moralis chain = %q, want eth", cfg.MoralisChain)
	}
	if cfg.MoralisRecheck != 604800*time.Second {
		t.Errorf("moralis recheck = %v, want 604800s", cfg.MoralisRecheck)
	}
	if cfg.MoralisRedisTTL != 86400*time.Second {
		t.Errorf("moralis redis ttl = %v, want 86400s", cfg.MoralisRedisTTL)
	}
}

func TestLoadFromHonoursOverrides(t *testing.T) {
	env := map[string]string{
		"ALCHEMY_API_KEY":           "key123",
		"DATABASE_URL":              "postgres://db",
		"REDIS_URL":                 "redis://cache:6379/1",
		"ALCHEMY_NETWORK":           "eth-sepolia",
		"CACHE_TTL_SECONDS":         "30",
		"PORT":                      "9000",
		"LIFI_TOKENS_URL":           "http://localhost:9999/v1/tokens",
		"LIFI_CHAIN":                "DAI",
		"LIFI_REFRESH_SECONDS":      "60",
		"MORALIS_API_KEY":           "mkey",
		"MORALIS_CHAIN":             "polygon",
		"MORALIS_RECHECK_SECONDS":   "120",
		"MORALIS_REDIS_TTL_SECONDS": "60",
	}
	cfg, err := loadFrom(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AlchemyNetwork != "eth-sepolia" || cfg.CacheTTL != 30*time.Second || cfg.Port != "9000" {
		t.Errorf("overrides not applied: %+v", cfg)
	}
	if cfg.RedisURL != "redis://cache:6379/1" {
		t.Errorf("redis url = %q", cfg.RedisURL)
	}
	if cfg.LifiTokensURL != "http://localhost:9999/v1/tokens" || cfg.LifiChain != "DAI" || cfg.LifiRefresh != 60*time.Second {
		t.Errorf("lifi overrides not applied: %+v", cfg)
	}
	if cfg.MoralisChain != "polygon" || cfg.MoralisRecheck != 120*time.Second || cfg.MoralisRedisTTL != 60*time.Second {
		t.Errorf("moralis overrides not applied: %+v", cfg)
	}
}

func TestLoadFromRequiresKeyAndDB(t *testing.T) {
	if _, err := loadFrom(func(string) string { return "" }); err == nil {
		t.Fatal("expected error when ALCHEMY_API_KEY/DATABASE_URL missing")
	}
	env := map[string]string{"ALCHEMY_API_KEY": "key123"}
	if _, err := loadFrom(func(k string) string { return env[k] }); err == nil {
		t.Fatal("expected error when DATABASE_URL missing")
	}
}

func TestLoadFromRequiresRedisURL(t *testing.T) {
	env := map[string]string{
		"ALCHEMY_API_KEY": "key123",
		"DATABASE_URL":    "postgres://db",
	}
	if _, err := loadFrom(func(k string) string { return env[k] }); err == nil {
		t.Fatal("expected error when REDIS_URL missing")
	}
}

func TestLoadFromRejectsBadRefresh(t *testing.T) {
	env := map[string]string{
		"ALCHEMY_API_KEY":      "key123",
		"DATABASE_URL":         "postgres://db",
		"REDIS_URL":            "redis://localhost:6379",
		"MORALIS_API_KEY":      "mkey",
		"LIFI_REFRESH_SECONDS": "0",
	}
	if _, err := loadFrom(func(k string) string { return env[k] }); err == nil {
		t.Fatal("expected error for non-positive LIFI_REFRESH_SECONDS")
	}
}

func TestLoadFromRejectsNonIntegerRefresh(t *testing.T) {
	env := map[string]string{
		"ALCHEMY_API_KEY":      "key123",
		"DATABASE_URL":         "postgres://db",
		"REDIS_URL":            "redis://localhost:6379",
		"MORALIS_API_KEY":      "mkey",
		"LIFI_REFRESH_SECONDS": "abc",
	}
	if _, err := loadFrom(func(k string) string { return env[k] }); err == nil {
		t.Fatal("expected error for non-integer LIFI_REFRESH_SECONDS")
	}
}

func TestLoadFromRequiresMoralisKey(t *testing.T) {
	env := map[string]string{
		"ALCHEMY_API_KEY": "key123",
		"DATABASE_URL":    "postgres://db",
		"REDIS_URL":       "redis://localhost:6379",
	}
	if _, err := loadFrom(func(k string) string { return env[k] }); err == nil {
		t.Fatal("expected error when MORALIS_API_KEY missing")
	}
}

func TestLoadFromRejectsBadMoralisRecheck(t *testing.T) {
	env := map[string]string{
		"ALCHEMY_API_KEY":         "key123",
		"DATABASE_URL":            "postgres://db",
		"REDIS_URL":               "redis://localhost:6379",
		"MORALIS_API_KEY":         "mkey",
		"MORALIS_RECHECK_SECONDS": "0",
	}
	if _, err := loadFrom(func(k string) string { return env[k] }); err == nil {
		t.Fatal("expected error for non-positive MORALIS_RECHECK_SECONDS")
	}
}

package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func coinGeckoTestEnv() map[string]string {
	return map[string]string{
		"ALCHEMY_API_KEY": "key123",
		"DATABASE_URL":    "postgres://db",
		"REDIS_URL":       "redis://localhost:6379/0",
		"MORALIS_API_KEY": "mkey",
	}
}

func TestLoadFromAppliesCoinGeckoDefaults(t *testing.T) {
	env := coinGeckoTestEnv()
	cfg, err := loadFrom(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.CoinGeckoBaseURL != "https://api.coingecko.com/api/v3" {
		t.Errorf("base URL = %q", cfg.CoinGeckoBaseURL)
	}
	if cfg.CoinGeckoUserAgent != "wallet-api/1.0" || cfg.CoinGeckoPlatform != "ethereum" {
		t.Errorf("identity defaults = %+v", cfg)
	}
	if cfg.CoinGeckoListRefresh != 6*time.Hour || cfg.CoinGeckoMarketTTL != 30*time.Minute {
		t.Errorf("duration defaults = %+v", cfg)
	}
	if cfg.CoinGeckoEnrichTimeout != 5*time.Second {
		t.Errorf("enrichment timeout = %v", cfg.CoinGeckoEnrichTimeout)
	}
	if !reflect.DeepEqual(cfg.CoinGeckoNativeIDs, []string{"ethereum"}) {
		t.Errorf("native IDs = %#v", cfg.CoinGeckoNativeIDs)
	}
}

func TestLoadFromHonoursCoinGeckoOverrides(t *testing.T) {
	env := coinGeckoTestEnv()
	env["COINGECKO_BASE_URL"] = "http://coingecko.test/api/v3"
	env["COINGECKO_USER_AGENT"] = "wallet-api-test/2.0"
	env["COINGECKO_PLATFORM"] = "base"
	env["COINGECKO_LIST_REFRESH_SECONDS"] = "60"
	env["COINGECKO_MARKET_TTL_SECONDS"] = "90"
	env["COINGECKO_ENRICH_TIMEOUT_SECONDS"] = "7"
	env["COINGECKO_NATIVE_IDS"] = " ethereum,wrapped-ether,ethereum "
	cfg, err := loadFrom(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.CoinGeckoBaseURL != "http://coingecko.test/api/v3" || cfg.CoinGeckoPlatform != "base" {
		t.Errorf("string overrides = %+v", cfg)
	}
	if cfg.CoinGeckoListRefresh != time.Minute || cfg.CoinGeckoMarketTTL != 90*time.Second || cfg.CoinGeckoEnrichTimeout != 7*time.Second {
		t.Errorf("duration overrides = %+v", cfg)
	}
	if !reflect.DeepEqual(cfg.CoinGeckoNativeIDs, []string{"ethereum", "wrapped-ether"}) {
		t.Errorf("native IDs = %#v", cfg.CoinGeckoNativeIDs)
	}
}

func TestLoadFromRejectsInvalidCoinGeckoDurations(t *testing.T) {
	keys := []string{
		"COINGECKO_LIST_REFRESH_SECONDS",
		"COINGECKO_MARKET_TTL_SECONDS",
		"COINGECKO_ENRICH_TIMEOUT_SECONDS",
	}
	for _, key := range keys {
		for _, raw := range []string{"0", "-1", "abc"} {
			t.Run(key+"="+raw, func(t *testing.T) {
				env := coinGeckoTestEnv()
				env[key] = raw
				if _, err := loadFrom(func(k string) string { return env[k] }); err == nil {
					t.Fatalf("expected error for %s=%q", key, raw)
				} else if !strings.Contains(err.Error(), key) {
					t.Fatalf("error = %q, want error naming %s", err, key)
				}
			})
		}
	}
}

func TestLoadFromRejectsEmptyCoinGeckoNativeIDs(t *testing.T) {
	env := coinGeckoTestEnv()
	env["COINGECKO_NATIVE_IDS"] = " , "
	if _, err := loadFrom(func(k string) string { return env[k] }); err == nil {
		t.Fatal("expected error for empty COINGECKO_NATIVE_IDS")
	}
}

func TestLoadFromAppliesCoinMarketCapDefaults(t *testing.T) {
	env := coinGeckoTestEnv()
	cfg, err := loadFrom(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.CoinMarketCapBaseURL != "https://pro-api.coinmarketcap.com/public-api" {
		t.Errorf("base URL = %q", cfg.CoinMarketCapBaseURL)
	}
	if cfg.CoinMarketCapListRefresh != 6*time.Hour || cfg.CoinMarketCapMarketTTL != 30*time.Minute || cfg.TokenMarketEnrichTimeout != 5*time.Second {
		t.Errorf("CMC defaults = %+v", cfg)
	}
}

func TestLoadFromHonoursCoinMarketCapOverrides(t *testing.T) {
	env := coinGeckoTestEnv()
	env["COINMARKETCAP_BASE_URL"] = "http://cmc.test/public-api"
	env["COINMARKETCAP_LIST_REFRESH_SECONDS"] = "60"
	env["COINMARKETCAP_MARKET_TTL_SECONDS"] = "90"
	env["TOKEN_MARKET_ENRICH_TIMEOUT_SECONDS"] = "7"
	cfg, err := loadFrom(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.CoinMarketCapBaseURL != "http://cmc.test/public-api" || cfg.CoinMarketCapListRefresh != time.Minute || cfg.CoinMarketCapMarketTTL != 90*time.Second || cfg.TokenMarketEnrichTimeout != 7*time.Second {
		t.Fatalf("CMC overrides = %+v", cfg)
	}
}

func TestLoadFromRejectsInvalidCoinMarketCapDurations(t *testing.T) {
	for _, key := range []string{"COINMARKETCAP_LIST_REFRESH_SECONDS", "COINMARKETCAP_MARKET_TTL_SECONDS", "TOKEN_MARKET_ENRICH_TIMEOUT_SECONDS"} {
		for _, raw := range []string{"0", "-1", "abc"} {
			t.Run(key+"="+raw, func(t *testing.T) {
				env := coinGeckoTestEnv()
				env[key] = raw
				if _, err := loadFrom(func(k string) string { return env[k] }); err == nil || !strings.Contains(err.Error(), key) {
					t.Fatalf("error = %v, want error naming %s", err, key)
				}
			})
		}
	}
}

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
	if cfg.ProviderAPILogging {
		t.Error("provider API logging enabled without explicit opt-in")
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

func TestLoadFromProviderAPILoggingRequiresExplicitLocalOptIn(t *testing.T) {
	tests := []struct {
		name    string
		optIn   string
		service string
		enabled bool
	}{
		{name: "explicit local opt-in", optIn: "true", enabled: true},
		{name: "default off"},
		{name: "false", optIn: "false"},
		{name: "uppercase is not explicit", optIn: "TRUE"},
		{name: "numeric is not explicit", optIn: "1"},
		{name: "Cloud Run overrides opt-in", optIn: "true", service: "wallet-api"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := coinGeckoTestEnv()
			env["PROVIDER_API_LOGGING"] = tt.optIn
			env["K_SERVICE"] = tt.service
			cfg, err := loadFrom(func(k string) string { return env[k] })
			if err != nil {
				t.Fatalf("loadFrom: %v", err)
			}
			if cfg.ProviderAPILogging != tt.enabled {
				t.Fatalf("ProviderAPILogging = %t, want %t", cfg.ProviderAPILogging, tt.enabled)
			}
		})
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

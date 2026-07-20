package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds all runtime configuration, sourced from environment variables.
type Config struct {
	AlchemyAPIKey   string
	AlchemyNetwork  string
	DatabaseURL     string
	CacheTTL        time.Duration
	Port            string
	RedisURL        string
	LifiTokensURL   string
	LifiChain       string
	LifiRefresh     time.Duration
	MoralisAPIKey   string
	MoralisChain    string
	MoralisRecheck  time.Duration
	MoralisRedisTTL time.Duration
}

// Load reads configuration from the process environment.
func Load() (Config, error) {
	return loadFrom(os.Getenv)
}

// loadFrom reads configuration using the supplied getenv function (testable).
func loadFrom(getenv func(string) string) (Config, error) {
	cfg := Config{
		AlchemyAPIKey:  getenv("ALCHEMY_API_KEY"),
		AlchemyNetwork: getenv("ALCHEMY_NETWORK"),
		DatabaseURL:    getenv("DATABASE_URL"),
		Port:           getenv("PORT"),
		RedisURL:       getenv("REDIS_URL"),
	}
	if cfg.AlchemyAPIKey == "" {
		return Config{}, fmt.Errorf("ALCHEMY_API_KEY is required")
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.RedisURL == "" {
		return Config{}, fmt.Errorf("REDIS_URL is required")
	}
	if cfg.AlchemyNetwork == "" {
		cfg.AlchemyNetwork = "eth-mainnet"
	}
	if cfg.Port == "" {
		cfg.Port = "8080"
	}
	cfg.LifiTokensURL = getenv("LIFI_TOKENS_URL")
	if cfg.LifiTokensURL == "" {
		cfg.LifiTokensURL = "https://li.quest/v1/tokens"
	}
	cfg.LifiChain = getenv("LIFI_CHAIN")
	if cfg.LifiChain == "" {
		cfg.LifiChain = "ETH"
	}
	refresh := 3600
	if raw := getenv("LIFI_REFRESH_SECONDS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("LIFI_REFRESH_SECONDS must be a positive integer, got %q", raw)
		}
		refresh = n
	}
	cfg.LifiRefresh = time.Duration(refresh) * time.Second
	ttl := 300
	if raw := getenv("CACHE_TTL_SECONDS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("CACHE_TTL_SECONDS must be a positive integer, got %q", raw)
		}
		ttl = n
	}
	cfg.CacheTTL = time.Duration(ttl) * time.Second
	cfg.MoralisAPIKey = getenv("MORALIS_API_KEY")
	if cfg.MoralisAPIKey == "" {
		return Config{}, fmt.Errorf("MORALIS_API_KEY is required")
	}
	cfg.MoralisChain = getenv("MORALIS_CHAIN")
	if cfg.MoralisChain == "" {
		cfg.MoralisChain = "eth"
	}
	recheck := 604800
	if raw := getenv("MORALIS_RECHECK_SECONDS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("MORALIS_RECHECK_SECONDS must be a positive integer, got %q", raw)
		}
		recheck = n
	}
	cfg.MoralisRecheck = time.Duration(recheck) * time.Second
	redisTTL := 86400
	if raw := getenv("MORALIS_REDIS_TTL_SECONDS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("MORALIS_REDIS_TTL_SECONDS must be a positive integer, got %q", raw)
		}
		redisTTL = n
	}
	cfg.MoralisRedisTTL = time.Duration(redisTTL) * time.Second
	return cfg, nil
}

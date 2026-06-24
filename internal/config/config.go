package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds all runtime configuration, sourced from environment variables.
type Config struct {
	AlchemyAPIKey  string
	AlchemyNetwork string
	DatabaseURL    string
	CacheTTL       time.Duration
	Port           string
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
	}
	if cfg.AlchemyAPIKey == "" {
		return Config{}, fmt.Errorf("ALCHEMY_API_KEY is required")
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.AlchemyNetwork == "" {
		cfg.AlchemyNetwork = "eth-mainnet"
	}
	if cfg.Port == "" {
		cfg.Port = "8080"
	}
	ttl := 300
	if raw := getenv("CACHE_TTL_SECONDS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("CACHE_TTL_SECONDS must be a positive integer, got %q", raw)
		}
		ttl = n
	}
	cfg.CacheTTL = time.Duration(ttl) * time.Second
	return cfg, nil
}

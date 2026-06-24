package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"wallet-api/internal/alchemy"
	"wallet-api/internal/api"
	"wallet-api/internal/config"
	"wallet-api/internal/lifi"
	"wallet-api/internal/rediscache"
	"wallet-api/internal/store"
	"wallet-api/internal/tokenlist"
	"wallet-api/internal/wallet"
)

// redisTokenListTTL is the safety TTL for the cached list. Each successful refresh
// resets it, so the list survives up to 24 h of consecutive refresher failures.
const redisTokenListTTL = 24 * time.Hour

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	setupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pg, err := store.New(setupCtx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer pg.Close()
	if err := pg.Migrate(setupCtx); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	redisCache, err := rediscache.New(cfg.RedisURL, redisTokenListTTL)
	if err != nil {
		log.Fatalf("redis: %v", err)
	}
	defer redisCache.Close()
	if err := redisCache.Ping(setupCtx); err != nil {
		log.Fatalf("redis ping: %v", err)
	}

	lifiClient := lifi.New(cfg.LifiTokensURL)
	holder := &tokenlist.Holder{}
	refresher := tokenlist.NewRefresher(lifiClient, redisCache, pg, holder, cfg.LifiChain, cfg.LifiRefresh)
	if err := refresher.Bootstrap(setupCtx); err != nil {
		log.Fatalf("token list bootstrap: %v", err)
	}
	go refresher.Run(context.Background())
	log.Printf("token list ready: %d tokens (chain=%s, refresh=%s)", holder.Current().Count(), cfg.LifiChain, cfg.LifiRefresh)

	ac := alchemy.New(cfg.AlchemyAPIKey, cfg.AlchemyNetwork)
	svc := wallet.NewService(ac, pg, pg, holder, cfg.AlchemyNetwork, cfg.CacheTTL)
	router := api.NewRouter(svc)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("wallet-api listening on :%s (network=%s, ttl=%s)", cfg.Port, cfg.AlchemyNetwork, cfg.CacheTTL)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server: %v", err)
	}
}

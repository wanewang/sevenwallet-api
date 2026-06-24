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
	"wallet-api/internal/store"
	"wallet-api/internal/wallet"
)

// permitAll is a placeholder Allowlist that passes every token through until
// the real tokenlist.Holder is wired up in Task 8.
type permitAll struct{}

func (permitAll) LookupByAddress(addr string) (lifi.ListToken, bool) {
	return lifi.ListToken{Address: addr}, true
}

func (permitAll) HasSymbol(_ string) bool { return true }

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pg, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer pg.Close()
	if err := pg.Migrate(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	ac := alchemy.New(cfg.AlchemyAPIKey, cfg.AlchemyNetwork)
	svc := wallet.NewService(ac, pg, pg, permitAll{}, cfg.AlchemyNetwork, cfg.CacheTTL)
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

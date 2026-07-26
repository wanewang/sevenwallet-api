package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"wallet-api/internal/alchemy"
	"wallet-api/internal/api"
	"wallet-api/internal/cmcmarket"
	"wallet-api/internal/coingecko"
	"wallet-api/internal/coinmarketcap"
	"wallet-api/internal/config"
	"wallet-api/internal/lifi"
	"wallet-api/internal/marketchain"
	"wallet-api/internal/marketcompare"
	"wallet-api/internal/marketdata"
	"wallet-api/internal/moralis"
	"wallet-api/internal/rediscache"
	"wallet-api/internal/store"
	"wallet-api/internal/tokenlist"
	"wallet-api/internal/tokenvalidity"
	"wallet-api/internal/wallet"
)

// redisTokenListTTL is the safety TTL for the cached list. Each successful refresh
// resets it, so the list survives up to 24 h of consecutive refresher failures.
const redisTokenListTTL = 24 * time.Hour

// @title        wallet-api
// @version      1.0.0
// @description  Read-only, non-custodial EVM wallet API (token portfolio + transaction history), allowlist-filtered via LI.FI.
// @BasePath     /v1
func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	marketChain, err := marketchain.Lookup(cfg.AlchemyNetwork)
	if err != nil {
		log.Fatalf("market chain: %v", err)
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

	var coinGeckoOptions []coingecko.Option
	if cfg.ProviderAPILogging {
		coinGeckoOptions = append(coinGeckoOptions, coingecko.WithLogf(log.Printf))
	}
	coinGeckoClient := coingecko.New(cfg.CoinGeckoBaseURL, cfg.CoinGeckoUserAgent, coinGeckoOptions...)
	coinCatalog := &marketdata.Holder{}
	coinRefresher := marketdata.NewRefresher(coinGeckoClient, pg, coinCatalog, cfg.CoinGeckoListRefresh)
	coinRefresher.Bootstrap(setupCtx)
	go coinRefresher.Run(context.Background())
	log.Printf("marketdata catalog ready: %d mappings (platform=%s, refresh=%s)", coinCatalog.Count(), cfg.CoinGeckoPlatform, cfg.CoinGeckoListRefresh)

	cmcClient := coinmarketcap.New(cfg.CoinMarketCapBaseURL)
	cmcCatalog := &cmcmarket.Holder{}
	cmcRefresher := cmcmarket.NewRefresher(
		cmcClient, pg, cmcCatalog, marketchain.CoinMarketCapPlatformIDs(),
		cfg.CoinMarketCapListRefresh,
	)
	cmcRefresher.Bootstrap(setupCtx)
	go cmcRefresher.Run(context.Background())
	log.Printf("CoinMarketCap catalog ready: %d mappings (platform=%d, refresh=%s)", cmcCatalog.Count(), marketChain.CoinMarketCapPlatform, cfg.CoinMarketCapListRefresh)

	var alchemyOptions []alchemy.Option
	var moralisOptions []moralis.Option
	if cfg.ProviderAPILogging {
		alchemyOptions = append(alchemyOptions, alchemy.WithLogf(log.Printf))
		moralisOptions = append(moralisOptions, moralis.WithLogf(log.Printf))
	}
	ac := alchemy.New(cfg.AlchemyAPIKey, cfg.AlchemyNetwork, alchemyOptions...)
	moralisClient := moralis.New(cfg.MoralisAPIKey, cfg.MoralisChain, moralisOptions...)
	validator := tokenvalidity.NewChecker(moralisClient, redisCache, pg, cfg.MoralisChain, cfg.MoralisRecheck, cfg.MoralisRedisTTL)
	coinMarket := marketdata.NewService(
		coinGeckoClient, redisCache, pg, coinCatalog,
		cfg.CoinGeckoPlatform, cfg.CoinGeckoNativeIDs,
		cfg.CoinGeckoMarketTTL, cfg.CoinGeckoEnrichTimeout,
	)
	svc := wallet.NewService(ac, pg, pg, holder, validator, coinMarket, cfg.AlchemyNetwork, cfg.CacheTTL)
	coinGeckoCompare := marketdata.NewService(
		coinGeckoClient, redisCache, pg, coinCatalog,
		marketChain.CoinGeckoPlatform, []string{marketChain.CoinGeckoNativeID},
		cfg.CoinGeckoMarketTTL, cfg.TokenMarketEnrichTimeout,
	)
	coinMarketCapCompare := cmcmarket.NewService(
		cmcClient, redisCache, pg, cmcCatalog,
		marketChain.CoinMarketCapPlatform, marketChain.CoinMarketCapNativeID,
		marketChain.MarketChain, cfg.CoinMarketCapMarketTTL,
	)
	svc.SetMarketComparator(marketcompare.New(coinGeckoCompare, coinMarketCapCompare, cfg.TokenMarketEnrichTimeout))
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

package wallet

import (
	"context"
	"errors"
	"time"

	"wallet-api/internal/alchemy"
	"wallet-api/internal/lifi"
)

// Sentinel errors let the API layer map failures to HTTP status codes.
var (
	ErrUpstream               = errors.New("upstream provider error")
	ErrStore                  = errors.New("storage error")
	ErrNativeTokenUnavailable = errors.New("native token data unavailable")
	ErrWalletNotCached        = errors.New("wallet token cache not found")
)

// Price is a single currency price for a token.
type Price struct {
	Currency      string `json:"currency"`
	Value         string `json:"value"`
	LastUpdatedAt string `json:"lastUpdatedAt"`
}

// Token is a normalized token holding returned to API clients.
type Token struct {
	TokenAddress        *string  `json:"tokenAddress"`
	Symbol              string   `json:"symbol"`
	Name                string   `json:"name"`
	Decimals            int      `json:"decimals"`
	RawBalance          string   `json:"rawBalance"`
	Balance             string   `json:"balance"`
	IsNative            bool     `json:"isNative"`
	Price               *Price   `json:"price"`
	LogoURI             *string  `json:"logoURI,omitempty"`
	CoinKey             *string  `json:"coinKey,omitempty" extensions:"x-nullable"`
	PriceUSD            *string  `json:"priceUSD,omitempty"`
	Change24HPercent    *float64 `json:"change24hPercent" extensions:"x-nullable"`
	MarketCapUSD        *float64 `json:"marketCapUSD" extensions:"x-nullable"`
	MarketDataUpdatedAt *string  `json:"marketDataUpdatedAt" extensions:"x-nullable"`
}

// TokenPortfolio is the current token snapshot for an address.
type TokenPortfolio struct {
	Address   string    `json:"address"`
	Network   string    `json:"network"`
	FetchedAt time.Time `json:"fetchedAt"`
	Tokens    []Token   `json:"tokens"`
}

// CoinGeckoMarket is fresh CoinGecko comparison data for one token.
type CoinGeckoMarket struct {
	ID               string   `json:"id"`
	PriceUSD         *string  `json:"priceUSD" extensions:"x-nullable"`
	Change24HPercent *float64 `json:"change24hPercent" extensions:"x-nullable"`
}

// CoinMarketCapMarket is fresh CoinMarketCap comparison data for one token.
type CoinMarketCapMarket struct {
	ID               int64    `json:"id"`
	PriceUSD         *string  `json:"priceUSD" extensions:"x-nullable"`
	Change24HPercent *float64 `json:"change24hPercent" extensions:"x-nullable"`
}

// TokenMarket contains cached wallet identity/balance plus provider comparisons.
type TokenMarket struct {
	TokenAddress *string              `json:"tokenAddress" extensions:"x-nullable"`
	Symbol       string               `json:"symbol"`
	Name         string               `json:"name"`
	Decimals     int                  `json:"decimals"`
	Balance      string               `json:"balance"`
	CG           *CoinGeckoMarket     `json:"cg" extensions:"x-nullable"`
	CMC          *CoinMarketCapMarket `json:"cmc" extensions:"x-nullable"`
}

// TokenMarketPortfolio is the cache-only dual-provider response.
type TokenMarketPortfolio struct {
	Wallet             string        `json:"wallet"`
	Network            string        `json:"network"`
	PortfolioFetchedAt time.Time     `json:"portfolioFetchedAt"`
	Tokens             []TokenMarket `json:"tokens"`
}

// MarketPair aligns provider results with one input Token by slice index.
type MarketPair struct {
	CG  *CoinGeckoMarket
	CMC *CoinMarketCapMarket
}

// Transfer is a single asset transfer returned to API clients.
type Transfer struct {
	Hash     string `json:"hash"`
	From     string `json:"from"`
	To       string `json:"to"`
	Asset    string `json:"asset"`
	Value    string `json:"value"`
	BlockNum string `json:"blockNum"`
	Category string `json:"category"`
}

// TransactionPage is a page of transfers for an address.
type TransactionPage struct {
	Address     string     `json:"address"`
	Transfers   []Transfer `json:"transfers"`
	NextPageKey string     `json:"nextPageKey,omitempty"`
}

// Allowlist is the LI.FI token allowlist the service filters/enriches against.
type Allowlist interface {
	LookupByAddress(addr string) (lifi.ListToken, bool)
	LookupNative() (lifi.ListToken, time.Time, bool)
	HasSymbol(sym string) bool
}

// Validation is the verdict + enrichment metadata for an unlisted ERC-20.
type Validation struct {
	Valid    bool
	Symbol   string
	Name     string
	LogoURI  string
	Decimals int
}

// Validator decides whether an unlisted ERC-20 is a legitimate token and
// supplies enrichment metadata when it is.
type Validator interface {
	Validate(ctx context.Context, address string) (Validation, error)
}

// MarketEnricher adds market data to tokens after wallet filtering.
type MarketEnricher interface {
	EnrichTokens(ctx context.Context, tokens []Token) []Token
}

// MarketComparator returns provider-specific fresh data aligned to input tokens.
type MarketComparator interface {
	Compare(ctx context.Context, tokens []Token) []MarketPair
}

// AlchemyClient is the subset of the Alchemy client the service depends on.
type AlchemyClient interface {
	GetTokens(ctx context.Context, address, network string) ([]alchemy.Token, error)
	GetTransfers(ctx context.Context, address string, limit int, pageKey string) (alchemy.TransfersResult, error)
}

// TokenStore persists the newest token snapshot per address.
type TokenStore interface {
	GetFreshTokens(ctx context.Context, address, network string, ttl time.Duration) (*TokenPortfolio, bool, error)
	GetLatestTokens(ctx context.Context, address, network string) (*TokenPortfolio, bool, error)
	SaveTokens(ctx context.Context, p *TokenPortfolio) error
}

// TxCache persists transaction-history pages as JSON.
type TxCache interface {
	GetFreshTransactions(ctx context.Context, address, params string, ttl time.Duration) (*TransactionPage, bool, error)
	SaveTransactions(ctx context.Context, address, params string, page *TransactionPage) error
}

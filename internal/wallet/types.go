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
	ErrUpstream = errors.New("upstream provider error")
	ErrStore    = errors.New("storage error")
)

// Price is a single currency price for a token.
type Price struct {
	Currency      string `json:"currency"`
	Value         string `json:"value"`
	LastUpdatedAt string `json:"lastUpdatedAt"`
}

// Token is a normalized token holding returned to API clients.
type Token struct {
	TokenAddress *string `json:"tokenAddress"`
	Symbol       string  `json:"symbol"`
	Name         string  `json:"name"`
	Decimals     int     `json:"decimals"`
	RawBalance   string  `json:"rawBalance"`
	Balance      string  `json:"balance"`
	IsNative     bool    `json:"isNative"`
	Price        *Price  `json:"price"`
	LogoURI  *string `json:"logoURI,omitempty"`
	CoinKey  *string `json:"coinKey,omitempty"`
	PriceUSD *string `json:"priceUSD,omitempty"`
}

// TokenPortfolio is the current token snapshot for an address.
type TokenPortfolio struct {
	Address   string    `json:"address"`
	Network   string    `json:"network"`
	FetchedAt time.Time `json:"fetchedAt"`
	Tokens    []Token   `json:"tokens"`
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
	HasSymbol(sym string) bool
}

// AlchemyClient is the subset of the Alchemy client the service depends on.
type AlchemyClient interface {
	GetTokens(ctx context.Context, address, network string) ([]alchemy.Token, error)
	GetTransfers(ctx context.Context, address string, limit int, pageKey string) (alchemy.TransfersResult, error)
}

// TokenStore persists the newest token snapshot per address.
type TokenStore interface {
	GetFreshTokens(ctx context.Context, address, network string, ttl time.Duration) (*TokenPortfolio, bool, error)
	SaveTokens(ctx context.Context, p *TokenPortfolio) error
}

// TxCache persists transaction-history pages as JSON.
type TxCache interface {
	GetFreshTransactions(ctx context.Context, address, params string, ttl time.Duration) (*TransactionPage, bool, error)
	SaveTransactions(ctx context.Context, address, params string, page *TransactionPage) error
}

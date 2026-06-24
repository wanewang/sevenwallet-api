package api

import (
	"context"
	"net/http"

	"wallet-api/internal/wallet"
)

// WalletService is the behavior the HTTP layer needs from the domain service.
type WalletService interface {
	GetTokens(ctx context.Context, address string) (*wallet.TokenPortfolio, error)
	GetTransactions(ctx context.Context, address string, limit int, pageKey string) (*wallet.TransactionPage, error)
}

// NewRouter wires the read-only wallet endpoints.
func NewRouter(svc WalletService) http.Handler {
	h := &handlers{svc: svc}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/addresses/{address}/tokens", h.getTokens)
	mux.HandleFunc("GET /v1/addresses/{address}/transactions", h.getTransactions)
	return mux
}

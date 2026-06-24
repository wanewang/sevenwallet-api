package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"

	"wallet-api/internal/wallet"
)

const (
	defaultLimit = 25
	maxLimit     = 100
)

var addressRE = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

// ValidAddress reports whether s is a 0x-prefixed 20-byte hex address.
func ValidAddress(s string) bool { return addressRE.MatchString(s) }

type handlers struct {
	svc WalletService
}

// ErrorResponse is the JSON body returned for any non-2xx response.
type ErrorResponse struct {
	Error string `json:"error"`
}

// getTokens returns an address's allowlist-filtered, enriched token portfolio.
//
// @Summary      Token portfolio
// @Description  Native + ERC-20 holdings for an address, filtered to the LI.FI allowlist and enriched with metadata.
// @Tags         addresses
// @Produce      json
// @Param        address  path      string  true  "0x-prefixed 20-byte hex address"
// @Success      200      {object}  wallet.TokenPortfolio
// @Failure      400      {object}  api.ErrorResponse
// @Failure      502      {object}  api.ErrorResponse
// @Failure      503      {object}  api.ErrorResponse
// @Router       /addresses/{address}/tokens [get]
func (h *handlers) getTokens(w http.ResponseWriter, r *http.Request) {
	address := r.PathValue("address")
	if !ValidAddress(address) {
		writeError(w, http.StatusBadRequest, "invalid address")
		return
	}
	p, err := h.svc.GetTokens(r.Context(), address)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// getTransactions returns a page of an address's allowlist-filtered transfer history.
//
// @Summary      Transaction history
// @Description  Asset transfers for an address (paginated), filtered to the LI.FI allowlist.
// @Tags         addresses
// @Produce      json
// @Param        address  path      string  true   "0x-prefixed 20-byte hex address"
// @Param        limit    query     int     false  "page size (default 25, max 100)"
// @Param        pageKey  query     string  false  "opaque pagination cursor"
// @Success      200      {object}  wallet.TransactionPage
// @Failure      400      {object}  api.ErrorResponse
// @Failure      502      {object}  api.ErrorResponse
// @Failure      503      {object}  api.ErrorResponse
// @Router       /addresses/{address}/transactions [get]
func (h *handlers) getTransactions(w http.ResponseWriter, r *http.Request) {
	address := r.PathValue("address")
	if !ValidAddress(address) {
		writeError(w, http.StatusBadRequest, "invalid address")
		return
	}
	limit := parseLimit(r.URL.Query().Get("limit"))
	pageKey := r.URL.Query().Get("pageKey")
	page, err := h.svc.GetTransactions(r.Context(), address, limit, pageKey)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func parseLimit(raw string) int {
	if raw == "" {
		return defaultLimit
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultLimit
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, wallet.ErrUpstream):
		writeError(w, http.StatusBadGateway, "upstream provider error")
	case errors.Is(err, wallet.ErrStore):
		writeError(w, http.StatusServiceUnavailable, "storage unavailable")
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}

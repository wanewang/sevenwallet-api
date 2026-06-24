package alchemy

// Price is a single currency price for a token.
type Price struct {
	Currency      string
	Value         string
	LastUpdatedAt string
}

// Token is a raw token holding returned by the Portfolio API.
// TokenAddress is nil for the chain native token (e.g. ETH).
type Token struct {
	TokenAddress *string
	Symbol       string
	Name         string
	Decimals     int
	RawBalance   string
	Price        *Price
}

// Transfer is a single asset transfer returned by getAssetTransfers.
type Transfer struct {
	Hash     string
	From     string
	To       string
	Asset    string
	Value    string
	BlockNum string
	Category string
}

// TransfersResult is a page of transfers.
type TransfersResult struct {
	Transfers []Transfer
	PageKey   string
}

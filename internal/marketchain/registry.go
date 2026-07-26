// Package marketchain defines the chains supported by dual-provider market lookup.
package marketchain

import "fmt"

// Chain contains provider identifiers for one supported Alchemy network.
type Chain struct {
	AlchemyNetwork        string
	EVMChainID            int64
	MarketChain           string
	CoinMarketCapPlatform int64
	CoinMarketCapNativeID int64
	CoinGeckoPlatform     string
	CoinGeckoNativeID     string
}

var supported = map[string]Chain{
	"eth-mainnet": {
		AlchemyNetwork:        "eth-mainnet",
		EVMChainID:            1,
		MarketChain:           "ethereum",
		CoinMarketCapPlatform: 1,
		CoinMarketCapNativeID: 1027,
		CoinGeckoPlatform:     "ethereum",
		CoinGeckoNativeID:     "ethereum",
	},
}

// Lookup returns the fixed market-provider identifiers for an Alchemy network.
func Lookup(network string) (Chain, error) {
	chain, ok := supported[network]
	if !ok {
		return Chain{}, fmt.Errorf("no market-provider chain mapping for ALCHEMY_NETWORK=%q", network)
	}
	return chain, nil
}

// CoinMarketCapPlatformIDs returns the supported CMC platform ID set.
func CoinMarketCapPlatformIDs() map[int64]struct{} {
	ids := make(map[int64]struct{}, len(supported))
	for _, chain := range supported {
		ids[chain.CoinMarketCapPlatform] = struct{}{}
	}
	return ids
}

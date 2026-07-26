package marketchain

import (
	"strings"
	"testing"
)

func TestLookupEthereum(t *testing.T) {
	chain, err := Lookup("eth-mainnet")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if chain.EVMChainID != 1 || chain.MarketChain != "ethereum" || chain.CoinMarketCapPlatform != 1 || chain.CoinMarketCapNativeID != 1027 {
		t.Fatalf("chain = %+v", chain)
	}
	if chain.CoinGeckoPlatform != "ethereum" || chain.CoinGeckoNativeID != "ethereum" {
		t.Fatalf("CoinGecko identifiers = %+v", chain)
	}
	if _, ok := CoinMarketCapPlatformIDs()[1]; !ok {
		t.Fatal("supported CMC platform IDs omit Ethereum")
	}
}

func TestLookupRejectsUnregisteredNetwork(t *testing.T) {
	_, err := Lookup("base-mainnet")
	if err == nil || !strings.Contains(err.Error(), `ALCHEMY_NETWORK="base-mainnet"`) {
		t.Fatalf("error = %v", err)
	}
}

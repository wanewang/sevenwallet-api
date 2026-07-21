package main

import (
	"wallet-api/internal/rediscache"
	"wallet-api/internal/store"
	"wallet-api/internal/tokenvalidity"
)

var _ tokenvalidity.Cache = (*rediscache.Cache)(nil)
var _ tokenvalidity.Store = (*store.Postgres)(nil)

func main() {}

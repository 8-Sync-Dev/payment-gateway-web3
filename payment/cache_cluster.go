package payment

import (
	"time"

	"encore.dev/storage/cache"
)

const supportedCurrenciesCacheKey = "latest"

type CurrencyInfo struct {
	Ccy      string `json:"ccy"`
	Name     string `json:"name"`
	Chain    string `json:"chain"`
	MinDep   string `json:"minDep"`
	LogoLink string `json:"logoLink"`
}
type CachedCurrencies struct {
	Currencies []CurrencyInfo `json:"currencies"`
}

var Web3CacheCluster = cache.NewCluster("payment-gateway-web3", cache.ClusterConfig{
	EvictionPolicy: cache.AllKeysLRU,
})

var SupportedCurrenciesCache = cache.NewStructKeyspace[string, CachedCurrencies](
	Web3CacheCluster,
	cache.KeyspaceConfig{
		KeyPattern:    "hub:currencies/:key",
		DefaultExpiry: cache.ExpireIn(15 * time.Minute),
	},
)

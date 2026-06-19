package payment

import (
	"time"

	"encore.app/payment/cacheadapter"
	"encore.dev/storage/cache"
	"encore.dev/storage/sqldb"
)

var db = sqldb.NewDatabase("payment", sqldb.DatabaseConfig{
	Migrations: "./migrations",
})

var cacheCluster = cache.NewCluster("payment-gateway-web3", cache.ClusterConfig{
	EvictionPolicy: cache.AllKeysLRU,
})

var supportedCurrenciesKeyspace = cache.NewStructKeyspace[string, cacheadapter.Payload](
	cacheCluster,
	cache.KeyspaceConfig{
		KeyPattern:    "hub:currencies/:key",
		DefaultExpiry: cache.ExpireIn(15 * time.Minute),
	},
)

const supportedCurrenciesCacheKey = "latest"

// secrets is populated by Encore at runtime. Set via:
//
//	encore secret set --type development OKXApiKey
//	encore secret set --type development OKXSecretKey
//	encore secret set --type development OKXPassphrase
var secrets struct {
	OKXApiKey     string
	OKXSecretKey  string
	OKXPassphrase string
}

package payment

import (
	"context"

	"encore.dev/cron"
)

var _ = cron.NewJob("sync-okx-currencies", cron.JobConfig{
	Title:    "Sync OKX Currencies",
	Endpoint: SyncOKXCurrencies,
	Every:    15 * cron.Minute,
})

//encore:api private
func SyncOKXCurrencies(ctx context.Context) error {
	_, err := syncSupportedCurrenciesCache(ctx)
	return err
}

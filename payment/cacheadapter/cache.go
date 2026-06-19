package cacheadapter

import (
	"context"
	"errors"
	"fmt"

	"encore.app/payment/domain"
	"encore.dev/storage/cache"
)

// Payload is the cached wire shape. Exported so the service root can declare
// the typed keyspace with this value type.
type Payload struct {
	Currencies []domain.Currency `json:"currencies"`
}

// Interface Guard best pratice for golang
var _ domain.CurrencyCache = (*CurrencyCache)(nil)

type CurrencyCache struct {
	keyspace *cache.StructKeyspace[string, Payload]
	key      string
}

func New(keyspace *cache.StructKeyspace[string, Payload], key string) *CurrencyCache {
	return &CurrencyCache{keyspace: keyspace, key: key}
}

func (c *CurrencyCache) Get(ctx context.Context) ([]domain.Currency, error) {
	p, err := c.keyspace.Get(ctx, c.key)
	if err != nil {
		if errors.Is(err, cache.Miss) {
			return nil, domain.ErrCacheMiss
		}
		return nil, fmt.Errorf("cache: get: %w", err)
	}
	return p.Currencies, nil
}

func (c *CurrencyCache) Set(ctx context.Context, currencies []domain.Currency) error {
	if err := c.keyspace.Set(ctx, c.key, Payload{Currencies: currencies}); err != nil {
		return fmt.Errorf("cache: set: %w", err)
	}
	return nil
}

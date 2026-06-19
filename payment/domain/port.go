package domain

import (
	"context"
	"time"
)

// DepositHandler is invoked by a Provider for each detected deposit.
// Must be idempotent — providers may redeliver the same deposit on reconnect.
type DepositHandler func(ctx context.Context, deposit Deposit) error

// Provider abstracts the exchange/custodian holding deposit addresses
// and observing incoming deposits.
type Provider interface {
	Name() string
	GetDepositAddress(ctx context.Context, ccy, chain string) (*DepositAddress, error)
	ListSupportedCurrencies(ctx context.Context) ([]Currency, error)
	// SubscribeDeposits blocks until ctx cancelled or unrecoverable error.
	// Reconnection/backoff is the provider's responsibility.
	SubscribeDeposits(ctx context.Context, handler DepositHandler) error
	Close() error
}

// Repository abstracts persistence of Transactions.
type Repository interface {
	CreateTransaction(ctx context.Context, tx *Transaction) error
	// HasPendingAmount — deposit fingerprint dedup at checkout.
	HasPendingAmount(ctx context.Context, ccy string, amount float64) (bool, error)
	// HasTxHash — deposit idempotency (WS at-least-once delivery).
	HasTxHash(ctx context.Context, txHash string) (bool, error)
	ListPendingForDeposit(ctx context.Context, ccy string, since time.Time) ([]*Transaction, error)
	// MarkSuccess returns rowsAffected — caller MUST check; 0 means a
	// concurrent matcher won this order.
	MarkSuccess(ctx context.Context, id string, receivedAmount float64, txHash string) (int64, error)
}

type CurrencyCache interface {
	Get(ctx context.Context) ([]Currency, error)
	Set(ctx context.Context, currencies []Currency) error
}

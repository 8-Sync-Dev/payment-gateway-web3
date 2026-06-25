package domain

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

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

// Repository abstracts persistence of Transactions, the deposit ledger, and
// the match-review queue.
type Repository interface {
	CreateTransaction(ctx context.Context, tx *Transaction) (inserted bool, err error)
	// HasPendingAmount — fast-path collision check used by amount dedup at
	// checkout. The transactions_pending_amount_uidx partial index is the
	// authoritative guard; this only reduces INSERT retries.
	HasPendingAmount(ctx context.Context, ccy string, amount decimal.Decimal) (bool, error)
	ListPendingForDeposit(ctx context.Context, ccy string, since time.Time) ([]*Transaction, error)
	// MarkSuccess returns rowsAffected — caller MUST check; 0 means a
	// concurrent matcher or expiry won this order.
	MarkSuccess(ctx context.Context, id string, receivedAmount decimal.Decimal, txHash string) (int64, error)

	// RecordDeposit appends to the immutable deposit ledger, idempotent on
	// tx_id. inserted=false means the deposit was already processed.
	RecordDeposit(ctx context.Context, d *Deposit) (inserted bool, err error)
	// MarkDepositMatched links a ledger deposit to the order it matched.
	MarkDepositMatched(ctx context.Context, txHash, orderID string) error
	// CreateMatchException queues a deposit that could not be auto-matched.
	CreateMatchException(ctx context.Context, e *MatchException) error

	// ExpirePendingOrders marks orders older than olderThan as expired,
	// freeing their (currency, amount) for reuse. Returns count expired.
	ExpirePendingOrders(ctx context.Context, olderThan time.Duration) (int64, error)
	ListOpenExceptions(ctx context.Context, limit int) ([]*MatchException, error)
	ResolveMatchException(ctx context.Context, id int64, resolution string) error
}

type CurrencyCache interface {
	Get(ctx context.Context) ([]Currency, error)
	Set(ctx context.Context, currencies []Currency) error
}

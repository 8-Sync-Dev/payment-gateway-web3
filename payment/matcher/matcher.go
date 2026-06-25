// Package matcher contains the deposit-to-transaction matching engine.
// Pure logic with no I/O — feeds off domain types only.
package matcher

import (
	"time"

	"encore.app/payment/domain"
	"github.com/shopspring/decimal"
)

type Config struct {
	// AllowedMargin is the max |orderAmount - depositAmount| that qualifies a
	// match.
	//
	// With a single deposit address and no memo, amount is the ONLY
	// discriminator, so this MUST be 0 (exact). The unique partial index on
	// transactions(currency, amount) WHERE status='pending' guarantees at most
	// one pending order has any given amount, so exact matching is
	// unambiguous. Any margin > 0 can bridge two distinct pending amounts and
	// cause a WRONG match; deposits that don't match exactly are routed to
	// match_exceptions for review instead of being guessed.
	AllowedMargin decimal.Decimal
	// TimeWindow bounds the candidate lookup and order expiry. A deposit at
	// time T considers pending orders created since T-TimeWindow; orders older
	// than TimeWindow are expired by the expire-pending-orders cron.
	TimeWindow time.Duration
}

func Default() Config {
	return Config{
		AllowedMargin: decimal.Zero,
		TimeWindow:    24 * time.Hour,
	}
}

type Matcher struct {
	cfg Config
}

func New(cfg Config) *Matcher {
	return &Matcher{cfg: cfg}
}

// FindBestMatch returns the transaction whose amount is within AllowedMargin of
// the deposit, tie-broken by smallest time gap. With AllowedMargin=0 (default)
// only an exact-amount order qualifies, and the pending-amount unique index
// guarantees there is at most one. Returns nil if none qualify.
// Caller must still call Repository.MarkSuccess and check rowsAffected.
func (m *Matcher) FindBestMatch(deposit domain.Deposit, candidates []*domain.Transaction) *domain.Transaction {
	var best *domain.Transaction
	var bestScore decimal.Decimal
	var bestTimeGap time.Duration

	for _, tx := range candidates {
		diff := tx.Amount.Sub(deposit.Amt).Abs()
		if diff.GreaterThan(m.cfg.AllowedMargin) {
			continue
		}
		gap := deposit.Time.Sub(tx.CreatedAt)
		if gap < 0 {
			gap = -gap
		}
		if best == nil || diff.LessThan(bestScore) || (diff.Equal(bestScore) && gap < bestTimeGap) {
			bestScore = diff
			bestTimeGap = gap
			best = tx
		}
	}
	return best
}

func (m *Matcher) Since(depositTime time.Time) time.Time {
	return depositTime.Add(-m.cfg.TimeWindow)
}

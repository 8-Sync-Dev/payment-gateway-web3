// Package matcher contains the deposit-to-transaction matching engine.
// Pure logic with no I/O — feeds off domain types only.
package matcher

import (
	"time"

	"encore.app/payment/domain"
	"github.com/shopspring/decimal"
)

type Config struct {
	AllowedMargin decimal.Decimal // max |orderAmount - depositAmount| to qualify
	TimeWindow    time.Duration   // how far back from deposit time to look
}

func Default() Config {
	return Config{
		AllowedMargin: decimal.RequireFromString("0.1"),
		TimeWindow:    2 * time.Hour,
	}
}

type Matcher struct {
	cfg Config
}

func New(cfg Config) *Matcher {
	return &Matcher{cfg: cfg}
}

// FindBestMatch returns the transaction closest to deposit.Amt within
// AllowedMargin, tie-broken by smallest time gap. Returns nil if none qualify.
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

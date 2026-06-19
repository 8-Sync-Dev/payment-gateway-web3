// Package matcher contains the deposit-to-transaction matching engine.
// Pure logic with no I/O — feeds off domain types only.
package matcher

import (
	"math"
	"time"

	"encore.app/payment/domain"
)

type Config struct {
	AllowedMargin float64       // max |orderAmount - depositAmount| to qualify
	TimeWindow    time.Duration // how far back from deposit time to look
}

func Default() Config {
	return Config{AllowedMargin: 0.1, TimeWindow: 2 * time.Hour}
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
	bestScore := math.MaxFloat64
	var best *domain.Transaction
	var bestTimeGap time.Duration = math.MaxInt64

	for _, tx := range candidates {
		diff := math.Abs(tx.Amount - deposit.Amt)
		if diff > m.cfg.AllowedMargin {
			continue
		}
		gap := deposit.Time.Sub(tx.CreatedAt)
		if gap < 0 {
			gap = -gap
		}
		if diff < bestScore || (diff == bestScore && gap < bestTimeGap) {
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

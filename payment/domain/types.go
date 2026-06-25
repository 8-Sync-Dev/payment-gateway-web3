// Package domain defines core business types and port contracts for the
// payment gateway. Adapters implement these interfaces; the service consumes them.
package domain

import (
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

// Transaction maps 1:1 to the `transactions` table.
type Transaction struct {
	ID             string
	UserID         string
	OrderID        string
	Amount         decimal.Decimal
	Currency       string
	DepositAddress string
	Status         string // StatusPending | StatusSuccess | StatusExpired
	TxID           string // on-chain tx hash, set when matched
	ReceivedAmount decimal.Decimal
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

const (
	StatusPending = "pending"
	StatusSuccess = "success"
	StatusExpired = "expired"
)

// Deposit is the normalized view of a blockchain deposit, translated from
// provider-specific push events (OKX, Binance, on-chain).
type Deposit struct {
	Ccy   string
	Amt   decimal.Decimal
	State string // provider-specific; "2" = finalized on OKX
	TxID  string
	Time  time.Time
}

type Currency struct {
	Ccy      string `json:"ccy"`
	Name     string `json:"name"`
	Chain    string `json:"chain"`
	MinDep   string `json:"minDep"`
	LogoLink string `json:"logoLink"`
}

type DepositAddress struct {
	Addr  string
	Chain string
	Ccy   string
}

// MatchException records a finalized deposit that could not be auto-matched and
// needs human review. Nothing is ever dropped: every finalized deposit lands in
// the immutable deposits ledger; those without a confident exact-amount match
// also land here for an operator to reconcile.
type MatchException struct {
	ID          int64                `json:"id"`
	DepositTxID string               `json:"deposit_tx_id"`
	Ccy         string               `json:"ccy"`
	Amt         decimal.Decimal      `json:"amt"`
	Reason      string               `json:"reason"` // ExceptionNoExactMatch | ExceptionRaceLost | ExceptionAmbiguous
	Candidates  []ExceptionCandidate `json:"candidates"`
	Status      string               `json:"status"` // ExceptionOpen | ExceptionResolved
	Resolution  string               `json:"resolution"`
	CreatedAt   time.Time            `json:"created_at"`
	ResolvedAt  time.Time            `json:"resolved_at,omitempty"`
}

// ExceptionCandidate is a snapshot of a pending order considered during a
// failed match, retained in MatchException for audit.
type ExceptionCandidate struct {
	ID        string          `json:"id"`
	Amount    decimal.Decimal `json:"amount"`
	CreatedAt time.Time       `json:"created_at"`
}

const (
	ExceptionNoExactMatch = "no_exact_match" // no pending order with the exact amount
	ExceptionRaceLost     = "race_lost"      // order claimed by a concurrent matcher/expiry
	ExceptionAmbiguous    = "ambiguous"      // multiple orders within margin (inert under exact matching)

	ExceptionOpen     = "open"
	ExceptionResolved = "resolved"
)

var (
	ErrNoDepositAddress  = errors.New("no deposit address available")
	ErrCacheMiss         = errors.New("cache miss")
	ErrExceptionNotFound = errors.New("match exception not found")
)

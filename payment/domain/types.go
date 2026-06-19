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

var (
	ErrNoDepositAddress = errors.New("no deposit address available")
	ErrCacheMiss        = errors.New("cache miss")
)

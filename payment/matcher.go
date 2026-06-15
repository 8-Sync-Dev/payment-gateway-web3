package payment

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"encore.dev/rlog"
)

const (
	AllowedMargin = 0.1
	TimeWindow    = 2 * time.Hour
)

type OKXDepositData struct {
	Ccy   string `json:"ccy"`
	Amt   string `json:"amt"`
	State string `json:"state"`
	TxId  string `json:"txId"`
	Ts    string `json:"ts"`
}

func matchOrder(ctx context.Context, deposit OKXDepositData) error {
	if deposit.State != "2" {
		return nil // Not a successful deposit
	}

	depositAmount, err := strconv.ParseFloat(deposit.Amt, 64)
	if err != nil {
		return fmt.Errorf("invalid deposit amount: %w", err)
	}

	tsMs, err := strconv.ParseInt(deposit.Ts, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid deposit timestamp: %w", err)
	}
	depositTime := time.UnixMilli(tsMs)

	var exists bool
	err = db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM transactions WHERE tx_id = $1)", deposit.TxId).Scan(&exists)
	if err != nil {
		return fmt.Errorf("failed to check tx_id existence: %w", err)
	}
	if exists {
		rlog.Info("transaction already processed", "tx_id", deposit.TxId)
		return nil
	}

	rows, err := db.Query(ctx, `
		SELECT id, amount, created_at 
		FROM transactions 
		WHERE status = 'pending' 
		  AND currency = $1 
		  AND created_at >= $2
	`, deposit.Ccy, depositTime.Add(-TimeWindow))

	if err != nil {
		return fmt.Errorf("failed to query pending transactions: %w", err)
	}
	defer rows.Close()

	var bestMatchID string
	var minDiff = math.MaxFloat64
	var bestMatchTimeDiff time.Duration = math.MaxInt64

	for rows.Next() {
		var id string
		var orderAmount float64
		var createdAt time.Time
		if err := rows.Scan(&id, &orderAmount, &createdAt); err != nil {
			rlog.Error("failed to scan row", "err", err)
			continue
		}

		diff := math.Abs(orderAmount - depositAmount)
		if diff <= AllowedMargin {
			timeDiff := depositTime.Sub(createdAt)
			if timeDiff < 0 {
				timeDiff = -timeDiff
			}

			if diff < minDiff || (diff == minDiff && timeDiff < bestMatchTimeDiff) {
				minDiff = diff
				bestMatchTimeDiff = timeDiff
				bestMatchID = id
			}
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating rows: %w", err)
	}

	if bestMatchID == "" {
		rlog.Warn("no matching order found for deposit", "amount", depositAmount, "currency", deposit.Ccy, "tx_id", deposit.TxId)
		return nil
	}

	_, err = db.Exec(ctx, `
		UPDATE transactions
		SET status = 'success', received_amount = $1, tx_id = $2, updated_at = NOW()
		WHERE id = $3 AND status = 'pending'
	`, depositAmount, deposit.TxId, bestMatchID)

	if err != nil {
		return fmt.Errorf("failed to update transaction %s: %w", bestMatchID, err)
	}

	rlog.Info("matched deposit to order successfully", "transaction_id", bestMatchID, "deposit_amount", depositAmount, "tx_id", deposit.TxId)
	return nil
}

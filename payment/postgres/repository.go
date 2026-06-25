package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"encore.app/payment/domain"
	"encore.dev/storage/sqldb"
	"github.com/shopspring/decimal"
)

var _ domain.Repository = (*Repository)(nil)

type Repository struct {
	db *sqldb.Database
}

func New(db *sqldb.Database) *Repository {
	return &Repository{db: db}
}

// CreateTransaction inserts a pending order. inserted=false means the
// (currency, amount) was already held by another pending order — the unique
// partial index is the atomic guard; the caller retries with a fresh amount.
func (r *Repository) CreateTransaction(ctx context.Context, tx *domain.Transaction) (bool, error) {
	tag, err := r.db.Exec(ctx, `
		INSERT INTO transactions (user_id, order_id, amount, currency, deposit_address, status)
		VALUES ($1, $2, $3, $4, $5, 'pending')
		ON CONFLICT (currency, amount) WHERE status = 'pending' DO NOTHING
	`, tx.UserID, tx.OrderID, tx.Amount, tx.Currency, tx.DepositAddress)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *Repository) HasPendingAmount(ctx context.Context, ccy string, amount decimal.Decimal) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM transactions
			WHERE currency = $1 AND amount = $2 AND status = 'pending'
		)
	`, ccy, amount).Scan(&exists)
	return exists, err
}

func (r *Repository) ListPendingForDeposit(ctx context.Context, ccy string, since time.Time) ([]*domain.Transaction, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, amount, created_at
		FROM transactions
		WHERE status = 'pending' AND currency = $1 AND created_at >= $2
	`, ccy, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*domain.Transaction
	for rows.Next() {
		tx := &domain.Transaction{}
		if err := rows.Scan(&tx.ID, &tx.Amount, &tx.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, tx)
	}
	return out, rows.Err()
}

// MarkSuccess returns rowsAffected. The WHERE status='pending' guard makes the
// UPDATE atomic against concurrent matchers — 0 means another matcher won.
func (r *Repository) MarkSuccess(ctx context.Context, id string, receivedAmount decimal.Decimal, txHash string) (int64, error) {
	tag, err := r.db.Exec(ctx, `
		UPDATE transactions
		SET status = 'success', received_amount = $1, tx_id = $2, updated_at = NOW()
		WHERE id = $3 AND status = 'pending'
	`, receivedAmount, txHash, id)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (r *Repository) RecordDeposit(ctx context.Context, d *domain.Deposit) (bool, error) {
	tag, err := r.db.Exec(ctx, `
		INSERT INTO deposits (tx_id, ccy, amt, state, received_at, match_status)
		VALUES ($1, $2, $3, $4, $5, 'unmatched')
		ON CONFLICT (tx_id) DO NOTHING
	`, d.TxID, d.Ccy, d.Amt, d.State, d.Time)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *Repository) MarkDepositMatched(ctx context.Context, txHash, orderID string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE deposits
		SET match_status = 'matched', matched_order_id = $2
		WHERE tx_id = $1
	`, txHash, orderID)
	return err
}

func (r *Repository) CreateMatchException(ctx context.Context, e *domain.MatchException) error {
	candidates, err := json.Marshal(e.Candidates)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx, `
		INSERT INTO match_exceptions (deposit_tx_id, ccy, amt, reason, candidates, status)
		VALUES ($1, $2, $3, $4, $5, 'open')
	`, e.DepositTxID, e.Ccy, e.Amt, e.Reason, candidates)
	return err
}

func (r *Repository) ExpirePendingOrders(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := r.db.Exec(ctx, `
		UPDATE transactions
		SET status = 'expired', updated_at = NOW()
		WHERE status = 'pending' AND created_at < NOW() - make_interval(secs => $1)
	`, olderThan.Seconds())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (r *Repository) ListOpenExceptions(ctx context.Context, limit int) ([]*domain.MatchException, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, deposit_tx_id, ccy, amt, reason, candidates, resolution, created_at, resolved_at
		FROM match_exceptions
		WHERE status = 'open'
		ORDER BY created_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*domain.MatchException
	for rows.Next() {
		var (
			e          domain.MatchException
			candidates []byte
			resolution sql.NullString
			resolvedAt sql.NullTime
		)
		if err := rows.Scan(&e.ID, &e.DepositTxID, &e.Ccy, &e.Amt, &e.Reason, &candidates, &resolution, &e.CreatedAt, &resolvedAt); err != nil {
			return nil, err
		}
		if len(candidates) > 0 {
			_ = json.Unmarshal(candidates, &e.Candidates)
		}
		if resolution.Valid {
			e.Resolution = resolution.String
		}
		if resolvedAt.Valid {
			e.ResolvedAt = resolvedAt.Time
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

func (r *Repository) ResolveMatchException(ctx context.Context, id int64, resolution string) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE match_exceptions
		SET status = 'resolved', resolution = $2, resolved_at = NOW()
		WHERE id = $1 AND status = 'open'
	`, id, resolution)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrExceptionNotFound
	}
	return nil
}

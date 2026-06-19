package postgres

import (
	"context"
	"time"

	"encore.app/payment/domain"
	"encore.dev/storage/sqldb"
)

var _ domain.Repository = (*Repository)(nil)

type Repository struct {
	db *sqldb.Database
}

func New(db *sqldb.Database) *Repository {
	return &Repository{db: db}
}

func (r *Repository) CreateTransaction(ctx context.Context, tx *domain.Transaction) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO transactions (user_id, order_id, amount, currency, deposit_address, status)
		VALUES ($1, $2, $3, $4, $5, 'pending')
	`, tx.UserID, tx.OrderID, tx.Amount, tx.Currency, tx.DepositAddress)
	return err
}

func (r *Repository) HasPendingAmount(ctx context.Context, ccy string, amount float64) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM transactions
			WHERE currency = $1 AND amount = $2 AND status = 'pending'
		)
	`, ccy, amount).Scan(&exists)
	return exists, err
}

func (r *Repository) HasTxHash(ctx context.Context, txHash string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM transactions WHERE tx_id = $1)
	`, txHash).Scan(&exists)
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
func (r *Repository) MarkSuccess(ctx context.Context, id string, receivedAmount float64, txHash string) (int64, error) {
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

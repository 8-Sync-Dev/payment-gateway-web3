// Package payment is the payment gateway service.
//
// initService is the composition root — it constructs concrete adapters
// (okx, postgres, cacheadapter, matcher) and injects them into Service via
// interface-typed fields. Business logic depends only on domain.* interfaces.
package payment

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"encore.app/payment/cacheadapter"
	"encore.app/payment/domain"
	"encore.app/payment/matcher"
	"encore.app/payment/okx"
	"encore.app/payment/postgres"

	"encore.dev/cron"
	"encore.dev/rlog"
	"github.com/shopspring/decimal"
)

type CheckoutRequest struct {
	UserID   string          `json:"user_id"`
	Amount   decimal.Decimal `json:"amount"`
	Currency string          `json:"currency"`
	Chain    string          `json:"chain"`
}

type CheckoutResponse struct {
	OrderID        string          `json:"order_id"`
	DepositAddress string          `json:"deposit_address"`
	Amount         decimal.Decimal `json:"amount"`
	Currency       string          `json:"currency"`
	Status         string          `json:"status"`
}

type GetCurrenciesResponse struct {
	Currencies []domain.Currency `json:"currencies"`
}

type ListExceptionsResponse struct {
	Exceptions []*domain.MatchException `json:"exceptions"`
}

type ResolveExceptionRequest struct {
	ID         int64  `json:"id"`
	Resolution string `json:"resolution"`
}

// svc is set by initService; read by package-level cron wrappers.
var svc *Service

//encore:service
type Service struct {
	provider domain.Provider
	repo     domain.Repository
	cache    domain.CurrencyCache
	matcher  *matcher.Matcher
	ctx      context.Context
	cancel   context.CancelFunc
}

func initService() (*Service, error) {
	ctx, cancel := context.WithCancel(context.Background())

	s := &Service{
		provider: okx.New(okx.Config{
			APIKey:     secrets.OKXApiKey,
			SecretKey:  secrets.OKXSecretKey,
			Passphrase: secrets.OKXPassphrase,
		}),
		repo:    postgres.New(db),
		cache:   cacheadapter.New(supportedCurrenciesKeyspace, supportedCurrenciesCacheKey),
		matcher: matcher.New(matcher.Default()),
		ctx:     ctx,
		cancel:  cancel,
	}
	svc = s

	go s.runDepositListener()
	return s, nil
}

func (s *Service) Shutdown(force context.Context) {
	s.cancel()
	_ = s.provider.Close()
}

//encore:api public method=POST path=/payment/checkout
func (s *Service) Checkout(ctx context.Context, req *CheckoutRequest) (*CheckoutResponse, error) {
	orderID := generateOrderID()

	addr, err := s.provider.GetDepositAddress(ctx, req.Currency, req.Chain)
	if err != nil {
		return nil, err
	}

	const maxRetries = 10
	var finalAmount decimal.Decimal
	for i := 0; i < maxRetries; i++ {
		amount, err := s.dedupAmount(ctx, req.Currency, req.Amount)
		if err != nil {
			return nil, err
		}
		inserted, err := s.repo.CreateTransaction(ctx, &domain.Transaction{
			UserID:         req.UserID,
			OrderID:        orderID,
			Amount:         amount,
			Currency:       req.Currency,
			DepositAddress: addr.Addr,
		})
		if err != nil {
			return nil, err
		}
		if inserted {
			finalAmount = amount
			break
		}
		if i == maxRetries-1 {
			return nil, errors.New("system busy, please try again later")
		}
		// (currency, amount) taken between dedup and insert — retry with a fresh offset.
	}

	return &CheckoutResponse{
		OrderID:        orderID,
		DepositAddress: addr.Addr,
		Amount:         finalAmount,
		Currency:       req.Currency,
		Status:         domain.StatusPending,
	}, nil
}

//encore:api public method=GET path=/payment/currencies
func (s *Service) GetCurrencies(ctx context.Context) (*GetCurrenciesResponse, error) {
	currencies, err := s.cache.Get(ctx)
	if err == nil {
		return &GetCurrenciesResponse{Currencies: currencies}, nil
	}
	if !errors.Is(err, domain.ErrCacheMiss) {
		rlog.Error("cache get failed, falling through to provider", "err", err)
	}

	currencies, err = s.provider.ListSupportedCurrencies(ctx)
	if err != nil {
		return nil, err
	}
	if cacheErr := s.cache.Set(ctx, currencies); cacheErr != nil {
		rlog.Warn("cache set failed", "err", cacheErr)
	}
	return &GetCurrenciesResponse{Currencies: currencies}, nil
}

//encore:api private
func (s *Service) SyncCurrencies(ctx context.Context) error {
	currencies, err := s.provider.ListSupportedCurrencies(ctx)
	if err != nil {
		return err
	}
	return s.cache.Set(ctx, currencies)
}

// handleDeposit records every finalized deposit into the immutable ledger,
// then matches by exact amount. Deposits with no exact-amount order, or that
// lose a claim race, are routed to the review queue instead of being dropped.
// Idempotent on tx_id (ledger ON CONFLICT DO NOTHING).
func (s *Service) handleDeposit(ctx context.Context, deposit domain.Deposit) error {
	if deposit.State != "2" {
		return nil
	}

	inserted, err := s.repo.RecordDeposit(ctx, &deposit)
	if err != nil {
		return err
	}
	if !inserted {
		return nil // already processed
	}

	candidates, err := s.repo.ListPendingForDeposit(ctx, deposit.Ccy, s.matcher.Since(deposit.Time))
	if err != nil {
		return err
	}

	best := s.matcher.FindBestMatch(deposit, candidates)
	if best == nil {
		if err := s.repo.CreateMatchException(ctx, &domain.MatchException{
			DepositTxID: deposit.TxID,
			Ccy:         deposit.Ccy,
			Amt:         deposit.Amt,
			Reason:      domain.ExceptionNoExactMatch,
			Candidates:  toExceptionCandidates(candidates),
		}); err != nil {
			rlog.Error("failed to record no-match exception", "err", err, "tx_id", deposit.TxID)
		}
		rlog.Warn("deposit has no exact-amount order",
			"amount", deposit.Amt, "ccy", deposit.Ccy, "tx_id", deposit.TxID)
		return nil
	}

	rows, err := s.repo.MarkSuccess(ctx, best.ID, deposit.Amt, deposit.TxID)
	if err != nil {
		return err
	}
	if rows == 0 {
		if err := s.repo.CreateMatchException(ctx, &domain.MatchException{
			DepositTxID: deposit.TxID,
			Ccy:         deposit.Ccy,
			Amt:         deposit.Amt,
			Reason:      domain.ExceptionRaceLost,
			Candidates:  toExceptionCandidates(candidates),
		}); err != nil {
			rlog.Error("failed to record race-lost exception", "err", err, "tx_id", deposit.TxID)
		}
		rlog.Warn("best match already claimed",
			"order_id", best.ID, "deposit_tx", deposit.TxID)
		return nil
	}

	if err := s.repo.MarkDepositMatched(ctx, deposit.TxID, best.ID); err != nil {
		rlog.Warn("failed to flag deposit as matched", "err", err, "tx_id", deposit.TxID)
	}
	rlog.Info("matched deposit to order",
		"order_id", best.ID, "amount", deposit.Amt, "tx_id", deposit.TxID)
	return nil
}

func toExceptionCandidates(txs []*domain.Transaction) []domain.ExceptionCandidate {
	out := make([]domain.ExceptionCandidate, 0, len(txs))
	for _, tx := range txs {
		out = append(out, domain.ExceptionCandidate{
			ID:        tx.ID,
			Amount:    tx.Amount,
			CreatedAt: tx.CreatedAt,
		})
	}
	return out
}

func (s *Service) runDepositListener() {
	if err := s.provider.SubscribeDeposits(s.ctx, s.handleDeposit); err != nil {
		rlog.Error("deposit listener exited", "err", err)
	}
}

// dedupAmount perturbs base by a small offset so a pending order's
// (currency, amount) is likely unique. The unique partial index
// transactions_pending_amount_uidx is the authoritative guard; Checkout retries
// CreateTransaction on the rare residual collision.
func (s *Service) dedupAmount(ctx context.Context, ccy string, base decimal.Decimal) (decimal.Decimal, error) {
	exists, err := s.repo.HasPendingAmount(ctx, ccy, base)
	if err != nil {
		return decimal.Zero, err
	}
	if !exists {
		return base, nil
	}
	offset, err := randomAmountOffset()
	if err != nil {
		return decimal.Zero, err
	}
	return base.Add(offset).Round(4), nil
}

func randomAmountOffset() (decimal.Decimal, error) {
	b := make([]byte, 2)
	if _, err := rand.Read(b); err != nil {
		return decimal.Zero, err
	}
	val := uint16(b[0])<<8 | uint16(b[1])
	return decimal.New(int64((val%999)+1), -4), nil // [0.0001, 0.0999]
}

func generateOrderID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Service) ExpirePendingOrders(ctx context.Context) (int64, error) {
	return s.repo.ExpirePendingOrders(ctx, matcher.Default().TimeWindow)
}

//encore:api private method=GET path=/payment/exceptions
func (s *Service) ListExceptions(ctx context.Context) (*ListExceptionsResponse, error) {
	excs, err := s.repo.ListOpenExceptions(ctx, 100)
	if err != nil {
		return nil, err
	}
	return &ListExceptionsResponse{Exceptions: excs}, nil
}

//encore:api private method=POST path=/payment/exceptions/resolve
func (s *Service) ResolveException(ctx context.Context, req *ResolveExceptionRequest) error {
	return s.repo.ResolveMatchException(ctx, req.ID, req.Resolution)
}

var _ = cron.NewJob("sync-okx-currencies", cron.JobConfig{
	Title:    "Sync OKX Currencies",
	Endpoint: syncOKXCurrenciesCron,
	Every:    15 * cron.Minute,
})

//encore:api private
func syncOKXCurrenciesCron(ctx context.Context) error {
	return svc.SyncCurrencies(ctx)
}

var _ = cron.NewJob("expire-pending-orders", cron.JobConfig{
	Title:    "Expire pending orders past the match window",
	Endpoint: expirePendingOrdersCron,
	Every:    1 * cron.Hour,
})

//encore:api private
func expirePendingOrdersCron(ctx context.Context) error {
	n, err := svc.ExpirePendingOrders(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		rlog.Info("expired pending orders", "count", n)
	}
	return nil
}

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

	finalAmount, err := s.dedupAmount(ctx, req.Currency, req.Amount)
	if err != nil {
		return nil, err
	}

	if err := s.repo.CreateTransaction(ctx, &domain.Transaction{
		UserID:         req.UserID,
		OrderID:        orderID,
		Amount:         finalAmount,
		Currency:       req.Currency,
		DepositAddress: addr.Addr,
	}); err != nil {
		return nil, err
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

// handleDeposit is idempotent — WS at-least-once delivery may redeliver.
func (s *Service) handleDeposit(ctx context.Context, deposit domain.Deposit) error {
	if deposit.State != "2" {
		return nil
	}

	seen, err := s.repo.HasTxHash(ctx, deposit.TxID)
	if err != nil {
		return err
	}
	if seen {
		return nil
	}

	candidates, err := s.repo.ListPendingForDeposit(ctx, deposit.Ccy, s.matcher.Since(deposit.Time))
	if err != nil {
		return err
	}

	best := s.matcher.FindBestMatch(deposit, candidates)
	if best == nil {
		rlog.Warn("no matching order for deposit",
			"amount", deposit.Amt, "ccy", deposit.Ccy, "tx_id", deposit.TxID)
		return nil
	}

	rows, err := s.repo.MarkSuccess(ctx, best.ID, deposit.Amt, deposit.TxID)
	if err != nil {
		return err
	}
	if rows == 0 {
		rlog.Warn("best match already claimed",
			"order_id", best.ID, "deposit_tx", deposit.TxID)
		return nil
	}

	rlog.Info("matched deposit to order",
		"order_id", best.ID, "amount", deposit.Amt, "tx_id", deposit.TxID)
	return nil
}

func (s *Service) runDepositListener() {
	if err := s.provider.SubscribeDeposits(s.ctx, s.handleDeposit); err != nil {
		rlog.Error("deposit listener exited", "err", err)
	}
}

// dedupAmount keeps amounts unique among pending orders. NOTE: still has
// SELECT-then-INSERT race (trace error.md C1).
func (s *Service) dedupAmount(ctx context.Context, ccy string, base decimal.Decimal) (decimal.Decimal, error) {
	const maxRetries = 10
	amount := base
	for i := 0; i < maxRetries; i++ {
		exists, err := s.repo.HasPendingAmount(ctx, ccy, amount)
		if err != nil {
			return decimal.Zero, err
		}
		if !exists {
			return amount, nil
		}
		if i == maxRetries-1 {
			return decimal.Zero, errors.New("system busy, please try again later")
		}
		b := make([]byte, 2)
		if _, err := rand.Read(b); err != nil {
			return decimal.Zero, err
		}
		val := uint16(b[0])<<8 | uint16(b[1])
		offset := decimal.New(int64((val%999)+1), -4) // offsetInt × 10^-4, exact
		amount = base.Add(offset).Round(4)
	}
	return amount, nil
}

func generateOrderID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
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

// Package okx implements domain.Provider against the OKX V5 API.
package okx

import (
	"context"
	"fmt"
	"time"

	"encore.app/payment/domain"
)

type Config struct {
	APIKey     string
	SecretKey  string
	Passphrase string
}

var _ domain.Provider = (*Provider)(nil)

type Provider struct {
	cfg  Config
	rest *restClient
	ws   *wsSubscriber
}

func New(cfg Config) *Provider {
	return &Provider{
		cfg:  cfg,
		rest: newRESTClient(cfg),
		ws:   newWSSubscriber(cfg),
	}
}

func (p *Provider) Name() string { return "okx" }

// GetDepositAddress returns the address for (ccy, chain).
// If chain is empty, returns the first address OKX lists for ccy.
func (p *Provider) GetDepositAddress(ctx context.Context, ccy, chain string) (*domain.DepositAddress, error) {
	rows, err := p.rest.fetchDepositAddress(ctx, ccy)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if chain == "" || r.Chain == chain {
			return &domain.DepositAddress{Addr: r.Addr, Chain: r.Chain, Ccy: r.Ccy}, nil
		}
	}
	return nil, fmt.Errorf("%w: ccy=%s chain=%s", domain.ErrNoDepositAddress, ccy, chain)
}

func (p *Provider) ListSupportedCurrencies(ctx context.Context) ([]domain.Currency, error) {
	rows, err := p.rest.fetchCurrencies(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Currency, 0, len(rows))
	for _, r := range rows {
		if !r.CanDep {
			continue
		}
		out = append(out, domain.Currency{
			Ccy: r.Ccy, Name: r.Name, Chain: r.Chain, MinDep: r.MinDep, LogoLink: r.LogoLink,
		})
	}
	return out, nil
}

// SubscribeDeposits runs the OKX WS stream, invoking handler per deposit.
// Reconnects with fixed backoff until ctx cancelled.
func (p *Provider) SubscribeDeposits(ctx context.Context, handler domain.DepositHandler) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := p.ws.Run(ctx, handler)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			time.Sleep(p.ws.reconnectBackoff)
		}
	}
}

func (p *Provider) Close() error { return nil }

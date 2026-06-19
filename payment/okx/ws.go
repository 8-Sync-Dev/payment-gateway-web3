package okx

import (
	"context"
	"encoding/json"
	"net/url"
	"time"

	"encore.app/payment/domain"
	"encore.dev/rlog"

	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"
)

type wsSubscriber struct {
	cfg              Config
	reconnectBackoff time.Duration
}

func newWSSubscriber(cfg Config) *wsSubscriber {
	return &wsSubscriber{cfg: cfg, reconnectBackoff: 5 * time.Second}
}

type wsDeposit struct {
	Ccy   string `json:"ccy"`
	Amt   string `json:"amt"`
	State string `json:"state"`
	TxID  string `json:"txId"`
	Ts    string `json:"ts"`
}

type wsFrame struct {
	Event string            `json:"event"`
	Arg   map[string]string `json:"arg"`
	Data  []wsDeposit       `json:"data"`
}

// Run connects, logs in, subscribes to deposit-info, and forwards events.
// Blocks until ctx cancelled or unrecoverable error.
func (s *wsSubscriber) Run(ctx context.Context, handler domain.DepositHandler) error {
	u := url.URL{Scheme: "wss", Host: "ws.okx.com:8443", Path: "/ws/v5/business"}

	c, _, err := websocket.DefaultDialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return err
	}
	defer c.Close()

	if err := s.loginAndSubscribe(ctx, c); err != nil {
		return err
	}

	// OKX requires a ping every <=30s.
	pingTicker := time.NewTicker(20 * time.Second)
	defer pingTicker.Stop()
	pingDone := make(chan struct{})
	defer close(pingDone)

	go func() {
		for {
			select {
			case <-pingDone:
				return
			case <-pingTicker.C:
				if err := c.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
					return
				}
			}
		}
	}()

	for {
		if ctx.Err() != nil {
			return nil
		}
		_, message, err := c.ReadMessage()
		if err != nil {
			return err
		}
		if string(message) == "pong" {
			continue
		}

		var frame wsFrame
		if err := json.Unmarshal(message, &frame); err != nil {
			continue // non-deposit push (subscribe ack, etc.)
		}
		if frame.Arg == nil || frame.Arg["channel"] != "deposit-info" || len(frame.Data) == 0 {
			continue
		}
		for _, d := range frame.Data {
			if err := handler(ctx, toDomainDeposit(d)); err != nil {
				rlog.Error("okx ws: deposit handler failed", "err", err, "tx_id", d.TxID)
			}
		}
	}
}

func (s *wsSubscriber) loginAndSubscribe(ctx context.Context, c *websocket.Conn) error {
	ts := wsTimestamp()
	signature := sign(ts, "GET", "/users/self/verify", "", s.cfg.SecretKey)

	login := map[string]any{
		"op": "login",
		"args": []map[string]string{{
			"apiKey":     s.cfg.APIKey,
			"passphrase": s.cfg.Passphrase,
			"timestamp":  ts,
			"sign":       signature,
		}},
	}
	if err := c.WriteJSON(login); err != nil {
		return err
	}
	if _, _, err := c.ReadMessage(); err != nil {
		return err
	}

	sub := map[string]any{
		"op":   "subscribe",
		"args": []map[string]string{{"channel": "deposit-info"}},
	}
	return c.WriteJSON(sub)
}

func toDomainDeposit(d wsDeposit) domain.Deposit {
	return domain.Deposit{
		Ccy:   d.Ccy,
		Amt:   parseDecimal(d.Amt),
		State: d.State,
		TxID:  d.TxID,
		Time:  parseMillis(d.Ts),
	}
}

func parseDecimal(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		rlog.Warn("okx ws: failed to parse amount", "raw", s, "err", err)
		return decimal.Zero
	}
	return d
}

func parseMillis(s string) time.Time {
	var ms int64
	_ = json.Unmarshal([]byte(s), &ms)
	return time.UnixMilli(ms)
}

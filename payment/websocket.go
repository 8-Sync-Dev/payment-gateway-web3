package payment

import (
	"context"
	"encoding/json"
	"net/url"
	"time"

	"encore.dev/rlog"
	"github.com/gorilla/websocket"
)

//encore:service
type Service struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func initService() (*Service, error) {
	ctx, cancel := context.WithCancel(context.Background())
	svc := &Service{
		ctx:    ctx,
		cancel: cancel,
	}

	go svc.startWebSocket()

	return svc, nil
}

func (s *Service) Shutdown(force context.Context) {
	s.cancel()
}

func (s *Service) startWebSocket() {
	u := url.URL{Scheme: "wss", Host: "ws.okx.com:8443", Path: "/ws/v5/business"}

	for {
		if s.ctx.Err() != nil {
			break
		}

		rlog.Info("connecting to OKX websocket", "url", u.String())
		c, _, err := websocket.DefaultDialer.DialContext(s.ctx, u.String(), nil)
		if err != nil {
			rlog.Error("websocket dial failed, retrying in 5s", "err", err)
			select {
			case <-time.After(5 * time.Second):
			case <-s.ctx.Done():
				return
			}
			continue
		}

		err = s.handleConnection(c)
		c.Close()

		if err != nil {
			rlog.Error("websocket connection closed with error, reconnecting in 5s", "err", err)
			select {
			case <-time.After(5 * time.Second):
			case <-s.ctx.Done():
				return
			}
		} else {
			if s.ctx.Err() != nil {
				return
			}
		}
	}
}

func (s *Service) handleConnection(c *websocket.Conn) error {
	timestamp := GenerateOKXWSTimestamp()
	sign := GenerateOKXSignature(timestamp, "GET", "/users/self/verify", "", secrets.OKXSecretKey)

	loginMsg := map[string]interface{}{
		"op": "login",
		"args": []map[string]string{
			{
				"apiKey":     secrets.OKXApiKey,
				"passphrase": secrets.OKXPassphrase,
				"timestamp":  timestamp,
				"sign":       sign,
			},
		},
	}
	if err := c.WriteJSON(loginMsg); err != nil {
		return err
	}

	_, msg, err := c.ReadMessage()
	if err != nil {
		return err
	}
	rlog.Info("OKX WS login response", "msg", string(msg))

	// 2. Subscribe to deposit-info
	subMsg := map[string]interface{}{
		"op": "subscribe",
		"args": []map[string]string{
			{
				"channel": "deposit-info",
			},
		},
	}
	if err := c.WriteJSON(subMsg); err != nil {
		return err
	}

	pingTicker := time.NewTicker(20 * time.Second)
	defer pingTicker.Stop()

	connCtx, connCancel := context.WithCancel(s.ctx)
	defer connCancel()

	go func() {
		for {
			select {
			case <-connCtx.Done():
				return
			case <-pingTicker.C:
				if err := c.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
					return
				}
			}
		}
	}()

	for {
		if s.ctx.Err() != nil {
			return nil
		}

		_, message, err := c.ReadMessage()
		if err != nil {
			return err
		}

		// OKX heartbeat response
		if string(message) == "pong" {
			continue
		}

		var payload struct {
			Event string            `json:"event"`
			Arg   map[string]string `json:"arg"`
			Data  []OKXDepositData  `json:"data"`
		}

		if err := json.Unmarshal(message, &payload); err != nil {
			// Not a deposit push, could be pong or sub success msg, skip for now
			continue
		}

		if payload.Arg != nil && payload.Arg["channel"] == "deposit-info" && len(payload.Data) > 0 {
			for _, dep := range payload.Data {
				if err := matchOrder(s.ctx, dep); err != nil {
					rlog.Error("failed to match order", "err", err, "tx_id", dep.TxId)
				}
			}
		}
	}
}

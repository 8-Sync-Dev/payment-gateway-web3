package payment

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"encore.dev/storage/sqldb"
)

var db = sqldb.NewDatabase("payment", sqldb.DatabaseConfig{
	Migrations: "./migrations",
})

var secrets struct {
	OKXApiKey     string
	OKXSecretKey  string
	OKXPassphrase string
}

type CheckoutRequest struct {
	UserID   string  `json:"user_id"`
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
	Chain    string  `json:"chain"`
}

type CheckoutResponse struct {
	OrderID        string  `json:"order_id"`
	DepositAddress string  `json:"deposit_address"`
	Amount         float64 `json:"amount"`
	Currency       string  `json:"currency"`
	Status         string  `json:"status"`
}

type OKXDepositAddressResponse struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Data []struct {
		Addr  string `json:"addr"`
		Chain string `json:"chain"`
		Ccy   string `json:"ccy"`
	} `json:"data"`
}

// generateOrderID creates a 32-character secure random hex string for the unique order ID
func generateOrderID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Checkout creates a new crypto payment request and fetches a deposit address from OKX V5
//
//encore:api public method=POST path=/payment/checkout
func Checkout(ctx context.Context, req *CheckoutRequest) (*CheckoutResponse, error) {
	orderID := generateOrderID()

	// 1. Fetch deposit address from OKX API
	requestPath := fmt.Sprintf("/api/v5/asset/deposit-address?ccy=%s", req.Currency)
	fullURL := "https://openapi.okx.com" + requestPath

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	timestamp := GenerateOKXTimestamp()
	method := "GET"
	bodyStr := ""

	apiKey := secrets.OKXApiKey
	secretKey := secrets.OKXSecretKey
	passphrase := secrets.OKXPassphrase

	sign := GenerateOKXSignature(timestamp, method, requestPath, bodyStr, secretKey)

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("OK-ACCESS-KEY", apiKey)
	httpReq.Header.Set("OK-ACCESS-PASSPHRASE", passphrase)
	httpReq.Header.Set("OK-ACCESS-TIMESTAMP", timestamp)
	httpReq.Header.Set("OK-ACCESS-SIGN", sign)

	client := &http.Client{Timeout: 10 * time.Second}
	httpResp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("okx api request failed: %w", err)
	}
	defer httpResp.Body.Close()

	bodyBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var okxResp OKXDepositAddressResponse
	if err := json.Unmarshal(bodyBytes, &okxResp); err != nil {
		return nil, fmt.Errorf("failed to parse okx response: %w, body: %s", err, string(bodyBytes))
	}

	if okxResp.Code != "0" {
		return nil, fmt.Errorf("okx returned error: code=%s, msg=%s", okxResp.Code, okxResp.Msg)
	}

	var depositAddress string
	for _, data := range okxResp.Data {
		if req.Chain != "" {
			if data.Chain == req.Chain {
				depositAddress = data.Addr
				break
			}
		} else {
			depositAddress = data.Addr
			break
		}
	}

	if depositAddress == "" {
		return nil, fmt.Errorf("no deposit address found for currency %s and chain %s", req.Currency, req.Chain)
	}

	finalAmount := req.Amount
	maxRetries := 10
	for i := 0; i < maxRetries; i++ {
		var exists bool
		err = db.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM transactions 
				WHERE currency = $1 AND amount = $2 AND status = 'pending'
			)
		`, req.Currency, finalAmount).Scan(&exists)
		if err != nil {
			return nil, fmt.Errorf("failed to check existing amount: %w", err)
		}

		if !exists {
			break
		}

		if i == maxRetries-1 {
			return nil, fmt.Errorf("system is busy, please try again later")
		}

		b := make([]byte, 2)
		rand.Read(b)
		val := uint16(b[0])<<8 | uint16(b[1])
		offset := float64((val%999)+1) / 10000.0

		finalAmount = math.Round((req.Amount+offset)*10000) / 10000
	}

	_, err = db.Exec(ctx, `
		INSERT INTO transactions (user_id, order_id, amount, currency, deposit_address, status)
		VALUES ($1, $2, $3, $4, $5, 'pending')
	`, req.UserID, orderID, finalAmount, req.Currency, depositAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to insert transaction: %w", err)
	}

	return &CheckoutResponse{
		OrderID:        orderID,
		DepositAddress: depositAddress,
		Amount:         finalAmount,
		Currency:       req.Currency,
		Status:         "pending",
	}, nil
}

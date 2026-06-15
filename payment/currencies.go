package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type CurrencyInfo struct {
	Ccy      string `json:"ccy"`
	Name     string `json:"name"`
	Chain    string `json:"chain"`
	MinDep   string `json:"minDep"`
	LogoLink string `json:"logoLink"`
}

type GetCurrenciesResponse struct {
	Currencies []CurrencyInfo `json:"currencies"`
}

type OKXCurrenciesResponse struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Data []struct {
		Ccy      string `json:"ccy"`
		Name     string `json:"name"`
		Chain    string `json:"chain"`
		CanDep   bool   `json:"canDep"`
		MinDep   string `json:"minDep"`
		LogoLink string `json:"logoLink"`
	} `json:"data"`
}

//encore:api public method=GET path=/payment/currencies
func GetCurrencies(ctx context.Context) (*GetCurrenciesResponse, error) {
	requestPath := "/api/v5/asset/currencies"
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

	var okxResp OKXCurrenciesResponse
	if err := json.Unmarshal(bodyBytes, &okxResp); err != nil {
		return nil, fmt.Errorf("failed to parse okx response: %w, body: %s", err, string(bodyBytes))
	}

	if okxResp.Code != "0" {
		return nil, fmt.Errorf("okx returned error: code=%s, msg=%s", okxResp.Code, okxResp.Msg)
	}

	var result []CurrencyInfo
	for _, c := range okxResp.Data {
		if c.CanDep {
			result = append(result, CurrencyInfo{
				Ccy:      c.Ccy,
				Name:     c.Name,
				Chain:    c.Chain,
				MinDep:   c.MinDep,
				LogoLink: c.LogoLink,
			})
		}
	}

	return &GetCurrenciesResponse{
		Currencies: result,
	}, nil
}

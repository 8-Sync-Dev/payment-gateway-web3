package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"encore.dev/storage/cache"
)

var httpClient = &http.Client{Timeout: 10 * time.Second}

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

func fetchOKXCurrencies(ctx context.Context) ([]CurrencyInfo, error) {
	requestPath := "/api/v5/asset/currencies"
	fullURL := "https://openapi.okx.com" + requestPath

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	timestamp := GenerateOKXTimestamp()
	sign := GenerateOKXSignature(timestamp, http.MethodGet, requestPath, "", secrets.OKXSecretKey)

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("OK-ACCESS-KEY", secrets.OKXApiKey)
	httpReq.Header.Set("OK-ACCESS-PASSPHRASE", secrets.OKXPassphrase)
	httpReq.Header.Set("OK-ACCESS-TIMESTAMP", timestamp)
	httpReq.Header.Set("OK-ACCESS-SIGN", sign)

	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("okx api request failed: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("okx api returned non-200 status: %d", httpResp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var okxResp OKXCurrenciesResponse
	if err := json.Unmarshal(bodyBytes, &okxResp); err != nil {
		return nil, fmt.Errorf("failed to parse okx response: %w", err)
	}

	if okxResp.Code != "0" {
		return nil, fmt.Errorf("okx returned error: code=%s, msg=%s", okxResp.Code, okxResp.Msg)
	}

	result := make([]CurrencyInfo, 0, len(okxResp.Data))
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

	return result, nil
}

func syncSupportedCurrenciesCache(ctx context.Context) ([]CurrencyInfo, error) {
	currencies, err := fetchOKXCurrencies(ctx)
	if err != nil {
		return nil, err
	}

	payload := CachedCurrencies{Currencies: currencies}
	if err := SupportedCurrenciesCache.Set(ctx, supportedCurrenciesCacheKey, payload); err != nil {
		return nil, fmt.Errorf("failed to save currencies to cache: %w", err)
	}

	return currencies, nil
}

func getCachedCurrencies(ctx context.Context) ([]CurrencyInfo, error) {
	payload, err := SupportedCurrenciesCache.Get(ctx, supportedCurrenciesCacheKey)
	if err != nil {
		return nil, err
	}

	return payload.Currencies, nil
}

//encore:api public method=GET path=/payment/currencies
func GetCurrencies(ctx context.Context) (*GetCurrenciesResponse, error) {
	currencies, err := getCachedCurrencies(ctx)
	if err == nil {
		return &GetCurrenciesResponse{Currencies: currencies}, nil
	}

	if errors.Is(err, cache.Miss) {
		currencies, err = syncSupportedCurrenciesCache(ctx)
		if err != nil {
			return nil, err
		}
		return &GetCurrenciesResponse{Currencies: currencies}, nil
	}

	currencies, syncErr := syncSupportedCurrenciesCache(ctx)
	if syncErr != nil {
		return nil, fmt.Errorf("failed to read currencies from cache: %w", err)
	}

	return &GetCurrenciesResponse{Currencies: currencies}, nil
}

package okx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type restClient struct {
	cfg    Config
	httpDo *http.Client
}

func newRESTClient(cfg Config) *restClient {
	return &restClient{cfg: cfg, httpDo: &http.Client{Timeout: 10 * time.Second}}
}

type okxEnvelope struct {
	Code string          `json:"code"` // "0" = success
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func (c *restClient) doGET(ctx context.Context, requestPath string, out any) error {
	const method = http.MethodGet
	fullURL := "https://openapi.okx.com" + requestPath

	req, err := http.NewRequestWithContext(ctx, method, fullURL, nil)
	if err != nil {
		return fmt.Errorf("okx: build request: %w", err)
	}

	ts := restTimestamp()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OK-ACCESS-KEY", c.cfg.APIKey)
	req.Header.Set("OK-ACCESS-PASSPHRASE", c.cfg.Passphrase)
	req.Header.Set("OK-ACCESS-TIMESTAMP", ts)
	req.Header.Set("OK-ACCESS-SIGN", sign(ts, method, requestPath, "", c.cfg.SecretKey))

	resp, err := c.httpDo.Do(req)
	if err != nil {
		return fmt.Errorf("okx: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("okx: http %d: %s", resp.StatusCode, string(body))
	}

	var env okxEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("okx: decode response: %w", err)
	}
	if env.Code != "0" {
		return fmt.Errorf("okx: code=%s msg=%s", env.Code, env.Msg)
	}
	if out != nil {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("okx: decode data: %w", err)
		}
	}
	return nil
}

type addressRow struct {
	Addr  string `json:"addr"`
	Chain string `json:"chain"`
	Ccy   string `json:"ccy"`
}

func (c *restClient) fetchDepositAddress(ctx context.Context, ccy string) ([]addressRow, error) {
	var rows []addressRow
	err := c.doGET(ctx, fmt.Sprintf("/api/v5/asset/deposit-address?ccy=%s", ccy), &rows)
	return rows, err
}

type currencyRow struct {
	Ccy      string `json:"ccy"`
	Name     string `json:"name"`
	Chain    string `json:"chain"`
	CanDep   bool   `json:"canDep"`
	MinDep   string `json:"minDep"`
	LogoLink string `json:"logoLink"`
}

func (c *restClient) fetchCurrencies(ctx context.Context) ([]currencyRow, error) {
	var rows []currencyRow
	err := c.doGET(ctx, "/api/v5/asset/currencies", &rows)
	return rows, err
}

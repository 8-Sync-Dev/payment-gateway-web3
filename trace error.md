# Trace Error — payment-gateway-web3

Danh sách issue còn mở sau refactor kiến trúc hexagonal (2026-06-19).
Mỗi issue ghi: vị trí, mô tả, kịch bản tái lập, tác động nghiệp vụ, fix đề xuất.

Đánh giá severity:
- 🔴 **CRITICAL** — sai logic nghiệp vụ / mất dữ liệu / sai khớp tiền
- 🟠 **HIGH** — khoảng trống nghiệp vụ, vận hành sẽ nổ khi scale
- 🟡 **MEDIUM** — robustness, edge case, ops khó chịu
- 🟢 **LOW** — code quality, maintainability

---

## ✅ Đã giải quyết qua refactor kiến trúc (2026-06-19)

Refactor tách code thành 5 sub-package (`domain/`, `okx/`, `postgres/`, `cacheadapter/`, `matcher/`) với constructor injection đã tự khắc phục các issue sau:

| Issue cũ | Cách giải quyết |
|---|---|
| ~~C2. Silent fail matcher~~ | `postgres.Repository.MarkSuccess` giờ trả `(int64, error)`; `payment.handleDeposit` check `rows == 0` và log warning + structured fields |
| ~~M1. `OKXConfig.IsDemo` dead field~~ | Struct cũ xóa hoàn toàn; `okx.Config` mới chỉ có `APIKey`, `SecretKey`, `Passphrase` |
| ~~M4. HTTP client không reuse~~ | `okx.restClient` có shared `*http.Client` khởi tạo 1 lần ở constructor, dùng cho mọi REST call |
| ~~L3. Secrets struct thiếu doc~~ | `payment/resources.go` có doc comment kèm lệnh `encore secret set --type ...` cho 3 credential |

Bonus: kiến trúc giờ hỗ trợ test (matcher là pure function, repository có interface) — nhưng chưa có file `*_test.go` thực tế (xem L5).

---

## 🔴 CRITICAL

### C1. Race condition trong amount fingerprinting

**Vị trí**: `payment/payment.go` — method `(*Service).dedupAmount`

**Mô tả**: Flow checkout là `HasPendingAmount` (SELECT EXISTS) → tính offset → `CreateTransaction` (INSERT). Giữa SELECT và INSERT không có lock, không có transaction, không có unique constraint trên `(currency, amount, status)`.

```go
// payment/payment.go (method dedupAmount)
exists, err := s.repo.HasPendingAmount(ctx, ccy, amount)
if err != nil { return 0, err }
if !exists {
    return amount, nil   // ← race window ở đây
}
// ... tính offset mới ...
// ... sau đó Checkout mới gọi s.repo.CreateTransaction ...
```

**Kịch bản tái lập**:
1. T=0ms: Request A và Request B đến đồng thời, cùng `{amount: 100, currency: USDT}`
2. T=10ms: A `HasPendingAmount` → false (chưa có ai)
3. T=11ms: B `HasPendingAmount` → false (A chưa INSERT)
4. T=20ms: A `CreateTransaction` amount=100.0000
5. T=21ms: B `CreateTransaction` amount=100.0000 → **thành công, vì không có unique constraint**
6. Khi deposit 100.0000 đến → matcher không phân biệt được 2 order

**Tác động**: thanh toán bị gán nhầm order. Khách A trả tiền, đơn hàng của B được confirm. Mất tiền khách, mất uy tín.

**Fix đề xuất** (chọn 1):

- **Option A — DB constraint** (khuyến nghị, đơn giản nhất):
  ```sql
  -- migration 3
  CREATE UNIQUE INDEX transactions_pending_amount_uniq
    ON transactions (currency, amount)
    WHERE status = 'pending';
  ```
  Trong `postgres.CreateTransaction`, bắt error `pgcode.UniqueViolation` rồi retry với offset mới (đẩy retry loop từ service xuống repo, hoặc để service bắt error và gọi lại `dedupAmount`).

- **Option B — SELECT FOR UPDATE trong transaction**:
  Đòi hỏi Encore's sqldb mở hộp transaction block (hiện mỗi call là 1 connection tự pool). Phức tạp hơn.

- **Option C — luôn add offset**: bỏ check `HasPendingAmount`, mỗi order luôn có fingerprint random → không bao giờ trùng, đơn giản hóa logic.

---

### C3. Không có reconciliation — WS miss event = mất tiền vĩnh viễn

**Vị trí**: không có. WS subscriber ở `payment/okx/ws.go`, không có cron bù.

**Mô tả**: WebSocket là best-effort delivery. Trong các trường hợp sau, deposit event bị miss:
- App restart đúng lúc OKX push event
- WS reconnect window (5s backoff ở `okx/provider.go SubscribeDeposits`) có event đến
- OKX WS bị sự cố, push không đến
- Network partition

Hiện `okx.Provider` chưa expose method `ListDeposits(ctx, since)` để bù — cần thêm vào interface `domain.Provider`.

**Kịch bản tái lập**:
1. App deploy lại, downtime 30 giây
2. Trong 30 giây đó, user K trả 100 USDT, OKX confirm, push event
3. App lên → WS reconnect → không nhận lại event cũ
4. Tiền của K ở trong OKX, không có order nào confirm

**Tác động**: mất tiền khách hàng thầm lặng. Đặc biệt nguy hiểm vì không có alert.

**Fix đề xuất** (3 bước):

1. Mở rộng `domain.Provider` interface:
   ```go
   type Provider interface {
       // ... existing methods ...
       ListDeposits(ctx context.Context, since time.Time) ([]Deposit, error)
   }
   ```

2. Implement trong `okx.Provider` (gọi `GET /api/v5/asset/deposit-history?after=<ts>`)

3. Thêm cron reconciliation:
   ```go
   // payment/reconciliation.go (file mới)
   var _ = cron.NewJob("reconcile-deposits", cron.JobConfig{
       Title:    "Reconcile OKX deposits",
       Endpoint: reconcileDepositsCron,
       Every:    1 * cron.Hour,
   })

   //encore:api private
   func reconcileDepositsCron(ctx context.Context) error {
       return svc.ReconcileDeposits(ctx)
   }

   func (s *Service) ReconcileDeposits(ctx context.Context) error {
       since := time.Now().Add(-2 * time.Hour)
       deposits, err := s.provider.ListDeposits(ctx, since)
       if err != nil { return err }
       for _, d := range deposits {
           if err := s.handleDeposit(ctx, d); err != nil {
               rlog.Error("reconcile: deposit handler failed", "err", err, "tx_id", d.TxID)
           }
       }
       return nil
   }
   ```
   `handleDeposit` đã idempotent (check `HasTxHash`) nên chạy lại an toàn.

---

### C4. AllowedMargin quá lớn so với fingerprint range

**Vị trí**: `payment/matcher/matcher.go` — `Default()` config

```go
func Default() Config {
    return Config{
        AllowedMargin: 0.1,        // ← margin chấp nhận khi match
        TimeWindow:    2 * time.Hour,
    }
}
```

**Mô tả**: 
- Fingerprint (`payment.go dedupAmount`) thêm offset `0.0001 - 0.0999`
- `AllowedMargin = 0.1`

Vì margin (0.1) ≥ max fingerprint (0.0999), **2 order khác nhau có thể cùng match 1 deposit**.

**Kịch bản tái lập**:
1. Order A: amount=100.00 (user muốn trả chẵn, không trùng pending nào)
2. Order B: amount=100.05 (fingerprint = 0.05 vì đã có A)
3. Deposit D: amount=100.03
4. diff(A, D) = 0.03 ≤ 0.1 ✓
5. diff(B, D) = 0.02 ≤ 0.1 ✓
6. Matcher pick B (diff nhỏ hơn) → A bị kẹt mãi

Trường hợp tệ hơn: A=100.00, B=100.0999, deposit=100.05 → cả hai đều match, pick 1.

**Tác động**: order không match được do "phạm vi overlap", gây sai lệchir hoặc treo pending mãi.

**Fix đề xuất**:
- **Option A — match chính xác**: `AllowedMargin = 0` (hoặc 0.001 để allowance gas). Match amount bằng tuyệt đối.
- **Option B — tight margin**: `AllowedMargin = 0.005`, đảm bảo nhỏ hơn fingerprint minimum (0.0001) × nhiều lần.
- **Option C — match theo fingerprint signature**: thay vì amount fuzzy, encode order_id vào amount chính xác (ví dụ cents cuối = order seq). Match exact.

Khuyến nghị: **Option A** + bỏ logic fingerprint, vì hiện fingerprint không tạo ra uniqueness thực sự khi margin quá rộng.

---

## 🟠 HIGH

### H1. Không có order expiry

**Vị trí**: không có cron expiry. Hiện service có 1 cron (`syncOKXCurrenciesCron` trong `payment.go`).

**Mô tả**: Order `pending` tồn tại mãi mãi nếu user không trả tiền. Hậu quả:
- Bào mòn pool candidate của matcher → tăng false positive
- DB phình to (sau 1 năm có hàng triệu row pending)
- Window 2h trong matcher không có ý nghĩa vì order cũ vẫn status=pending

**Fix đề xuất**:

1. Thêm method trên Repository interface:
   ```go
   // domain/port.go
   type Repository interface {
       // ... existing methods ...
       ExpireStalePending(ctx context.Context, olderThan time.Time) (int64, error)
   }
   ```

2. Implement trong `postgres/repository.go`:
   ```go
   func (r *Repository) ExpireStalePending(ctx context.Context, olderThan time.Time) (int64, error) {
       tag, err := r.db.Exec(ctx, `
           UPDATE transactions
           SET status = 'expired', updated_at = NOW()
           WHERE status = 'pending' AND created_at < $1
       `, olderThan)
       if err != nil { return 0, err }
       return tag.RowsAffected(), nil
   }
   ```

3. Thêm cron + service method:
   ```go
   // payment.go
   var _ = cron.NewJob("expire-stale-orders", cron.JobConfig{
       Title:    "Expire stale pending orders",
       Endpoint: expireStaleOrdersCron,
       Every:    1 * cron.Hour,
   })

   //encore:api private
   func expireStaleOrdersCron(ctx context.Context) error {
       return svc.ExpireStaleOrders(ctx)
   }

   func (s *Service) ExpireStaleOrders(ctx context.Context) error {
       cutoff := time.Now().Add(-24 * time.Hour)
       n, err := s.repo.ExpireStalePending(ctx, cutoff)
       if err != nil { return err }
       rlog.Info("expired stale orders", "count", n)
       return nil
   }
   ```

---

### H2. Không có refund flow

**Vị trí**: không có. Liên quan C2-đã-fix (unmatched deposits giờ được log nhưng chưa persist).

**Mô tả**: Khi user trả sai amount (xa ngoài margin), deposit vào OKX nhưng không có order nào match. Tiền nằm chờ trong OKX, không ai biết, không có workflow hoàn tiền.

OKX có API `POST /api/v5/asset/withdrawal` để withdrawal — có thể build endpoint admin trigger refund.

**Tác động**: phiền to khi có khách trả sai. Về lâu dài, tích lũy tiền "orphan" trong OKX là rủi ro tài chính + tuân thủ (regulatory).

**Fix đề xuất** (3 bước):
1. Thêm bảng `unmatched_deposits` ghi log deposit chưa match được (xem M3)
2. Endpoint admin `GET /payment/admin/unmatched` list ra
3. Endpoint admin `POST /payment/admin/refund` gọi OKX withdrawal API (thêm `Withdraw` vào `domain.Provider`)
4. Alert (Slack/email) khi có unmatched deposit > N giờ

---

### H3. Checkout API không idempotent

**Vị trí**: `payment/payment.go` — method `(*Service).Checkout`

**Mô tả**: Không nhận idempotency key. User double-click submit, hoặc network retry, → 2 order tạo ra cùng `(user_id, amount, currency)`.

**Tác động**: 
- Duplicate orders, user bối rối
- Tăng pool candidate của matcher, tăng risk conflict
- Nếu user trả 1 deposit, có thể match order nào cũng sai (order kia vẫn pending)

**Fix đề xuất**: nhận header `Idempotency-Key` (chuẩn phổ biến), hash + check Redis cache:
```go
type CheckoutRequest struct {
    UserID         string  `json:"user_id" header:"X-User-Id"`
    Amount         float64 `json:"amount"`
    Currency       string  `json:"currency"`
    Chain          string  `json:"chain"`
    IdempotencyKey string  `json:"-" header:"Idempotency-Key"`
}
```
Hoặc simpler: check DB có order nào `(user_id, amount, currency, status=pending)` created trong 5 phút cuối không → return order cũ. Thêm method `FindRecentPending` vào `domain.Repository`.

---

### H4. Price volatility chưa xử lý — accept non-stablecoin sẽ sai

**Vị trí**: không có. `CheckoutRequest` (trong `payment.go`) chỉ có `Amount` (float), không có `fiat_amount` hay `price_locked_at`.

**Mô tả**: Code giả định ngầm rằng tất cả payment là stablecoin (USDT/USDC/DAI). Nếu user request `ccy=BTC`:
- Order 0.001 BTC (=$100 lúc tạo)
- User trả 5 phút sau, BTC giảm 5% → chỉ còn $95
- Merchant ghi nhận đúng 0.001 BTC nhưng fiat value đã sai

**Tác động**: nếu business plan accept non-stablecoin, nghiệp vụ sẽ sai về mặt tài chính.

**Fix đề xuất** (nếu cần):
1. Thêm field `FiatAmount` + `FiatCurrency` (USD/EUR/VND) vào CheckoutRequest
2. Lấy giá realtime từ OKX `GET /api/v5/market/ticker?instId=BTC-USDT` (thêm method vào `domain.Provider`)
3. Lock giá trong N phút (`price_expires_at`), expired thì yêu cầu tạo lại order
4. Lưu cả `crypto_amount` + `fiat_amount` + `rate` vào DB (cần migration)

Nếu business chỉ accept stablecoin → thêm whitelist ở validation:
```go
var allowedStablecoins = map[string]bool{"USDT": true, "USDC": true, "DAI": true}
if !allowedStablecoins[req.Currency] {
    return nil, fmt.Errorf("only stablecoins supported")
}
```

---

## 🟡 MEDIUM

### M2. Graceful shutdown chưa gửi WebSocket close frame

**Vị trí**: `payment/payment.go` — `(*Service).Shutdown` + `payment/okx/provider.go` — `(*Provider).Close`

**Mô tả**: `Shutdown` cancel ctx rồi gọi `provider.Close()`. `okx.Provider.Close()` hiện là no-op. WS connection được close qua `defer c.Close()` trong `wsSubscriber.Run` khi ctx bị cancel — đóng TCP, nhưng không gửi WebSocket CloseMessage handshake frame.

**Tác động**: 
- OKX coi session bị ngắt đột ngột (không "clean closure")
- Trên Encore deploy, restart app chậm hơn (đợi timeout TCP thay vì close ngay)

**Fix đề xuất**:
1. Track connection trong `wsSubscriber`:
   ```go
   type wsSubscriber struct {
       cfg              Config
       reconnectBackoff time.Duration
       mu               sync.Mutex
       conn             *websocket.Conn
   }
   ```

2. Set+clear connection trong `Run`:
   ```go
   s.mu.Lock()
   s.conn = c
   s.mu.Unlock()
   defer func() {
       s.mu.Lock()
       s.conn = nil
       s.mu.Unlock()
   }()
   ```

3. Implement proper Close trên provider:
   ```go
   func (p *Provider) Close() error {
       p.ws.mu.Lock()
       defer p.ws.mu.Unlock()
       if p.ws.conn != nil {
           _ = p.ws.conn.WriteMessage(websocket.CloseMessage,
               websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
           return p.ws.conn.Close()
       }
       return nil
   }
   ```

---

### M3. Unmatched deposits chưa persist vào DB

**Vị trí**: `payment/payment.go` — `(*Service).handleDeposit`

```go
best := s.matcher.FindBestMatch(deposit, candidates)
if best == nil {
    rlog.Warn("no matching order for deposit",
        "amount", deposit.Amt, "ccy", deposit.Ccy, "tx_id", deposit.TxID)
    return nil  // ← chỉ log, không lưu đâu cả
}
```

**Mô tả**: Khi deposit không match order nào, chỉ có 1 dòng `rlog.Warn` (đã có structured fields sau refactor). Logs bị xoay vòng sau N ngày → mất dấu. Không có bảng DB, không có alert.

**Tác động**: khó debug khi khách khiếu nại "tôi đã trả mà không thấy confirm". Phải mò log.

**Fix đề xuất**: thêm bảng + Repository method:
```sql
-- migration 3
CREATE TABLE unmatched_deposits (
    id BIGSERIAL PRIMARY KEY,
    tx_id TEXT UNIQUE NOT NULL,
    ccy TEXT NOT NULL,
    amount NUMERIC(24,8) NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    handled BOOLEAN NOT NULL DEFAULT false,
    notes TEXT
);
```

```go
// domain/port.go
type Repository interface {
    // ... existing methods ...
    RecordUnmatchedDeposit(ctx context.Context, d Deposit) error
}

// postgres/repository.go
func (r *Repository) RecordUnmatchedDeposit(ctx context.Context, d domain.Deposit) error {
    _, err := r.db.Exec(ctx, `
        INSERT INTO unmatched_deposits (tx_id, ccy, amount)
        VALUES ($1, $2, $3)
        ON CONFLICT (tx_id) DO NOTHING
    `, d.TxID, d.Ccy, d.Amt)
    return err
}
```

Service gọi `RecordUnmatchedDeposit` khi `best == nil`. Admin dashboard check bảng này (xem H2).

---

### M5. WS reconnect không có exponential backoff

**Vị trí**: `payment/okx/provider.go` — `(*Provider).SubscribeDeposits`

```go
func (p *Provider) SubscribeDeposits(ctx context.Context, handler domain.DepositHandler) error {
    for {
        if ctx.Err() != nil { return nil }
        err := p.ws.Run(ctx, handler)
        if ctx.Err() != nil { return nil }
        if err != nil {
            time.Sleep(p.ws.reconnectBackoff)  // ← fixed 5s
        }
    }
}
```

**Mô tả**: Backoff cố định 5s. Nếu OKX down kéo dài, app spam reconnect mỗi 5s.

**Tác động**: log noise, potentially rate-limited bởi OKX.

**Fix đề xuất**: exponential backoff với cap:
```go
func (p *Provider) SubscribeDeposits(ctx context.Context, handler domain.DepositHandler) error {
    backoff := 5 * time.Second
    const maxBackoff = 5 * time.Minute
    for {
        if ctx.Err() != nil { return nil }
        err := p.ws.Run(ctx, handler)
        if ctx.Err() != nil { return nil }
        if err != nil {
            select {
            case <-time.After(backoff):
                backoff *= 2
                if backoff > maxBackoff { backoff = maxBackoff }
            case <-ctx.Done():
                return nil
            }
        } else {
            backoff = 5 * time.Second  // reset on clean exit
        }
    }
}
```

---

### M6. Matcher tie-break logic có thể chọn sai order

**Vị trí**: `payment/matcher/matcher.go` — `(*Matcher).FindBestMatch`

```go
if diff < bestScore || (diff == bestScore && gap < bestTimeGap) {
    // pick this one
}
```

**Mô tả**: Khi 2 candidate có diff bằng nhau (hi hữu nhưng có thể xảy ra nếu amount round), tie-break by smallest time gap (gần deposit.Time nhất). Logic này giả định "user trả ngay sau khi tạo order" — không phải lúc nào đúng.

**Kịch bản**: user A tạo order lúc T, trả lúc T+10s. User B tạo order lúc T+5s (sau A), trả cùng lúc. Tie-break sẽ chọn B (gần depositTime hơn), nhưng thực ra A là người trả trước.

**Tác động**: edge case, hiếm nhưng sai về business logic.

**Fix đề xuất**: nếu tie, ưu tiên order **tạo sớm hơn** (FIFO):
```go
if diff < bestScore || (diff == bestScore && tx.CreatedAt.Before(best.CreatedAt)) {
```
Hoặc bỏ fingerprint logic nếu migration sang unique address per order.

---

## 🟢 LOW

### L1. Hardcoded retry/backoff constants

**Vị trí**:
- `payment/payment.go` — `dedupAmount` có `const maxRetries = 10`
- `payment/matcher/matcher.go` — `Default()` có `AllowedMargin = 0.1`, `TimeWindow = 2h`
- `payment/okx/provider.go` — `reconnectBackoff` fixed 5s
- `payment/okx/ws.go` — `pingTicker = 20 * time.Second`
- `payment/resources.go` — `DefaultExpiry: cache.ExpireIn(15 * time.Minute)`

**Mô tả**: Các magic number rải rác. Nếu muốn tune production, phải sửa code + deploy.

**Fix đề xuất**: dùng Encore config:
```go
import "encore.dev/config"

type gatewayConfig struct {
    CheckoutMaxRetries config.Int
    MatchAllowedMargin config.Float64
    MatchTimeWindow    config.Duration  // if available, else string parse
    WSReconnectBackoff config.Duration
    WSPingInterval     config.Duration
    CacheTTL           config.Duration
}

var cfg = config.Load[*gatewayConfig]()
```
Allow tuning per-env mà không deploy lại code.

---

### L2. Không dùng errs.Error structured errors

**Vị trí**: toàn bộ `payment.go`, `okx/*.go`, `postgres/*.go`.

**Mô tả**: Code return `fmt.Errorf(...)` cho mọi error. Encore framework khuyến khích `errs.Error` với code HTTP rõ ràng:

```go
// Hiện tại (payment.go Checkout)
return nil, fmt.Errorf("system busy, please try again later")

// Nên
return nil, errs.B().
    Code(errs.ResourceExhausted).
    Msg("system busy, please try again later").
    Err()
```

**Tác động**: client không phân biệt được 400 vs 500, tất cả là generic error. Trace không có metadata.

**Fix đề xuất**: áp dụng `errs.B().Code(...).Msg(...).Meta(...).Err()` ở error paths chính. Ưu tiên:
- `Checkout`: NotFound (no addr), ResourceExhausted (busy, retry), Internal (DB fail)
- `GetCurrencies`: Unavailable (OKX down)
- `handleDeposit` (private): chỉ cần wrap, không cần HTTP code

---

### L4. Migration naming không theo convention chuẩn

**Vị trí**: `payment/migrations/`

```
1_create_tables.up.sql
2_add_ws_fields.up.sql
```

**Mô tả**: Theo Encore docs (AGENTS.md), convention là `number_description.up.sql`. Hiện tại chỉ 1 digit, không có down migrations.

**Tác động**: không critical, nhưng khi đến migration 10+ sẽ sort lexical sai (`10_xxx` đứng trước `2_xxx`).

**Fix đề xuất**: rename (nếu chưa production):
```
0001_create_tables.up.sql
0002_add_ws_fields.up.sql
```
Hoặc ít nhất dùng 2 digit: `01_`, `02_`. Thêm `.down.sql` nếu cần rollback.

---

### L5. Chưa có test thực tế

**Vị trí**: không có file `*_test.go` nào.

**Mô tả**: Refactor kiến trúc đã làm code **testable** (matcher là pure function, Repository/Provider/Cache đều có interface), nhưng chưa có test thực tế.

Các logic critical cần test:
- `okx.sign` — signature sai = API fail toàn bộ
- `matcher.FindBestMatch` — logic match, margin, tie-break
- `payment.generateOrderID` — uniqueness
- `payment.(*Service).dedupAmount` — fingerprint logic (cần mock Repository)
- `payment.(*Service).handleDeposit` — idempotency, mark success race (cần mock Repository)

**Tác động**: refactor sợ, regression không phát hiện sớm.

**Fix đề xuất**: ưu tiên unit test cho:
1. `okx/auth_test.go` — verify signature format đúng spec OKX (test vector từ OKX docs)
2. `matcher/matcher_test.go` — table-driven test với các deposit/order combinations:
   ```go
   func TestFindBestMatch(t *testing.T) {
       cases := []struct{
           name     string
           deposit  domain.Deposit
           candidates []*domain.Transaction
           wantID   string
       }{
           {"exact match", ...},
           {"within margin", ...},
           {"no match", ...},
           {"tie-break by time", ...},
       }
       for _, c := range cases {
           t.Run(c.name, func(t *testing.T) {
               m := matcher.New(matcher.Default())
               got := m.FindBestMatch(c.deposit, c.candidates)
               // assert
           })
       }
   }
   ```
3. `payment/payment_test.go` — test `dedupAmount` + `handleDeposit` với mock Repository (implement domain.Repository trong test)

Mock Repository trong test: viết struct nhỏ implement `domain.Repository` (in-memory), KHÔNG dùng mock framework.

---

## Ưu tiên đề xuất xử lý

Sau refactor, thứ tự ưu tiên còn lại:

| Ưu tiên | Issue | Lý do |
|---|---|---|
| **P0** | C1 (race condition) | Mất tiền khách ngay lập tức khi concurrent |
| **P0** | C3 (reconciliation) | Mất tiền khách khi app restart |
| **P1** | C4 (margin) | Sai logic match |
| **P1** | H1 (expiry) | Bào mòn pool match |
| **P1** | H3 (idempotency) | UX + duplicate orders |
| **P2** | H2 (refund), H4 (volatility) | Tùy business plan |
| **P2** | M2, M3, M5, M6 | Robustness, ops |
| **P3** | L1, L2, L4, L5 | Code quality, maintainability |

---

## Ghi chú

- Số dòng / file ref cập nhật theo cấu trúc post-refactor (2026-06-19): `payment/{payment.go, resources.go, domain/, okx/, postgres/, cacheadapter/, matcher/}`.
- Một số đánh giá (đặc biệt OKX behavior) là [INFERENCE] dựa trên docs OKX công khai, chưa verify thực tế qua test.
- Fix code cụ thể trong các section là **草案**, cần adapt theo context khi implement.
- Các interface method mới đề xuất thêm (`ListDeposits`, `ExpireStalePending`, `RecordUnmatchedDeposit`, `Withdraw`) cần cập nhật cả `domain/port.go` và mọi implementation — hiện chỉ có 1 implementation mỗi interface nên cost thấp.

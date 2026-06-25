package matcher

import (
	"testing"
	"time"

	"encore.app/payment/domain"
	"github.com/shopspring/decimal"
)

func mustDec(t *testing.T, s string) decimal.Decimal {
	t.Helper()
	d, err := decimal.NewFromString(s)
	if err != nil {
		t.Fatalf("bad decimal %q: %v", s, err)
	}
	return d
}

func tx(id string, amount string, created time.Time) *domain.Transaction {
	d, _ := decimal.NewFromString(amount)
	return &domain.Transaction{ID: id, Amount: d, CreatedAt: created}
}

func deposit(amt string, at time.Time) domain.Deposit {
	d, _ := decimal.NewFromString(amt)
	return domain.Deposit{Amt: d, Time: at, Ccy: "USDT"}
}

// Exact match is the safe default for single-address, no-memo matching.
func TestFindBestMatch_ExactOnly(t *testing.T) {
	t.Parallel()
	m := New(Default())
	now := time.Now()
	cands := []*domain.Transaction{
		tx("a", "100.0000", now.Add(-1*time.Hour)),
		tx("b", "100.0500", now.Add(-30*time.Minute)), // distinct amount, within old 0.1 margin
	}

	got := m.FindBestMatch(deposit("100.0000", now), cands)
	if got == nil || got.ID != "a" {
		t.Fatalf("expected exact match 'a', got %v", got)
	}
}

// The old ±0.1 margin would have wrongly matched a nearby-but-distinct amount.
// Exact matching (margin=0) must reject it.
func TestFindBestMatch_RejectsNearMiss(t *testing.T) {
	t.Parallel()
	m := New(Default())
	now := time.Now()
	cands := []*domain.Transaction{tx("b", "100.0500", now.Add(-30 * time.Minute))}

	if got := m.FindBestMatch(deposit("100.0000", now), cands); got != nil {
		t.Fatalf("expected nil for non-exact amount, got %v", got.ID)
	}
}

func TestFindBestMatch_NoCandidates(t *testing.T) {
	t.Parallel()
	m := New(Default())
	if got := m.FindBestMatch(deposit("50.00", time.Now()), nil); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

// Tie on exact amount is broken by smallest time gap (multiple exact-amount
// orders can only exist if the unique index invariant is violated, but the
// tie-break is still exercised here for safety).
func TestFindBestMatch_ExactTieBreakByTime(t *testing.T) {
	t.Parallel()
	m := New(Default())
	now := time.Now()
	cands := []*domain.Transaction{
		tx("far", "10.00", now.Add(-2*time.Hour)),
		tx("near", "10.00", now.Add(-5*time.Minute)),
	}
	got := m.FindBestMatch(deposit("10.00", now), cands)
	if got == nil || got.ID != "near" {
		t.Fatalf("expected nearest 'near', got %v", got)
	}
}

func TestSince(t *testing.T) {
	t.Parallel()
	m := New(Config{AllowedMargin: decimal.Zero, TimeWindow: 24 * time.Hour})
	at := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	want := at.Add(-24 * time.Hour)
	if got := m.Since(at); !got.Equal(want) {
		t.Fatalf("Since = %v, want %v", got, want)
	}
}

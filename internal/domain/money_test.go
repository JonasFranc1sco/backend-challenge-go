package domain_test

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/domain"
)

func TestMoney_Parsing(t *testing.T) {
	tests := []struct {
		name        string
		amountStr   string
		currency    string
		expectUnits int64
		expectErr   bool
	}{
		{"Valid 25.00 BRL", "25.00", "BRL", 2500, false},
		{"Valid 0.00 BRL", "0.00", "BRL", 0, false},
		{"Valid 1000.50 USD", "1000.50", "USD", 100050, false},
		{"Valid internal negative -15.20 EUR", "-15.20", "EUR", -1520, false},
		{"Large valid amount", "92233720368547758.07", "BRL", math.MaxInt64, false},
		{"Invalid no decimals", "25", "BRL", 0, true},
		{"Invalid one decimal", "25.5", "BRL", 0, true},
		{"Invalid three decimals", "25.005", "BRL", 0, true},
		{"Invalid empty amount", "", "BRL", 0, true},
		{"Invalid NaN", "NaN", "BRL", 0, true},
		{"Invalid Infinity", "Infinity", "BRL", 0, true},
		{"Invalid scientific notation", "1e5", "BRL", 0, true},
		{"Invalid scientific decimal", "1.00e2", "BRL", 0, true},
		{"Invalid negative zero", "-0.00", "BRL", 0, true},
		{"Invalid lowercase currency", "25.00", "brl", 0, true},
		{"Invalid currency length", "25.00", "USDT", 0, true},
		{"Invalid numeric currency", "25.00", "123", 0, true},
		{"Overflow beyond int64", "922337203685477580.00", "BRL", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := domain.NewMoneyFromDecimal(tt.amountStr, tt.currency)
			if tt.expectErr {
				if err == nil {
					t.Fatalf("expected error for input '%s' '%s', got nil", tt.amountStr, tt.currency)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if m.Units() != tt.expectUnits {
					t.Errorf("expected units %d, got %d", tt.expectUnits, m.Units())
				}
				if m.Currency() != tt.currency {
					t.Errorf("expected currency %s, got %s", tt.currency, m.Currency())
				}
			}
		})
	}
}

func TestMoney_PositiveAndNonNegativeParsing(t *testing.T) {
	// NewPositiveMoneyFromDecimal
	_, err := domain.NewPositiveMoneyFromDecimal("25.00", "BRL")
	if err != nil {
		t.Fatalf("unexpected error for positive amount: %v", err)
	}

	_, err = domain.NewPositiveMoneyFromDecimal("0.00", "BRL")
	if !errors.Is(err, domain.ErrZeroAmountNotAllowed) {
		t.Errorf("expected ErrZeroAmountNotAllowed for 0.00, got: %v", err)
	}

	_, err = domain.NewPositiveMoneyFromDecimal("-10.00", "BRL")
	if !errors.Is(err, domain.ErrNegativeAmount) {
		t.Errorf("expected ErrNegativeAmount for -10.00, got: %v", err)
	}

	// NewNonNegativeMoneyFromDecimal
	mZero, err := domain.NewNonNegativeMoneyFromDecimal("0.00", "BRL")
	if err != nil || !mZero.IsZero() {
		t.Fatalf("expected zero Money without error, got %v, err=%v", mZero, err)
	}

	_, err = domain.NewNonNegativeMoneyFromDecimal("-5.00", "BRL")
	if !errors.Is(err, domain.ErrNegativeAmount) {
		t.Errorf("expected ErrNegativeAmount for negative input, got: %v", err)
	}
}

func TestMoney_Arithmetic(t *testing.T) {
	m10, _ := domain.NewMoneyFromDecimal("10.00", "BRL")
	m25, _ := domain.NewMoneyFromDecimal("25.00", "BRL")
	usd, _ := domain.NewMoneyFromDecimal("10.00", "USD")

	// Add
	sum, err := m10.Add(m25)
	if err != nil {
		t.Fatalf("unexpected error on Add: %v", err)
	}
	if sum.String() != "35.00" {
		t.Errorf("expected 35.00, got %s", sum.String())
	}

	// Sub
	diff, err := m25.Sub(m10)
	if err != nil {
		t.Fatalf("unexpected error on Sub: %v", err)
	}
	if diff.String() != "15.00" {
		t.Errorf("expected 15.00, got %s", diff.String())
	}

	// Currency Mismatch on Add / Sub
	_, err = m10.Add(usd)
	if !errors.Is(err, domain.ErrCurrencyMismatch) {
		t.Errorf("expected ErrCurrencyMismatch, got %v", err)
	}

	_, err = m10.Sub(usd)
	if !errors.Is(err, domain.ErrCurrencyMismatch) {
		t.Errorf("expected ErrCurrencyMismatch, got %v", err)
	}

	// Negation
	neg, err := m10.Neg()
	if err != nil {
		t.Fatalf("unexpected error on Neg: %v", err)
	}
	if neg.String() != "-10.00" || !neg.IsNegative() {
		t.Errorf("expected -10.00, got %s", neg.String())
	}

	// Overflow checks
	mMax, _ := domain.NewMoney(math.MaxInt64, "BRL")
	mOne, _ := domain.NewMoney(1, "BRL")
	_, err = mMax.Add(mOne)
	if !errors.Is(err, domain.ErrArithmeticOverflow) {
		t.Errorf("expected ErrArithmeticOverflow on Add, got %v", err)
	}

	mMin, _ := domain.NewMoney(math.MinInt64, "BRL")
	_, err = mMin.Sub(mOne)
	if !errors.Is(err, domain.ErrArithmeticOverflow) {
		t.Errorf("expected ErrArithmeticOverflow on Sub, got %v", err)
	}

	_, err = mMin.Neg()
	if !errors.Is(err, domain.ErrArithmeticOverflow) {
		t.Errorf("expected ErrArithmeticOverflow on Negating MinInt64, got %v", err)
	}
}

func TestMoney_Comparisons(t *testing.T) {
	m10, _ := domain.NewMoneyFromDecimal("10.00", "BRL")
	m20, _ := domain.NewMoneyFromDecimal("20.00", "BRL")
	m10Dup, _ := domain.NewMoneyFromDecimal("10.00", "BRL")
	usd10, _ := domain.NewMoneyFromDecimal("10.00", "USD")

	if !m10.Equals(m10Dup) {
		t.Error("expected m10.Equals(m10Dup) to be true")
	}
	if m10.Equals(m20) {
		t.Error("expected m10.Equals(m20) to be false")
	}
	if m10.Equals(usd10) {
		t.Error("expected different currencies to not be equal")
	}

	gt, err := m20.GreaterThan(m10)
	if err != nil || !gt {
		t.Errorf("expected m20 > m10, got %v, err=%v", gt, err)
	}

	lt, err := m10.LessThan(m20)
	if err != nil || !lt {
		t.Errorf("expected m10 < m20, got %v, err=%v", lt, err)
	}

	gte, err := m10.GreaterThanOrEqual(m10Dup)
	if err != nil || !gte {
		t.Errorf("expected m10 >= m10Dup, got %v, err=%v", gte, err)
	}

	// Comparison currency mismatch
	_, err = m10.GreaterThan(usd10)
	if !errors.Is(err, domain.ErrCurrencyMismatch) {
		t.Errorf("expected ErrCurrencyMismatch on GreaterThan, got %v", err)
	}
}

func TestMoney_JSON(t *testing.T) {
	m, _ := domain.NewMoneyFromDecimal("975.00", "BRL")
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("unexpected error marshalling JSON: %v", err)
	}

	expectedJSON := `{"amount":"975.00","currency":"BRL"}`
	if string(data) != expectedJSON {
		t.Errorf("expected JSON %s, got %s", expectedJSON, string(data))
	}

	var unmarshalled domain.Money
	if err := json.Unmarshal(data, &unmarshalled); err != nil {
		t.Fatalf("unexpected error unmarshalling JSON: %v", err)
	}

	if !unmarshalled.Equals(m) {
		t.Errorf("expected %v, got %v", m, unmarshalled)
	}

	// Invalid JSON payload
	invalidJSON := `{"amount":"invalid","currency":"BRL"}`
	if err := json.Unmarshal([]byte(invalidJSON), &unmarshalled); err == nil {
		t.Error("expected error unmarshalling invalid JSON amount, got nil")
	}
}

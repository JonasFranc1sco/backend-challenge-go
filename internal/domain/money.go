package domain

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

var (
	currencyRegex   = regexp.MustCompile(`^[A-Z]{3}$`)
	decimalRegex    = regexp.MustCompile(`^(-?)([0-9]+)\.([0-9]{2})$`)
	nonNegDecRegex  = regexp.MustCompile(`^([0-9]+)\.([0-9]{2})$`)
)

// Money is an immutable value object representing a monetary amount in minimal units (cents)
// with a fixed scale of 2 decimal places and an ISO 4217 currency code.
//
// ZERO-FLOAT GUARANTEE: Money never passes through float32 or float64.
// All values are stored as int64 units. Overflow is strictly checked.
type Money struct {
	units    int64
	currency string
}

// NewMoney creates a Money value object from minimal units (cents) and currency.
func NewMoney(units int64, currency string) (Money, error) {
	currency = strings.TrimSpace(currency)
	if !currencyRegex.MatchString(currency) {
		return Money{}, fmt.Errorf("%w: '%s'", ErrInvalidCurrency, currency)
	}
	return Money{
		units:    units,
		currency: currency,
	}, nil
}

// MoneyZero returns a zero Money instance for the specified currency.
func MoneyZero(currency string) (Money, error) {
	return NewMoney(0, currency)
}

// NewMoneyFromDecimal parses an exact decimal string (e.g. "25.00", "-10.50") into a Money value object.
// It strictly enforces a scale of 2 decimal places, rejecting scientific notation, NaN, Infinity, etc.
func NewMoneyFromDecimal(amountStr, currency string) (Money, error) {
	amountStr = strings.TrimSpace(amountStr)
	currency = strings.TrimSpace(currency)

	if !currencyRegex.MatchString(currency) {
		return Money{}, fmt.Errorf("%w: '%s'", ErrInvalidCurrency, currency)
	}

	matches := decimalRegex.FindStringSubmatch(amountStr)
	if matches == nil {
		return Money{}, fmt.Errorf("%w: '%s' (must be exact decimal with 2 decimal places, e.g. '25.00')", ErrInvalidAmount, amountStr)
	}

	isNegative := matches[1] == "-"
	integerPartStr := matches[2]
	fractionalPartStr := matches[3]

	// Reject "-0.00"
	if isNegative && integerPartStr == "0" && fractionalPartStr == "00" {
		return Money{}, fmt.Errorf("%w: negative zero '-0.00' is not allowed", ErrInvalidAmount)
	}

	// Parse integer part
	intPart, err := strconv.ParseInt(integerPartStr, 10, 64)
	if err != nil {
		return Money{}, fmt.Errorf("%w: overflow parsing integer part '%s': %v", ErrArithmeticOverflow, integerPartStr, err)
	}

	// Check multiplication overflow before multiplying by 100
	if intPart > math.MaxInt64/100 {
		return Money{}, fmt.Errorf("%w: amount '%s' overflows int64 cents", ErrArithmeticOverflow, amountStr)
	}
	units := intPart * 100

	// Parse fractional part (always 2 digits)
	fracPart, err := strconv.ParseInt(fractionalPartStr, 10, 64)
	if err != nil {
		return Money{}, fmt.Errorf("%w: invalid fractional part '%s'", ErrInvalidAmount, fractionalPartStr)
	}

	if units > math.MaxInt64-fracPart {
		return Money{}, fmt.Errorf("%w: amount '%s' overflows int64 cents", ErrArithmeticOverflow, amountStr)
	}
	units += fracPart

	if isNegative {
		units = -units
	}

	return Money{
		units:    units,
		currency: currency,
	}, nil
}

// NewPositiveMoneyFromDecimal parses an external financial input.
// It rejects negative values and zero values (must be strictly > 0.00).
func NewPositiveMoneyFromDecimal(amountStr, currency string) (Money, error) {
	m, err := NewMoneyFromDecimal(amountStr, currency)
	if err != nil {
		return Money{}, err
	}
	if m.units <= 0 {
		if m.units < 0 {
			return Money{}, fmt.Errorf("%w: value cannot be negative: '%s'", ErrNegativeAmount, amountStr)
		}
		return Money{}, fmt.Errorf("%w: value must be strictly positive", ErrZeroAmountNotAllowed)
	}
	return m, nil
}

// NewNonNegativeMoneyFromDecimal parses an input that can be zero or positive (e.g. initial balance or LOSS).
func NewNonNegativeMoneyFromDecimal(amountStr, currency string) (Money, error) {
	m, err := NewMoneyFromDecimal(amountStr, currency)
	if err != nil {
		return Money{}, err
	}
	if m.units < 0 {
		return Money{}, fmt.Errorf("%w: value cannot be negative: '%s'", ErrNegativeAmount, amountStr)
	}
	return m, nil
}

// Units returns the internal int64 minimal monetary units (cents).
func (m Money) Units() int64 {
	return m.units
}

// Currency returns the ISO 4217 currency code.
func (m Money) Currency() string {
	return m.currency
}

// IsZero returns true if the amount is exactly zero.
func (m Money) IsZero() bool {
	return m.units == 0
}

// IsPositive returns true if the amount is strictly positive.
func (m Money) IsPositive() bool {
	return m.units > 0
}

// IsNegative returns true if the amount is strictly negative.
func (m Money) IsNegative() bool {
	return m.units < 0
}

// String returns the amount formatted as a decimal string with two decimal places (e.g. "25.00").
func (m Money) String() string {
	absUnits := m.units
	sign := ""
	if absUnits < 0 {
		sign = "-"
		// Handle math.MinInt64
		if absUnits == math.MinInt64 {
			return "-92233720368547758.08"
		}
		absUnits = -absUnits
	}
	intPart := absUnits / 100
	fracPart := absUnits % 100
	return fmt.Sprintf("%s%d.%02d", sign, intPart, fracPart)
}

// Formatted returns the amount string followed by the currency (e.g. "25.00 BRL").
func (m Money) Formatted() string {
	return fmt.Sprintf("%s %s", m.String(), m.currency)
}

// Equals checks if two Money objects have identical units and currency.
func (m Money) Equals(other Money) bool {
	return m.units == other.units && m.currency == other.currency
}

func (m Money) ensureSameCurrency(other Money) error {
	if m.currency != other.currency {
		return fmt.Errorf("%w: cannot operate on '%s' and '%s'", ErrCurrencyMismatch, m.currency, other.currency)
	}
	return nil
}

// Add adds two Money objects with overflow detection.
func (m Money) Add(other Money) (Money, error) {
	if err := m.ensureSameCurrency(other); err != nil {
		return Money{}, err
	}

	// Overflow checks for a + b
	if (other.units > 0 && m.units > math.MaxInt64-other.units) ||
		(other.units < 0 && m.units < math.MinInt64-other.units) {
		return Money{}, fmt.Errorf("%w: adding %d and %d", ErrArithmeticOverflow, m.units, other.units)
	}

	return Money{
		units:    m.units + other.units,
		currency: m.currency,
	}, nil
}

// Sub subtracts other from m with overflow detection.
func (m Money) Sub(other Money) (Money, error) {
	if err := m.ensureSameCurrency(other); err != nil {
		return Money{}, err
	}

	// Overflow checks for a - b
	if (other.units < 0 && m.units > math.MaxInt64+other.units) ||
		(other.units > 0 && m.units < math.MinInt64+other.units) {
		return Money{}, fmt.Errorf("%w: subtracting %d from %d", ErrArithmeticOverflow, other.units, m.units)
	}

	return Money{
		units:    m.units - other.units,
		currency: m.currency,
	}, nil
}

// Neg negates the Money amount with overflow detection (math.MinInt64 negation overflows).
func (m Money) Neg() (Money, error) {
	if m.units == math.MinInt64 {
		return Money{}, fmt.Errorf("%w: cannot negate math.MinInt64", ErrArithmeticOverflow)
	}
	return Money{
		units:    -m.units,
		currency: m.currency,
	}, nil
}

// GreaterThan returns true if m > other.
func (m Money) GreaterThan(other Money) (bool, error) {
	if err := m.ensureSameCurrency(other); err != nil {
		return false, err
	}
	return m.units > other.units, nil
}

// GreaterThanOrEqual returns true if m >= other.
func (m Money) GreaterThanOrEqual(other Money) (bool, error) {
	if err := m.ensureSameCurrency(other); err != nil {
		return false, err
	}
	return m.units >= other.units, nil
}

// LessThan returns true if m < other.
func (m Money) LessThan(other Money) (bool, error) {
	if err := m.ensureSameCurrency(other); err != nil {
		return false, err
	}
	return m.units < other.units, nil
}

// LessThanOrEqual returns true if m <= other.
func (m Money) LessThanOrEqual(other Money) (bool, error) {
	if err := m.ensureSameCurrency(other); err != nil {
		return false, err
	}
	return m.units <= other.units, nil
}

// moneyJSON matches the contract {"amount":"25.00","currency":"BRL"}
type moneyJSON struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// MarshalJSON serializes Money to JSON format {"amount":"25.00","currency":"BRL"}.
func (m Money) MarshalJSON() ([]byte, error) {
	return json.Marshal(moneyJSON{
		Amount:   m.String(),
		Currency: m.currency,
	})
}

// UnmarshalJSON deserializes Money from JSON format {"amount":"25.00","currency":"BRL"}.
func (m *Money) UnmarshalJSON(data []byte) error {
	var mj moneyJSON
	if err := json.Unmarshal(data, &mj); err != nil {
		return fmt.Errorf("%w: invalid JSON for Money: %v", ErrInvalidAmount, err)
	}

	parsed, err := NewMoneyFromDecimal(mj.Amount, mj.Currency)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}

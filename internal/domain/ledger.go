package domain

import (
	"fmt"
	"strings"
	"time"
)

// LedgerDirection indicates whether the entry was a debit or credit to the wallet balance.
type LedgerDirection string

const (
	DirectionDebit  LedgerDirection = "DEBIT"
	DirectionCredit LedgerDirection = "CREDIT"
)

func (d LedgerDirection) IsValid() bool {
	return d == DirectionDebit || d == DirectionCredit
}

// WalletLedgerEntry represents an immutable financial journal entry.
// Mathematical invariants:
// - CREDIT: balanceAfter = balanceBefore + amount
// - DEBIT:  balanceAfter = balanceBefore - amount
type WalletLedgerEntry struct {
	id            string
	walletID      string
	transactionID string
	direction     LedgerDirection
	amount        Money
	balanceBefore Money
	balanceAfter  Money
	createdAt     time.Time
}

// NewLedgerEntry constructs an immutable ledger entry enforcing strict mathematical balance.
func NewLedgerEntry(
	id string,
	walletID string,
	transactionID string,
	direction LedgerDirection,
	amount Money,
	balanceBefore Money,
	balanceAfter Money,
	now time.Time,
) (*WalletLedgerEntry, error) {
	id = strings.TrimSpace(id)
	walletID = strings.TrimSpace(walletID)
	transactionID = strings.TrimSpace(transactionID)

	if id == "" || walletID == "" || transactionID == "" {
		return nil, fmt.Errorf("%w: id, walletID, and transactionID cannot be empty", ErrInvalidLedgerMath)
	}
	if !direction.IsValid() {
		return nil, fmt.Errorf("%w: '%s'", ErrInvalidLedgerDirection, direction)
	}
	if !amount.IsPositive() {
		return nil, fmt.Errorf("%w: ledger entry amount must be strictly positive", ErrZeroAmountNotAllowed)
	}
	if balanceBefore.IsNegative() || balanceAfter.IsNegative() {
		return nil, fmt.Errorf("%w: balanceBefore and balanceAfter cannot be negative", ErrNegativeAmount)
	}

	// Check currencies match
	currency := amount.Currency()
	if balanceBefore.Currency() != currency || balanceAfter.Currency() != currency {
		return nil, fmt.Errorf("%w: currency mismatch in ledger math: amount(%s), before(%s), after(%s)",
			ErrCurrencyMismatch, currency, balanceBefore.Currency(), balanceAfter.Currency())
	}

	// Validate balance math
	switch direction {
	case DirectionCredit:
		expectedAfter, err := balanceBefore.Add(amount)
		if err != nil {
			return nil, err
		}
		if !balanceAfter.Equals(expectedAfter) {
			return nil, fmt.Errorf("%w: CREDIT expected balanceAfter %s, got %s",
				ErrInvalidLedgerMath, expectedAfter.String(), balanceAfter.String())
		}
	case DirectionDebit:
		expectedAfter, err := balanceBefore.Sub(amount)
		if err != nil {
			return nil, err
		}
		if !balanceAfter.Equals(expectedAfter) {
			return nil, fmt.Errorf("%w: DEBIT expected balanceAfter %s, got %s",
				ErrInvalidLedgerMath, expectedAfter.String(), balanceAfter.String())
		}
	}

	return &WalletLedgerEntry{
		id:            id,
		walletID:      walletID,
		transactionID: transactionID,
		direction:     direction,
		amount:        amount,
		balanceBefore: balanceBefore,
		balanceAfter:  balanceAfter,
		createdAt:     now.UTC(),
	}, nil
}

// RehydrateLedgerEntry restores a WalletLedgerEntry from database storage.
func RehydrateLedgerEntry(
	id string,
	walletID string,
	transactionID string,
	direction LedgerDirection,
	amount Money,
	balanceBefore Money,
	balanceAfter Money,
	createdAt time.Time,
) *WalletLedgerEntry {
	return &WalletLedgerEntry{
		id:            id,
		walletID:      walletID,
		transactionID: transactionID,
		direction:     direction,
		amount:        amount,
		balanceBefore: balanceBefore,
		balanceAfter:  balanceAfter,
		createdAt:     createdAt.UTC(),
	}
}

// Getters

func (e *WalletLedgerEntry) ID() string                 { return e.id }
func (e *WalletLedgerEntry) WalletID() string           { return e.walletID }
func (e *WalletLedgerEntry) TransactionID() string      { return e.transactionID }
func (e *WalletLedgerEntry) Direction() LedgerDirection { return e.direction }
func (e *WalletLedgerEntry) Amount() Money              { return e.amount }
func (e *WalletLedgerEntry) BalanceBefore() Money       { return e.balanceBefore }
func (e *WalletLedgerEntry) BalanceAfter() Money        { return e.balanceAfter }
func (e *WalletLedgerEntry) CreatedAt() time.Time       { return e.createdAt }

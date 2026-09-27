package domain

import (
	"fmt"
	"strings"
	"time"
)

// Wallet is the aggregate root for player balances.
// Its state is strictly encapsulated. Modifying balance requires explicit Debit/Credit methods
// which enforce monetary invariants and maintain the aggregate version.
type Wallet struct {
	id        string
	playerID  string
	currency  string
	balance   Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

// NewWallet constructs a new Wallet aggregate with initial state.
// Version is initialized to 1. Initial balance must be non-negative (>= 0).
func NewWallet(id, playerID, currency string, initialBalance Money, now time.Time) (*Wallet, error) {
	id = strings.TrimSpace(id)
	playerID = strings.TrimSpace(playerID)
	currency = strings.TrimSpace(currency)

	if id == "" {
		return nil, fmt.Errorf("%w: wallet id cannot be empty", ErrWalletNotInitialized)
	}
	if playerID == "" {
		return nil, fmt.Errorf("%w: player id cannot be empty", ErrWalletNotInitialized)
	}
	if !currencyRegex.MatchString(currency) {
		return nil, fmt.Errorf("%w: '%s'", ErrInvalidCurrency, currency)
	}
	if initialBalance.Currency() != currency {
		return nil, fmt.Errorf("%w: initial balance currency '%s' does not match wallet currency '%s'",
			ErrCurrencyMismatch, initialBalance.Currency(), currency)
	}
	if initialBalance.IsNegative() {
		return nil, fmt.Errorf("%w: initial balance cannot be negative", ErrNegativeAmount)
	}

	return &Wallet{
		id:        id,
		playerID:  playerID,
		currency:  currency,
		balance:   initialBalance,
		version:   1,
		createdAt: now.UTC(),
		updatedAt: now.UTC(),
	}, nil
}

// RehydrateWallet restores a Wallet aggregate from persistent storage without emitting events or reapplying business logic.
func RehydrateWallet(id, playerID, currency string, balance Money, version int64, createdAt, updatedAt time.Time) (*Wallet, error) {
	id = strings.TrimSpace(id)
	playerID = strings.TrimSpace(playerID)
	currency = strings.TrimSpace(currency)

	if id == "" || playerID == "" || currency == "" {
		return nil, fmt.Errorf("%w: id, playerID and currency cannot be empty", ErrWalletNotInitialized)
	}
	if version < 1 {
		return nil, fmt.Errorf("%w: version %d is invalid", ErrInvalidWalletVersion, version)
	}
	if balance.Currency() != currency {
		return nil, fmt.Errorf("%w: balance currency '%s' does not match wallet currency '%s'",
			ErrCurrencyMismatch, balance.Currency(), currency)
	}
	if balance.IsNegative() {
		return nil, fmt.Errorf("%w: rehydrated balance cannot be negative", ErrNegativeAmount)
	}

	return &Wallet{
		id:        id,
		playerID:  playerID,
		currency:  currency,
		balance:   balance,
		version:   version,
		createdAt: createdAt.UTC(),
		updatedAt: updatedAt.UTC(),
	}, nil
}

// ID returns the unique wallet identifier.
func (w *Wallet) ID() string {
	return w.id
}

// PlayerID returns the player identifier who owns this wallet.
func (w *Wallet) PlayerID() string {
	return w.playerID
}

// Currency returns the ISO 4217 currency code of the wallet.
func (w *Wallet) Currency() string {
	return w.currency
}

// Balance returns the current immutable Money balance.
func (w *Wallet) Balance() Money {
	return w.balance
}

// Version returns the current aggregate version.
func (w *Wallet) Version() int64 {
	return w.version
}

// CreatedAt returns the timestamp when the wallet was created.
func (w *Wallet) CreatedAt() time.Time {
	return w.createdAt
}

// UpdatedAt returns the timestamp when the wallet was last modified.
func (w *Wallet) UpdatedAt() time.Time {
	return w.updatedAt
}

// Debit deducts money from the wallet balance.
// Requires amount > 0, currency match, and balance >= amount.
// Increments version by 1 and updates updatedAt.
func (w *Wallet) Debit(amount Money, now time.Time) error {
	if amount.Currency() != w.currency {
		return fmt.Errorf("%w: cannot debit '%s' from wallet in '%s'", ErrCurrencyMismatch, amount.Currency(), w.currency)
	}
	if !amount.IsPositive() {
		return fmt.Errorf("%w: debit amount must be strictly positive", ErrZeroAmountNotAllowed)
	}

	canDebit, err := w.balance.GreaterThanOrEqual(amount)
	if err != nil {
		return err
	}
	if !canDebit {
		return fmt.Errorf("%w: balance %s is less than required %s", ErrInsufficientBalance, w.balance.String(), amount.String())
	}

	newBalance, err := w.balance.Sub(amount)
	if err != nil {
		return err
	}

	w.balance = newBalance
	w.version++
	w.updatedAt = now.UTC()
	return nil
}

// Credit adds money to the wallet balance.
// Requires amount > 0 and currency match.
// Increments version by 1 and updates updatedAt.
func (w *Wallet) Credit(amount Money, now time.Time) error {
	if amount.Currency() != w.currency {
		return fmt.Errorf("%w: cannot credit '%s' to wallet in '%s'", ErrCurrencyMismatch, amount.Currency(), w.currency)
	}
	if !amount.IsPositive() {
		return fmt.Errorf("%w: credit amount must be strictly positive", ErrZeroAmountNotAllowed)
	}

	newBalance, err := w.balance.Add(amount)
	if err != nil {
		return err
	}

	w.balance = newBalance
	w.version++
	w.updatedAt = now.UTC()
	return nil
}

package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/domain"
)

func TestWallet_Creation(t *testing.T) {
	now := time.Now().UTC()
	initBal, _ := domain.NewMoneyFromDecimal("100.00", "BRL")

	// Valid creation
	w, err := domain.NewWallet("wallet-1", "player-1", "BRL", initBal, now)
	if err != nil {
		t.Fatalf("unexpected error creating wallet: %v", err)
	}

	if w.ID() != "wallet-1" {
		t.Errorf("expected id wallet-1, got %s", w.ID())
	}
	if w.PlayerID() != "player-1" {
		t.Errorf("expected player player-1, got %s", w.PlayerID())
	}
	if w.Currency() != "BRL" {
		t.Errorf("expected currency BRL, got %s", w.Currency())
	}
	if w.Version() != 1 {
		t.Errorf("expected initial version 1, got %d", w.Version())
	}
	if !w.Balance().Equals(initBal) {
		t.Errorf("expected balance %v, got %v", initBal, w.Balance())
	}

	// Currency mismatch between wallet and initial balance
	usdBal, _ := domain.NewMoneyFromDecimal("100.00", "USD")
	_, err = domain.NewWallet("wallet-1", "player-1", "BRL", usdBal, now)
	if !errors.Is(err, domain.ErrCurrencyMismatch) {
		t.Errorf("expected ErrCurrencyMismatch, got %v", err)
	}

	// Negative initial balance
	negBal, _ := domain.NewMoneyFromDecimal("-10.00", "BRL")
	_, err = domain.NewWallet("wallet-1", "player-1", "BRL", negBal, now)
	if !errors.Is(err, domain.ErrNegativeAmount) {
		t.Errorf("expected ErrNegativeAmount, got %v", err)
	}

	// Empty ID
	_, err = domain.NewWallet("", "player-1", "BRL", initBal, now)
	if !errors.Is(err, domain.ErrWalletNotInitialized) {
		t.Errorf("expected ErrWalletNotInitialized, got %v", err)
	}
}

func TestWallet_DebitCredit(t *testing.T) {
	now := time.Now().UTC()
	initBal, _ := domain.NewMoneyFromDecimal("100.00", "BRL")
	w, _ := domain.NewWallet("wallet-1", "player-1", "BRL", initBal, now)

	// Valid debit
	debitAmount, _ := domain.NewMoneyFromDecimal("30.00", "BRL")
	err := w.Debit(debitAmount, now)
	if err != nil {
		t.Fatalf("unexpected error on debit: %v", err)
	}
	if w.Balance().String() != "70.00" {
		t.Errorf("expected balance 70.00, got %s", w.Balance().String())
	}
	if w.Version() != 2 {
		t.Errorf("expected version 2, got %d", w.Version())
	}

	// Insufficient balance debit
	tooBig, _ := domain.NewMoneyFromDecimal("80.00", "BRL")
	err = w.Debit(tooBig, now)
	if !errors.Is(err, domain.ErrInsufficientBalance) {
		t.Errorf("expected ErrInsufficientBalance, got %v", err)
	}
	// Balance and version must remain unchanged after failure
	if w.Balance().String() != "70.00" || w.Version() != 2 {
		t.Errorf("balance or version modified after failed debit: %s, v%d", w.Balance().String(), w.Version())
	}

	// Valid credit
	creditAmount, _ := domain.NewMoneyFromDecimal("50.00", "BRL")
	err = w.Credit(creditAmount, now)
	if err != nil {
		t.Fatalf("unexpected error on credit: %v", err)
	}
	if w.Balance().String() != "120.00" {
		t.Errorf("expected balance 120.00, got %s", w.Balance().String())
	}
	if w.Version() != 3 {
		t.Errorf("expected version 3, got %d", w.Version())
	}

	// Zero amount debit/credit
	zero, _ := domain.MoneyZero("BRL")
	err = w.Debit(zero, now)
	if !errors.Is(err, domain.ErrZeroAmountNotAllowed) {
		t.Errorf("expected ErrZeroAmountNotAllowed on zero debit, got %v", err)
	}
	err = w.Credit(zero, now)
	if !errors.Is(err, domain.ErrZeroAmountNotAllowed) {
		t.Errorf("expected ErrZeroAmountNotAllowed on zero credit, got %v", err)
	}

	// Currency mismatch on credit
	usd, _ := domain.NewMoneyFromDecimal("10.00", "USD")
	err = w.Credit(usd, now)
	if !errors.Is(err, domain.ErrCurrencyMismatch) {
		t.Errorf("expected ErrCurrencyMismatch on credit, got %v", err)
	}
}

func TestWallet_Rehydration(t *testing.T) {
	now := time.Now().UTC()
	bal, _ := domain.NewMoneyFromDecimal("500.00", "BRL")

	w, err := domain.RehydrateWallet("wallet-rehydrated", "player-1", "BRL", bal, 10, now, now)
	if err != nil {
		t.Fatalf("unexpected error rehydrating: %v", err)
	}

	if w.ID() != "wallet-rehydrated" || w.Version() != 10 || !w.Balance().Equals(bal) {
		t.Errorf("rehydrated wallet fields mismatch")
	}

	// Invalid version < 1
	_, err = domain.RehydrateWallet("w", "p", "BRL", bal, 0, now, now)
	if !errors.Is(err, domain.ErrInvalidWalletVersion) {
		t.Errorf("expected ErrInvalidWalletVersion, got %v", err)
	}
}

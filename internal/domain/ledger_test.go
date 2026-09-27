package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/domain"
)

func TestLedger_Validation(t *testing.T) {
	now := time.Now().UTC()
	m100, _ := domain.NewMoneyFromDecimal("100.00", "BRL")
	m25, _ := domain.NewMoneyFromDecimal("25.00", "BRL")
	m125, _ := domain.NewMoneyFromDecimal("125.00", "BRL")
	m75, _ := domain.NewMoneyFromDecimal("75.00", "BRL")
	mWrong, _ := domain.NewMoneyFromDecimal("120.00", "BRL")

	// 1. Valid CREDIT
	entryCredit, err := domain.NewLedgerEntry(
		"entry-1", "wallet-1", "tx-1",
		domain.DirectionCredit, m25, m100, m125, now,
	)
	if err != nil {
		t.Fatalf("unexpected error creating CREDIT ledger entry: %v", err)
	}
	if entryCredit.BalanceAfter().String() != "125.00" {
		t.Errorf("expected balanceAfter 125.00, got %s", entryCredit.BalanceAfter().String())
	}

	// 2. Valid DEBIT
	entryDebit, err := domain.NewLedgerEntry(
		"entry-2", "wallet-1", "tx-2",
		domain.DirectionDebit, m25, m100, m75, now,
	)
	if err != nil {
		t.Fatalf("unexpected error creating DEBIT ledger entry: %v", err)
	}
	if entryDebit.BalanceAfter().String() != "75.00" {
		t.Errorf("expected balanceAfter 75.00, got %s", entryDebit.BalanceAfter().String())
	}

	// 3. Mathematical mismatch CREDIT
	_, err = domain.NewLedgerEntry(
		"entry-3", "wallet-1", "tx-3",
		domain.DirectionCredit, m25, m100, mWrong, now,
	)
	if !errors.Is(err, domain.ErrInvalidLedgerMath) {
		t.Errorf("expected ErrInvalidLedgerMath on CREDIT mismatch, got %v", err)
	}

	// 4. Mathematical mismatch DEBIT
	_, err = domain.NewLedgerEntry(
		"entry-4", "wallet-1", "tx-4",
		domain.DirectionDebit, m25, m100, mWrong, now,
	)
	if !errors.Is(err, domain.ErrInvalidLedgerMath) {
		t.Errorf("expected ErrInvalidLedgerMath on DEBIT mismatch, got %v", err)
	}

	// 5. Invalid direction
	_, err = domain.NewLedgerEntry(
		"entry-5", "wallet-1", "tx-5",
		domain.LedgerDirection("UNKNOWN"), m25, m100, m125, now,
	)
	if !errors.Is(err, domain.ErrInvalidLedgerDirection) {
		t.Errorf("expected ErrInvalidLedgerDirection, got %v", err)
	}

	// 6. Currency mismatch
	usd25, _ := domain.NewMoneyFromDecimal("25.00", "USD")
	_, err = domain.NewLedgerEntry(
		"entry-6", "wallet-1", "tx-6",
		domain.DirectionCredit, usd25, m100, m125, now,
	)
	if !errors.Is(err, domain.ErrCurrencyMismatch) {
		t.Errorf("expected ErrCurrencyMismatch, got %v", err)
	}
}

func TestLedger_Rehydration(t *testing.T) {
	now := time.Now().UTC()
	m100, _ := domain.NewMoneyFromDecimal("100.00", "BRL")
	m25, _ := domain.NewMoneyFromDecimal("25.00", "BRL")
	m125, _ := domain.NewMoneyFromDecimal("125.00", "BRL")

	entry := domain.RehydrateLedgerEntry("entry-rehydrated", "wallet-1", "tx-1", domain.DirectionCredit, m25, m100, m125, now)
	if entry.ID() != "entry-rehydrated" || entry.Direction() != domain.DirectionCredit {
		t.Errorf("mismatch in rehydrated ledger entry")
	}
}

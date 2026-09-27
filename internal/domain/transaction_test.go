package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/domain"
)

func TestTransaction_CreationRules(t *testing.T) {
	now := time.Now().UTC()
	m25, _ := domain.NewMoneyFromDecimal("25.00", "BRL")
	mZero, _ := domain.MoneyZero("BRL")

	baseParams := domain.ExternalTransactionParams{
		ID:                    "tx-1",
		ProviderID:            "provider-a",
		ExternalTransactionID: "ext-1",
		IdempotencyKey:        "provider-a:ext-1",
		PayloadHash:           "dummy-hash",
		WalletID:              "wallet-1",
		PlayerID:              "player-1",
		RoundID:               "round-1",
		GameID:                "game-1",
		Kind:                  domain.KindBet,
		Money:                 m25,
		Now:                   now,
	}

	// 1. Valid BET
	tx, err := domain.NewExternalTransaction(baseParams)
	if err != nil {
		t.Fatalf("unexpected error creating BET: %v", err)
	}
	if tx.Status() != domain.StatusPending {
		t.Errorf("expected status PENDING, got %s", tx.Status())
	}
	if tx.Origin() != domain.OriginExternal {
		t.Errorf("expected origin EXTERNAL, got %s", tx.Origin())
	}

	// 2. Reject OPENING externally
	openingParams := baseParams
	openingParams.Kind = domain.KindOpening
	_, err = domain.NewExternalTransaction(openingParams)
	if !errors.Is(err, domain.ErrOpeningNotAllowedExternally) {
		t.Errorf("expected ErrOpeningNotAllowedExternally, got %v", err)
	}

	// 3. Valid LOSS with 0.00
	lossParams := baseParams
	lossParams.Kind = domain.KindLoss
	lossParams.Money = mZero
	txLoss, err := domain.NewExternalTransaction(lossParams)
	if err != nil {
		t.Fatalf("unexpected error creating LOSS: %v", err)
	}
	if !txLoss.Money().IsZero() {
		t.Errorf("expected LOSS money to be zero, got %s", txLoss.Money().String())
	}

	// 4. Reject LOSS with non-zero money
	lossNonZeroParams := baseParams
	lossNonZeroParams.Kind = domain.KindLoss
	lossNonZeroParams.Money = m25
	_, err = domain.NewExternalTransaction(lossNonZeroParams)
	if !errors.Is(err, domain.ErrLossAmountMustBeZero) {
		t.Errorf("expected ErrLossAmountMustBeZero, got %v", err)
	}

	// 5. Valid REFUND with reference
	refundParams := baseParams
	refundParams.Kind = domain.KindRefund
	refundParams.ReferenceExternalTransactionID = "ext-bet-1"
	txRefund, err := domain.NewExternalTransaction(refundParams)
	if err != nil {
		t.Fatalf("unexpected error creating REFUND: %v", err)
	}
	if txRefund.ReferenceExternalTransactionID() != "ext-bet-1" {
		t.Errorf("expected reference ext-bet-1, got %s", txRefund.ReferenceExternalTransactionID())
	}

	// 6. Reject REFUND without reference
	refundNoRef := baseParams
	refundNoRef.Kind = domain.KindRefund
	refundNoRef.ReferenceExternalTransactionID = ""
	_, err = domain.NewExternalTransaction(refundNoRef)
	if !errors.Is(err, domain.ErrMissingReference) {
		t.Errorf("expected ErrMissingReference for refund, got %v", err)
	}

	// 7. Missing metadata
	missingMeta := baseParams
	missingMeta.ProviderID = ""
	_, err = domain.NewExternalTransaction(missingMeta)
	if !errors.Is(err, domain.ErrExternalMetadataMissing) {
		t.Errorf("expected ErrExternalMetadataMissing, got %v", err)
	}
}

func TestTransaction_OpeningInternal(t *testing.T) {
	now := time.Now().UTC()
	initBal, _ := domain.NewMoneyFromDecimal("1000.00", "BRL")

	tx, err := domain.NewOpeningTransaction("open-tx-1", "wallet-1", "player-1", initBal, now)
	if err != nil {
		t.Fatalf("unexpected error creating OPENING: %v", err)
	}

	if tx.Origin() != domain.OriginInternal {
		t.Errorf("expected INTERNAL origin, got %s", tx.Origin())
	}
	if tx.Kind() != domain.KindOpening {
		t.Errorf("expected KindOpening, got %s", tx.Kind())
	}
	if tx.Status() != domain.StatusProcessed {
		t.Errorf("expected OPENING to be PROCESSED, got %s", tx.Status())
	}
	if tx.BalanceSnapshot() == nil || !tx.BalanceSnapshot().Equals(initBal) {
		t.Errorf("expected balance snapshot %v, got %v", initBal, tx.BalanceSnapshot())
	}

	// Reject OPENING with zero money
	zero, _ := domain.MoneyZero("BRL")
	_, err = domain.NewOpeningTransaction("open-tx-2", "wallet-1", "player-1", zero, now)
	if !errors.Is(err, domain.ErrZeroAmountNotAllowed) {
		t.Errorf("expected ErrZeroAmountNotAllowed for zero opening, got %v", err)
	}
}

func TestTransaction_StateMachine(t *testing.T) {
	now := time.Now().UTC()
	m25, _ := domain.NewMoneyFromDecimal("25.00", "BRL")
	balSnapshot, _ := domain.NewMoneyFromDecimal("975.00", "BRL")

	tx, _ := domain.NewExternalTransaction(domain.ExternalTransactionParams{
		ID:                    "tx-sm-1",
		ProviderID:            "provider-a",
		ExternalTransactionID: "ext-1",
		IdempotencyKey:        "provider-a:ext-1",
		PayloadHash:           "hash-1",
		WalletID:              "wallet-1",
		PlayerID:              "player-1",
		RoundID:               "round-1",
		GameID:                "game-1",
		Kind:                  domain.KindBet,
		Money:                 m25,
		Now:                   now,
	})

	// Transition PENDING -> PROCESSED
	err := tx.MarkProcessed(balSnapshot, now)
	if err != nil {
		t.Fatalf("unexpected error transitioning to PROCESSED: %v", err)
	}
	if tx.Status() != domain.StatusProcessed {
		t.Errorf("expected status PROCESSED, got %s", tx.Status())
	}
	if tx.BalanceSnapshot() == nil || !tx.BalanceSnapshot().Equals(balSnapshot) {
		t.Errorf("expected snapshot %v, got %v", balSnapshot, tx.BalanceSnapshot())
	}

	// Terminal state cannot be transitioned again
	err = tx.MarkRejected("SOME_CODE", now)
	if !errors.Is(err, domain.ErrTerminalState) {
		t.Errorf("expected ErrTerminalState when modifying PROCESSED, got %v", err)
	}

	// Pending Reference Transition
	txRef, _ := domain.NewExternalTransaction(domain.ExternalTransactionParams{
		ID:                             "tx-ref-1",
		ProviderID:                     "provider-a",
		ExternalTransactionID:          "ext-ref-1",
		IdempotencyKey:                 "key-ref-1",
		PayloadHash:                    "hash-ref",
		WalletID:                       "wallet-1",
		PlayerID:                       "player-1",
		RoundID:                        "round-1",
		GameID:                         "game-1",
		Kind:                           domain.KindRefund,
		Money:                          m25,
		ReferenceExternalTransactionID: "bet-original",
		Now:                            now,
	})

	err = txRef.MarkPendingReference(now)
	if err != nil {
		t.Fatalf("unexpected error transitioning to PENDING_REFERENCE: %v", err)
	}
	if txRef.Status() != domain.StatusPendingReference {
		t.Errorf("expected PENDING_REFERENCE, got %s", txRef.Status())
	}

	// Resolve reference and transition to PROCESSED
	txRef.ResolveInternalReference("internal-resolved-id")
	if txRef.ReferenceInternalTransactionID() != "internal-resolved-id" {
		t.Errorf("expected internal-resolved-id, got %s", txRef.ReferenceInternalTransactionID())
	}

	err = txRef.MarkProcessed(balSnapshot, now)
	if err != nil {
		t.Fatalf("unexpected error marking PROCESSED after pending reference: %v", err)
	}
	if txRef.Status() != domain.StatusProcessed {
		t.Errorf("expected PROCESSED, got %s", txRef.Status())
	}
}

package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/domain"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/postgres"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/postgres/repository"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type ProcessWagerTransactionUseCase struct {
	transactor postgres.Transactor
	walletRepo *repository.WalletRepository
	txRepo     *repository.TransactionRepository
	ledgerRepo *repository.LedgerRepository
	outboxRepo *repository.OutboxRepository
}

func NewProcessWagerTransactionUseCase(
	transactor postgres.Transactor,
	walletRepo *repository.WalletRepository,
	txRepo *repository.TransactionRepository,
	ledgerRepo *repository.LedgerRepository,
	outboxRepo *repository.OutboxRepository,
) *ProcessWagerTransactionUseCase {
	return &ProcessWagerTransactionUseCase{
		transactor: transactor,
		walletRepo: walletRepo,
		txRepo:     txRepo,
		ledgerRepo: ledgerRepo,
		outboxRepo: outboxRepo,
	}
}

// Execute processes a wager transaction within its own ACID transaction.
func (uc *ProcessWagerTransactionUseCase) Execute(ctx context.Context, input ProcessWagerInput) (*ProcessWagerOutput, error) {
	var output *ProcessWagerOutput
	err := uc.transactor.WithinTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		output, err = uc.ExecuteWithTx(ctx, tx, input)
		return err
	})
	if err != nil {
		return nil, err
	}
	return output, nil
}

// ExecuteWithTx executes the wager transaction within an existing SQL transaction (e.g. sharing with Inbox).
func (uc *ProcessWagerTransactionUseCase) ExecuteWithTx(ctx context.Context, tx pgx.Tx, input ProcessWagerInput) (*ProcessWagerOutput, error) {
	now := time.Now().UTC()

	// 1. Calculate deterministic Canonical Payload Hash
	payloadHash, err := ComputeCanonicalPayloadHash(CanonicalPayload{
		ExternalTransactionID:          input.ExternalTransactionID,
		GameID:                         input.GameID,
		Kind:                           string(input.Kind),
		Money:                          input.Money,
		PlayerID:                       input.PlayerID,
		ProviderID:                     input.ProviderID,
		ReferenceExternalTransactionID: input.ReferenceExternalTransactionID,
		RoundID:                        input.RoundID,
		WalletID:                       input.WalletID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to compute canonical hash: %w", err)
	}

	// 2. Execute with Pessimistic Row Lock on the specific wallet
	// All operations for the same wallet are strictly serialized here, preventing lost updates and race conditions.
	wallet, err := uc.walletRepo.GetByIDForUpdate(ctx, tx, input.WalletID)
	if err != nil {
		if errors.Is(err, repository.ErrWalletNotFound) {
			return nil, ErrWalletNotFound
		}
		return nil, fmt.Errorf("failed to lock wallet: %w", err)
	}

	// 3. Check Idempotency by (providerId, idempotencyKey) within active transaction and wallet lock
	existingByKey, err := uc.txRepo.GetByProviderAndIdempotencyKeyTx(ctx, tx, input.ProviderID, input.IdempotencyKey)
	if err != nil && !errors.Is(err, repository.ErrTransactionNotFound) {
		return nil, fmt.Errorf("failed checking idempotency key: %w", err)
	}

	if existingByKey != nil {
		// Key exists: check payload hash match
		if existingByKey.PayloadHash() == payloadHash {
			// Idempotent Replay: return persisted outcome and original balance snapshot
			var originalBal domain.Money
			if existingByKey.BalanceSnapshot() != nil {
				originalBal = *existingByKey.BalanceSnapshot()
			} else {
				originalBal, _ = domain.MoneyZero(input.Money.Currency())
			}

			return &ProcessWagerOutput{
				TransactionID:    existingByKey.ID(),
				Status:           existingByKey.Status(),
				Balance:          originalBal,
				IdempotentReplay: true,
				FailureCode:      existingByKey.FailureCode(),
			}, nil
		}

		// Reused key with different payload -> 409 Conflict
		return nil, ErrIdempotencyKeyConflict
	}

	// 4. Check if (providerId, externalTransactionId) exists under a different key within active transaction
	existingByExtID, err := uc.txRepo.GetByProviderAndExternalIDTx(ctx, tx, input.ProviderID, input.ExternalTransactionID)
	if err != nil && !errors.Is(err, repository.ErrTransactionNotFound) {
		return nil, fmt.Errorf("failed checking external transaction ID: %w", err)
	}
	if existingByExtID != nil {
		return nil, ErrExternalIDAlreadyExists
	}

	// 5. Construct WagerTransaction in domain PENDING state
	txID := uuid.NewString()
	wagerTx, err := domain.NewExternalTransaction(domain.ExternalTransactionParams{
		ID:                             txID,
		ProviderID:                     input.ProviderID,
		ExternalTransactionID:          input.ExternalTransactionID,
		IdempotencyKey:                 input.IdempotencyKey,
		PayloadHash:                    payloadHash,
		WalletID:                       input.WalletID,
		PlayerID:                       input.PlayerID,
		RoundID:                        input.RoundID,
		GameID:                         input.GameID,
		Kind:                           input.Kind,
		Money:                          input.Money,
		ReferenceExternalTransactionID: input.ReferenceExternalTransactionID,
		Now:                            now,
	})
	if err != nil {
		return nil, err
	}

	// Currency validation
	if wallet.Currency() != input.Money.Currency() {
		_ = wagerTx.MarkRejected(domain.FailureCodeInvalidCurrency, now)
		if err := uc.txRepo.Create(ctx, tx, wagerTx); err != nil {
			return nil, err
		}
		if err := uc.emitRejectedEvent(ctx, tx, wagerTx, input.CorrelationID, now); err != nil {
			return nil, err
		}
		return &ProcessWagerOutput{
			TransactionID:    txID,
			Status:           domain.StatusRejected,
			Balance:          wallet.Balance(),
			IdempotentReplay: false,
			FailureCode:      domain.FailureCodeInvalidCurrency,
		}, nil
	}

	switch input.Kind {
	case domain.KindBet:
		return uc.processBet(ctx, tx, wallet, wagerTx, input.CorrelationID, now)
	case domain.KindWin:
		return uc.processWin(ctx, tx, wallet, wagerTx, input.CorrelationID, now)
	case domain.KindLoss:
		return uc.processLoss(ctx, tx, wallet, wagerTx, input.CorrelationID, now)
	case domain.KindRefund:
		return uc.processRefund(ctx, tx, wallet, wagerTx, input.CorrelationID, now)
	case domain.KindRollback:
		return uc.processRollback(ctx, tx, wallet, wagerTx, input.CorrelationID, now)
	default:
		return nil, fmt.Errorf("%w: %s", domain.ErrInvalidTransactionKind, input.Kind)
	}
}

func (uc *ProcessWagerTransactionUseCase) processBet(
	ctx context.Context, tx pgx.Tx, wallet *domain.Wallet, wagerTx *domain.WagerTransaction,
	correlationID string, now time.Time,
) (*ProcessWagerOutput, error) {
	balBefore := wallet.Balance()

	// Attempt debit
	err := wallet.Debit(wagerTx.Money(), now)
	if err != nil {
		if errors.Is(err, domain.ErrInsufficientBalance) {
			// Insufficient funds: terminal business rejection
			if err := wagerTx.MarkRejected(domain.FailureCodeInsufficientFunds, now); err != nil {
				return nil, err
			}
			if err := uc.txRepo.Create(ctx, tx, wagerTx); err != nil {
				return nil, fmt.Errorf("failed to persist rejected bet: %w", err)
			}
			if err := uc.emitRejectedEvent(ctx, tx, wagerTx, correlationID, now); err != nil {
				return nil, err
			}

			return &ProcessWagerOutput{
				TransactionID:    wagerTx.ID(),
				Status:           domain.StatusRejected,
				Balance:          wallet.Balance(),
				IdempotentReplay: false,
				FailureCode:      domain.FailureCodeInsufficientFunds,
			}, nil
		}
		return nil, err
	}

	// Debit successful
	balAfter := wallet.Balance()
	if err := uc.walletRepo.Update(ctx, tx, wallet); err != nil {
		return nil, fmt.Errorf("failed to update wallet balance: %w", err)
	}

	if err := wagerTx.MarkProcessed(balAfter, now); err != nil {
		return nil, err
	}
	if err := uc.txRepo.Create(ctx, tx, wagerTx); err != nil {
		return nil, fmt.Errorf("failed to persist processed bet: %w", err)
	}

	// Create Debit Ledger Entry
	ledgerID := uuid.NewString()
	ledgerEntry, err := domain.NewLedgerEntry(
		ledgerID, wallet.ID(), wagerTx.ID(),
		domain.DirectionDebit, wagerTx.Money(), balBefore, balAfter, now,
	)
	if err != nil {
		return nil, err
	}
	if err := uc.ledgerRepo.Create(ctx, tx, ledgerEntry); err != nil {
		return nil, fmt.Errorf("failed to persist ledger entry: %w", err)
	}

	// Emit Outbox Events
	if err := uc.emitProcessedEvent(ctx, tx, wagerTx, balAfter, correlationID, now); err != nil {
		return nil, err
	}
	if err := uc.emitBalanceChangedEvent(ctx, tx, wallet.ID(), wagerTx.ID(), "DEBIT", wagerTx.Money(), balBefore, balAfter, wallet.Version(), correlationID, now); err != nil {
		return nil, err
	}

	return &ProcessWagerOutput{
		TransactionID:    wagerTx.ID(),
		Status:           domain.StatusProcessed,
		Balance:          balAfter,
		IdempotentReplay: false,
	}, nil
}

func (uc *ProcessWagerTransactionUseCase) processWin(
	ctx context.Context, tx pgx.Tx, wallet *domain.Wallet, wagerTx *domain.WagerTransaction,
	correlationID string, now time.Time,
) (*ProcessWagerOutput, error) {
	balBefore := wallet.Balance()

	if err := wallet.Credit(wagerTx.Money(), now); err != nil {
		return nil, err
	}
	balAfter := wallet.Balance()

	if err := uc.walletRepo.Update(ctx, tx, wallet); err != nil {
		return nil, fmt.Errorf("failed to update wallet balance: %w", err)
	}

	if err := wagerTx.MarkProcessed(balAfter, now); err != nil {
		return nil, err
	}
	if err := uc.txRepo.Create(ctx, tx, wagerTx); err != nil {
		return nil, fmt.Errorf("failed to persist processed win: %w", err)
	}

	// Create Credit Ledger Entry
	ledgerID := uuid.NewString()
	ledgerEntry, err := domain.NewLedgerEntry(
		ledgerID, wallet.ID(), wagerTx.ID(),
		domain.DirectionCredit, wagerTx.Money(), balBefore, balAfter, now,
	)
	if err != nil {
		return nil, err
	}
	if err := uc.ledgerRepo.Create(ctx, tx, ledgerEntry); err != nil {
		return nil, fmt.Errorf("failed to persist ledger entry: %w", err)
	}

	// Emit Outbox Events
	if err := uc.emitProcessedEvent(ctx, tx, wagerTx, balAfter, correlationID, now); err != nil {
		return nil, err
	}
	if err := uc.emitBalanceChangedEvent(ctx, tx, wallet.ID(), wagerTx.ID(), "CREDIT", wagerTx.Money(), balBefore, balAfter, wallet.Version(), correlationID, now); err != nil {
		return nil, err
	}

	return &ProcessWagerOutput{
		TransactionID:    wagerTx.ID(),
		Status:           domain.StatusProcessed,
		Balance:          balAfter,
		IdempotentReplay: false,
	}, nil
}

func (uc *ProcessWagerTransactionUseCase) processLoss(
	ctx context.Context, tx pgx.Tx, wallet *domain.Wallet, wagerTx *domain.WagerTransaction,
	correlationID string, now time.Time,
) (*ProcessWagerOutput, error) {
	// LOSS requires amount == 0.00. No wallet balance change, no ledger entry, version unchanged.
	currentBal := wallet.Balance()

	if err := wagerTx.MarkProcessed(currentBal, now); err != nil {
		return nil, err
	}
	if err := uc.txRepo.Create(ctx, tx, wagerTx); err != nil {
		return nil, fmt.Errorf("failed to persist processed loss: %w", err)
	}

	// Emit WagerTransactionProcessed (NO WalletBalanceChanged event!)
	if err := uc.emitProcessedEvent(ctx, tx, wagerTx, currentBal, correlationID, now); err != nil {
		return nil, err
	}

	return &ProcessWagerOutput{
		TransactionID:    wagerTx.ID(),
		Status:           domain.StatusProcessed,
		Balance:          currentBal,
		IdempotentReplay: false,
	}, nil
}

func (uc *ProcessWagerTransactionUseCase) processRefund(
	ctx context.Context, tx pgx.Tx, wallet *domain.Wallet, wagerTx *domain.WagerTransaction,
	correlationID string, now time.Time,
) (*ProcessWagerOutput, error) {
	// Look up referenced transaction
	refTx, err := uc.txRepo.GetByProviderAndExternalID(ctx, wagerTx.ProviderID(), wagerTx.ReferenceExternalTransactionID())
	if err != nil && !errors.Is(err, repository.ErrTransactionNotFound) {
		return nil, fmt.Errorf("failed to query reference tx: %w", err)
	}

	// 1. Reference not yet available -> transition to PENDING_REFERENCE
	if refTx == nil {
		if err := wagerTx.MarkPendingReference(now); err != nil {
			return nil, err
		}
		if err := uc.txRepo.Create(ctx, tx, wagerTx); err != nil {
			return nil, fmt.Errorf("failed to persist pending reference refund: %w", err)
		}
		if err := uc.emitPendingRefEvent(ctx, tx, wagerTx, correlationID, now); err != nil {
			return nil, err
		}

		return &ProcessWagerOutput{
			TransactionID:    wagerTx.ID(),
			Status:           domain.StatusPendingReference,
			Balance:          wallet.Balance(),
			IdempotentReplay: false,
		}, nil
	}

	// 2. Validate referenced transaction
	if refTx.Kind() != domain.KindBet || refTx.Status() != domain.StatusProcessed {
		return uc.rejectTransaction(ctx, tx, wagerTx, wallet, domain.FailureCodeReferenceMismatch, correlationID, now)
	}
	if refTx.WalletID() != wagerTx.WalletID() || refTx.PlayerID() != wagerTx.PlayerID() ||
		refTx.RoundID() != wagerTx.RoundID() || !refTx.Money().Equals(wagerTx.Money()) {
		return uc.rejectTransaction(ctx, tx, wagerTx, wallet, domain.FailureCodeReferenceMismatch, correlationID, now)
	}

	// Check if already refunded
	wagerTx.ResolveInternalReference(refTx.ID())

	// REFUND credits the wallet
	balBefore := wallet.Balance()
	if err := wallet.Credit(wagerTx.Money(), now); err != nil {
		return nil, err
	}
	balAfter := wallet.Balance()

	if err := uc.walletRepo.Update(ctx, tx, wallet); err != nil {
		return nil, fmt.Errorf("failed to update wallet on refund: %w", err)
	}

	if err := wagerTx.MarkProcessed(balAfter, now); err != nil {
		return nil, err
	}
	if err := uc.txRepo.Create(ctx, tx, wagerTx); err != nil {
		return nil, fmt.Errorf("failed to persist processed refund: %w", err)
	}

	// Credit Ledger Entry
	ledgerID := uuid.NewString()
	ledgerEntry, err := domain.NewLedgerEntry(
		ledgerID, wallet.ID(), wagerTx.ID(),
		domain.DirectionCredit, wagerTx.Money(), balBefore, balAfter, now,
	)
	if err != nil {
		return nil, err
	}
	if err := uc.ledgerRepo.Create(ctx, tx, ledgerEntry); err != nil {
		return nil, fmt.Errorf("failed to persist ledger entry: %w", err)
	}

	if err := uc.emitProcessedEvent(ctx, tx, wagerTx, balAfter, correlationID, now); err != nil {
		return nil, err
	}
	if err := uc.emitBalanceChangedEvent(ctx, tx, wallet.ID(), wagerTx.ID(), "CREDIT", wagerTx.Money(), balBefore, balAfter, wallet.Version(), correlationID, now); err != nil {
		return nil, err
	}

	return &ProcessWagerOutput{
		TransactionID:    wagerTx.ID(),
		Status:           domain.StatusProcessed,
		Balance:          balAfter,
		IdempotentReplay: false,
	}, nil
}

func (uc *ProcessWagerTransactionUseCase) processRollback(
	ctx context.Context, tx pgx.Tx, wallet *domain.Wallet, wagerTx *domain.WagerTransaction,
	correlationID string, now time.Time,
) (*ProcessWagerOutput, error) {
	refTx, err := uc.txRepo.GetByProviderAndExternalID(ctx, wagerTx.ProviderID(), wagerTx.ReferenceExternalTransactionID())
	if err != nil && !errors.Is(err, repository.ErrTransactionNotFound) {
		return nil, fmt.Errorf("failed to query reference tx: %w", err)
	}

	// Reference not available yet
	if refTx == nil {
		if err := wagerTx.MarkPendingReference(now); err != nil {
			return nil, err
		}
		if err := uc.txRepo.Create(ctx, tx, wagerTx); err != nil {
			return nil, fmt.Errorf("failed to persist pending reference rollback: %w", err)
		}
		if err := uc.emitPendingRefEvent(ctx, tx, wagerTx, correlationID, now); err != nil {
			return nil, err
		}

		return &ProcessWagerOutput{
			TransactionID:    wagerTx.ID(),
			Status:           domain.StatusPendingReference,
			Balance:          wallet.Balance(),
			IdempotentReplay: false,
		}, nil
	}

	// Validate reference
	if refTx.Status() != domain.StatusProcessed {
		return uc.rejectTransaction(ctx, tx, wagerTx, wallet, domain.FailureCodeReferenceMismatch, correlationID, now)
	}
	if refTx.WalletID() != wagerTx.WalletID() || refTx.PlayerID() != wagerTx.PlayerID() ||
		refTx.RoundID() != wagerTx.RoundID() || !refTx.Money().Equals(wagerTx.Money()) {
		return uc.rejectTransaction(ctx, tx, wagerTx, wallet, domain.FailureCodeReferenceMismatch, correlationID, now)
	}

	wagerTx.ResolveInternalReference(refTx.ID())
	balBefore := wallet.Balance()

	// Direction opposite of reference
	var (
		ledgerDir domain.LedgerDirection
		balAfter  domain.Money
	)

	switch refTx.Kind() {
	case domain.KindBet:
		// Undoing BET (debit) -> CREDIT
		ledgerDir = domain.DirectionCredit
		if err := wallet.Credit(wagerTx.Money(), now); err != nil {
			return nil, err
		}
		balAfter = wallet.Balance()

	case domain.KindWin, domain.KindRefund:
		// Undoing WIN or REFUND (credit) -> DEBIT
		ledgerDir = domain.DirectionDebit
		err := wallet.Debit(wagerTx.Money(), now)
		if err != nil {
			if errors.Is(err, domain.ErrInsufficientBalance) {
				// Rejection specific to reversal debit insufficient funds
				return uc.rejectTransaction(ctx, tx, wagerTx, wallet, domain.FailureCodeReversalInsufficientFunds, correlationID, now)
			}
			return nil, err
		}
		balAfter = wallet.Balance()

	default:
		return uc.rejectTransaction(ctx, tx, wagerTx, wallet, domain.FailureCodeReferenceMismatch, correlationID, now)
	}

	if err := uc.walletRepo.Update(ctx, tx, wallet); err != nil {
		return nil, fmt.Errorf("failed to update wallet on rollback: %w", err)
	}

	if err := wagerTx.MarkProcessed(balAfter, now); err != nil {
		return nil, err
	}
	if err := uc.txRepo.Create(ctx, tx, wagerTx); err != nil {
		return nil, fmt.Errorf("failed to persist processed rollback: %w", err)
	}

	// Ledger Entry
	ledgerID := uuid.NewString()
	ledgerEntry, err := domain.NewLedgerEntry(
		ledgerID, wallet.ID(), wagerTx.ID(),
		ledgerDir, wagerTx.Money(), balBefore, balAfter, now,
	)
	if err != nil {
		return nil, err
	}
	if err := uc.ledgerRepo.Create(ctx, tx, ledgerEntry); err != nil {
		return nil, fmt.Errorf("failed to persist ledger entry: %w", err)
	}

	if err := uc.emitProcessedEvent(ctx, tx, wagerTx, balAfter, correlationID, now); err != nil {
		return nil, err
	}
	if err := uc.emitBalanceChangedEvent(ctx, tx, wallet.ID(), wagerTx.ID(), string(ledgerDir), wagerTx.Money(), balBefore, balAfter, wallet.Version(), correlationID, now); err != nil {
		return nil, err
	}

	return &ProcessWagerOutput{
		TransactionID:    wagerTx.ID(),
		Status:           domain.StatusProcessed,
		Balance:          balAfter,
		IdempotentReplay: false,
	}, nil
}

func (uc *ProcessWagerTransactionUseCase) rejectTransaction(
	ctx context.Context, tx pgx.Tx, wagerTx *domain.WagerTransaction, wallet *domain.Wallet,
	failureCode, correlationID string, now time.Time,
) (*ProcessWagerOutput, error) {
	if err := wagerTx.MarkRejected(failureCode, now); err != nil {
		return nil, err
	}
	if err := uc.txRepo.Create(ctx, tx, wagerTx); err != nil {
		return nil, fmt.Errorf("failed to persist rejected transaction: %w", err)
	}
	if err := uc.emitRejectedEvent(ctx, tx, wagerTx, correlationID, now); err != nil {
		return nil, err
	}

	return &ProcessWagerOutput{
		TransactionID:    wagerTx.ID(),
		Status:           domain.StatusRejected,
		Balance:          wallet.Balance(),
		IdempotentReplay: false,
		FailureCode:      failureCode,
	}, nil
}

// Helpers for Outbox Event Emission

func (uc *ProcessWagerTransactionUseCase) emitProcessedEvent(
	ctx context.Context, tx pgx.Tx, wagerTx *domain.WagerTransaction, balSnapshot domain.Money,
	correlationID string, now time.Time,
) error {
	payload := domain.WagerTransactionProcessedPayload{
		TransactionID:         wagerTx.ID(),
		Origin:                string(wagerTx.Origin()),
		ProviderID:            wagerTx.ProviderID(),
		ExternalTransactionID: wagerTx.ExternalTransactionID(),
		WalletID:              wagerTx.WalletID(),
		PlayerID:              wagerTx.PlayerID(),
		RoundID:               wagerTx.RoundID(),
		GameID:                wagerTx.GameID(),
		Kind:                  string(wagerTx.Kind()),
		Money:                 wagerTx.Money(),
		BalanceSnapshot:       &balSnapshot,
	}
	env, err := domain.NewEventEnvelope(
		uuid.NewString(), domain.EventWagerTransactionProcessed,
		wagerTx.WalletID(), correlationID, "", now, 1, payload,
	)
	if err != nil {
		return err
	}
	return uc.outboxRepo.Create(ctx, tx, env, "WagerTransaction")
}

func (uc *ProcessWagerTransactionUseCase) emitRejectedEvent(
	ctx context.Context, tx pgx.Tx, wagerTx *domain.WagerTransaction,
	correlationID string, now time.Time,
) error {
	payload := domain.WagerTransactionRejectedPayload{
		TransactionID:         wagerTx.ID(),
		ProviderID:            wagerTx.ProviderID(),
		ExternalTransactionID: wagerTx.ExternalTransactionID(),
		WalletID:              wagerTx.WalletID(),
		PlayerID:              wagerTx.PlayerID(),
		RoundID:               wagerTx.RoundID(),
		GameID:                wagerTx.GameID(),
		Kind:                  string(wagerTx.Kind()),
		Money:                 wagerTx.Money(),
		FailureCode:           wagerTx.FailureCode(),
	}
	env, err := domain.NewEventEnvelope(
		uuid.NewString(), domain.EventWagerTransactionRejected,
		wagerTx.WalletID(), correlationID, "", now, 1, payload,
	)
	if err != nil {
		return err
	}
	return uc.outboxRepo.Create(ctx, tx, env, "WagerTransaction")
}

func (uc *ProcessWagerTransactionUseCase) emitBalanceChangedEvent(
	ctx context.Context, tx pgx.Tx, walletID, transactionID, direction string,
	money, balanceBefore, balanceAfter domain.Money, walletVersion int64,
	correlationID string, now time.Time,
) error {
	payload := domain.WalletBalanceChangedPayload{
		WalletID:      walletID,
		TransactionID: transactionID,
		Direction:     direction,
		Money:         money,
		BalanceBefore: balanceBefore,
		BalanceAfter:  balanceAfter,
		WalletVersion: walletVersion,
	}
	env, err := domain.NewEventEnvelope(
		uuid.NewString(), domain.EventWalletBalanceChanged,
		walletID, correlationID, "", now, 1, payload,
	)
	if err != nil {
		return err
	}
	return uc.outboxRepo.Create(ctx, tx, env, "Wallet")
}

func (uc *ProcessWagerTransactionUseCase) emitPendingRefEvent(
	ctx context.Context, tx pgx.Tx, wagerTx *domain.WagerTransaction,
	correlationID string, now time.Time,
) error {
	payload := domain.WagerTransactionPendingReferencePayload{
		TransactionID:                  wagerTx.ID(),
		ProviderID:                     wagerTx.ProviderID(),
		ExternalTransactionID:          wagerTx.ExternalTransactionID(),
		ReferenceExternalTransactionID: wagerTx.ReferenceExternalTransactionID(),
		WalletID:                       wagerTx.WalletID(),
		PlayerID:                       wagerTx.PlayerID(),
		RoundID:                        wagerTx.RoundID(),
		Kind:                           string(wagerTx.Kind()),
		Money:                          wagerTx.Money(),
	}
	env, err := domain.NewEventEnvelope(
		uuid.NewString(), domain.EventWagerTransactionPendingReference,
		wagerTx.WalletID(), correlationID, "", now, 1, payload,
	)
	if err != nil {
		return err
	}
	return uc.outboxRepo.Create(ctx, tx, env, "WagerTransaction")
}

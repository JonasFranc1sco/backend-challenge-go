package worker

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/domain"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/postgres"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/postgres/repository"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type PendingReferenceWorker struct {
	transactor   postgres.Transactor
	walletRepo   *repository.WalletRepository
	txRepo       *repository.TransactionRepository
	ledgerRepo   *repository.LedgerRepository
	outboxRepo   *repository.OutboxRepository
	pollInterval time.Duration
	maxRetries   int
	stopChan     chan struct{}
	wg           sync.WaitGroup
}

func NewPendingReferenceWorker(
	transactor postgres.Transactor,
	walletRepo *repository.WalletRepository,
	txRepo *repository.TransactionRepository,
	ledgerRepo *repository.LedgerRepository,
	outboxRepo *repository.OutboxRepository,
	pollInterval time.Duration,
	maxRetries int,
) *PendingReferenceWorker {
	if pollInterval <= 0 {
		pollInterval = 1 * time.Second
	}
	if maxRetries <= 0 {
		maxRetries = 5
	}
	return &PendingReferenceWorker{
		transactor:   transactor,
		walletRepo:   walletRepo,
		txRepo:       txRepo,
		ledgerRepo:   ledgerRepo,
		outboxRepo:   outboxRepo,
		pollInterval: pollInterval,
		maxRetries:   maxRetries,
		stopChan:     make(chan struct{}),
	}
}

// Start begins background polling for pending references.
func (w *PendingReferenceWorker) Start(ctx context.Context) {
	w.wg.Add(1)
	go w.runLoop(ctx)
}

// Stop cleanly terminates the worker waiting for in-flight evaluations to complete.
func (w *PendingReferenceWorker) Stop(ctx context.Context) error {
	close(w.stopChan)
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *PendingReferenceWorker) runLoop(ctx context.Context) {
	defer w.wg.Done()
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stopChan:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.processBatch(ctx)
		}
	}
}

// processBatch retrieves and resolves pending references with row locks.
func (w *PendingReferenceWorker) processBatch(ctx context.Context) {
	_ = w.transactor.WithinTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		pendingTxs, err := w.txRepo.FetchPendingReferencesForRetry(ctx, tx, 25)
		if err != nil {
			return err
		}

		now := time.Now().UTC()
		for _, pendingTx := range pendingTxs {
			w.evaluatePendingTx(ctx, tx, pendingTx, now)
		}

		return nil
	})
}

func (w *PendingReferenceWorker) evaluatePendingTx(
	ctx context.Context, tx pgx.Tx, pendingTx *domain.WagerTransaction, now time.Time,
) {
	// 1. Check if max retry attempts or TTL expired
	if pendingTx.RetryCount() >= w.maxRetries {
		_ = pendingTx.MarkRejected(domain.FailureCodeReferenceNotFound, now)
		_ = w.txRepo.Update(ctx, tx, pendingTx)
		_ = w.emitRejectedEvent(ctx, tx, pendingTx, uuid.NewString(), now)
		return
	}

	// 2. Query referenced transaction
	refTx, err := w.txRepo.GetByProviderAndExternalID(ctx, pendingTx.ProviderID(), pendingTx.ReferenceExternalTransactionID())
	if err != nil && !errors.Is(err, repository.ErrTransactionNotFound) {
		return
	}

	// Reference still not arrived
	if refTx == nil {
		backoffSeconds := math.Pow(2, float64(pendingTx.RetryCount()))
		if backoffSeconds > 30 {
			backoffSeconds = 30
		}
		nextRetry := now.Add(time.Duration(backoffSeconds) * time.Second)
		_ = w.txRepo.ScheduleReferenceRetry(ctx, tx, pendingTx.ID(), nextRetry)
		return
	}

	// If reference is still pending, wait
	if refTx.Status() == domain.StatusPending || refTx.Status() == domain.StatusPendingReference {
		nextRetry := now.Add(1 * time.Second)
		_ = w.txRepo.ScheduleReferenceRetry(ctx, tx, pendingTx.ID(), nextRetry)
		return
	}

	// If reference ended in failure, reject immediately
	if refTx.Status() == domain.StatusRejected || refTx.Status() == domain.StatusFailed {
		_ = pendingTx.MarkRejected(domain.FailureCodeReferenceFailed, now)
		_ = w.txRepo.Update(ctx, tx, pendingTx)
		_ = w.emitRejectedEvent(ctx, tx, pendingTx, uuid.NewString(), now)
		return
	}

	// Reference is PROCESSED: acquire lock on wallet and resolve
	wallet, err := w.walletRepo.GetByIDForUpdate(ctx, tx, pendingTx.WalletID())
	if err != nil {
		return
	}

	// Validate reference parameters
	if refTx.WalletID() != pendingTx.WalletID() || refTx.PlayerID() != pendingTx.PlayerID() ||
		refTx.RoundID() != pendingTx.RoundID() || !refTx.Money().Equals(pendingTx.Money()) {
		_ = pendingTx.MarkRejected(domain.FailureCodeReferenceMismatch, now)
		_ = w.txRepo.Update(ctx, tx, pendingTx)
		_ = w.emitRejectedEvent(ctx, tx, pendingTx, uuid.NewString(), now)
		return
	}

	pendingTx.ResolveInternalReference(refTx.ID())
	balBefore := wallet.Balance()

	var (
		ledgerDir domain.LedgerDirection
		balAfter  domain.Money
	)

	switch pendingTx.Kind() {
	case domain.KindRefund:
		if refTx.Kind() != domain.KindBet {
			_ = pendingTx.MarkRejected(domain.FailureCodeReferenceMismatch, now)
			_ = w.txRepo.Update(ctx, tx, pendingTx)
			_ = w.emitRejectedEvent(ctx, tx, pendingTx, uuid.NewString(), now)
			return
		}
		ledgerDir = domain.DirectionCredit
		_ = wallet.Credit(pendingTx.Money(), now)
		balAfter = wallet.Balance()

	case domain.KindRollback:
		switch refTx.Kind() {
		case domain.KindBet:
			ledgerDir = domain.DirectionCredit
			_ = wallet.Credit(pendingTx.Money(), now)
			balAfter = wallet.Balance()

		case domain.KindWin, domain.KindRefund:
			ledgerDir = domain.DirectionDebit
			if err := wallet.Debit(pendingTx.Money(), now); err != nil {
				if errors.Is(err, domain.ErrInsufficientBalance) {
					_ = pendingTx.MarkRejected(domain.FailureCodeReversalInsufficientFunds, now)
					_ = w.txRepo.Update(ctx, tx, pendingTx)
					_ = w.emitRejectedEvent(ctx, tx, pendingTx, uuid.NewString(), now)
					return
				}
				return
			}
			balAfter = wallet.Balance()

		default:
			_ = pendingTx.MarkRejected(domain.FailureCodeReferenceMismatch, now)
			_ = w.txRepo.Update(ctx, tx, pendingTx)
			_ = w.emitRejectedEvent(ctx, tx, pendingTx, uuid.NewString(), now)
			return
		}
	default:
		return
	}

	// Update wallet and mark transaction processed
	_ = w.walletRepo.Update(ctx, tx, wallet)
	_ = pendingTx.MarkProcessed(balAfter, now)
	_ = w.txRepo.Update(ctx, tx, pendingTx)

	// Create Ledger Entry
	ledgerID := uuid.NewString()
	ledgerEntry, _ := domain.NewLedgerEntry(
		ledgerID, wallet.ID(), pendingTx.ID(),
		ledgerDir, pendingTx.Money(), balBefore, balAfter, now,
	)
	_ = w.ledgerRepo.Create(ctx, tx, ledgerEntry)

	// Emit events
	_ = w.emitProcessedEvent(ctx, tx, pendingTx, balAfter, uuid.NewString(), now)
	_ = w.emitBalanceChangedEvent(ctx, tx, wallet.ID(), pendingTx.ID(), string(ledgerDir), pendingTx.Money(), balBefore, balAfter, wallet.Version(), uuid.NewString(), now)
}

func (w *PendingReferenceWorker) emitProcessedEvent(
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
	return w.outboxRepo.Create(ctx, tx, env, "WagerTransaction")
}

func (w *PendingReferenceWorker) emitRejectedEvent(
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
	return w.outboxRepo.Create(ctx, tx, env, "WagerTransaction")
}

func (w *PendingReferenceWorker) emitBalanceChangedEvent(
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
	return w.outboxRepo.Create(ctx, tx, env, "Wallet")
}

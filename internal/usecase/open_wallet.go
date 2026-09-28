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

type OpenWalletUseCase struct {
	transactor postgres.Transactor
	walletRepo *repository.WalletRepository
	txRepo     *repository.TransactionRepository
	ledgerRepo *repository.LedgerRepository
	outboxRepo *repository.OutboxRepository
}

func NewOpenWalletUseCase(
	transactor postgres.Transactor,
	walletRepo *repository.WalletRepository,
	txRepo *repository.TransactionRepository,
	ledgerRepo *repository.LedgerRepository,
	outboxRepo *repository.OutboxRepository,
) *OpenWalletUseCase {
	return &OpenWalletUseCase{
		transactor: transactor,
		walletRepo: walletRepo,
		txRepo:     txRepo,
		ledgerRepo: ledgerRepo,
		outboxRepo: outboxRepo,
	}
}

// Execute opens a new player wallet with ACID transaction guarantees.
// If initial balance > 0: atomically creates wallet, OPENING transaction, credit ledger, and outbox events.
// If initial balance == 0: creates wallet without OPENING, ledger or financial events.
func (uc *OpenWalletUseCase) Execute(ctx context.Context, input OpenWalletInput) (*OpenWalletOutput, error) {
	currency := input.InitialBalance.Currency()
	now := time.Now().UTC()

	// Check if wallet already exists for player and currency
	existing, err := uc.walletRepo.GetByPlayerAndCurrency(ctx, input.PlayerID, currency)
	if err != nil && !errors.Is(err, repository.ErrWalletNotFound) {
		return nil, fmt.Errorf("failed checking existing wallet: %w", err)
	}
	if existing != nil {
		return nil, ErrWalletAlreadyExists
	}

	walletID := uuid.NewString()
	wallet, err := domain.NewWallet(walletID, input.PlayerID, currency, input.InitialBalance, now)
	if err != nil {
		return nil, err
	}

	err = uc.transactor.WithinTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		// 1. Create Wallet
		if err := uc.walletRepo.Create(ctx, tx, wallet); err != nil {
			return fmt.Errorf("failed to create wallet: %w", err)
		}

		// 2. If positive initial balance, create OPENING, Ledger and Outbox events
		if input.InitialBalance.IsPositive() {
			txID := uuid.NewString()
			openingTx, err := domain.NewOpeningTransaction(txID, walletID, input.PlayerID, input.InitialBalance, now)
			if err != nil {
				return err
			}

			if err := uc.txRepo.Create(ctx, tx, openingTx); err != nil {
				return fmt.Errorf("failed to create opening transaction: %w", err)
			}

			zeroMoney, _ := domain.MoneyZero(currency)
			ledgerID := uuid.NewString()
			ledgerEntry, err := domain.NewLedgerEntry(
				ledgerID, walletID, txID,
				domain.DirectionCredit, input.InitialBalance, zeroMoney, input.InitialBalance, now,
			)
			if err != nil {
				return err
			}

			if err := uc.ledgerRepo.Create(ctx, tx, ledgerEntry); err != nil {
				return fmt.Errorf("failed to create opening ledger entry: %w", err)
			}

			// Outbox: WagerTransactionProcessed
			snapshot := input.InitialBalance
			processedPayload := domain.WagerTransactionProcessedPayload{
				TransactionID:   txID,
				Origin:          string(domain.OriginInternal),
				WalletID:        walletID,
				PlayerID:        input.PlayerID,
				Kind:            string(domain.KindOpening),
				Money:           input.InitialBalance,
				BalanceSnapshot: &snapshot,
			}
			processedEnv, err := domain.NewEventEnvelope(
				uuid.NewString(),
				domain.EventWagerTransactionProcessed,
				walletID,
				uuid.NewString(),
				"",
				now,
				1,
				processedPayload,
			)
			if err != nil {
				return err
			}
			if err := uc.outboxRepo.Create(ctx, tx, processedEnv, "Wallet"); err != nil {
				return fmt.Errorf("failed to create outbox event: %w", err)
			}

			// Outbox: WalletBalanceChanged
			balancePayload := domain.WalletBalanceChangedPayload{
				WalletID:      walletID,
				TransactionID: txID,
				Direction:     string(domain.DirectionCredit),
				Money:         input.InitialBalance,
				BalanceBefore: zeroMoney,
				BalanceAfter:  input.InitialBalance,
				WalletVersion: 1,
			}
			balanceEnv, err := domain.NewEventEnvelope(
				uuid.NewString(),
				domain.EventWalletBalanceChanged,
				walletID,
				uuid.NewString(),
				"",
				now,
				1,
				balancePayload,
			)
			if err != nil {
				return err
			}
			if err := uc.outboxRepo.Create(ctx, tx, balanceEnv, "Wallet"); err != nil {
				return fmt.Errorf("failed to create outbox balance event: %w", err)
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return &OpenWalletOutput{
		ID:       walletID,
		PlayerID: input.PlayerID,
		Balance:  input.InitialBalance,
		Version:  1,
	}, nil
}

package usecase

import (
	"context"
	"fmt"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/domain"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/postgres/repository"
)

type ReconciliationUseCase struct {
	walletRepo *repository.WalletRepository
	ledgerRepo *repository.LedgerRepository
}

func NewReconciliationUseCase(
	walletRepo *repository.WalletRepository,
	ledgerRepo *repository.LedgerRepository,
) *ReconciliationUseCase {
	return &ReconciliationUseCase{
		walletRepo: walletRepo,
		ledgerRepo: ledgerRepo,
	}
}

type ReconciliationOutput struct {
	WalletID          string       `json:"walletId"`
	StoredBalance     domain.Money `json:"storedBalance"`
	CalculatedBalance domain.Money `json:"calculatedBalance"`
	Difference        domain.Money `json:"difference"`
	Consistent        bool         `json:"consistent"`
	CheckedEntries    int          `json:"checkedEntries"`
}

// Execute performs an audit by reconstructing the balance from ledger entries
// and comparing it with the persisted wallet balance.
func (uc *ReconciliationUseCase) Execute(ctx context.Context, walletID string) (*ReconciliationOutput, error) {
	wallet, err := uc.walletRepo.GetByID(ctx, walletID)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve wallet: %w", err)
	}

	currency := wallet.Currency()

	// Sum credits and debits from append-only ledger
	totalCredits, totalDebits, count, err := uc.ledgerRepo.GetBalanceSumByWallet(ctx, nil, walletID)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate ledger entries: %w", err)
	}

	calculatedUnits := totalCredits - totalDebits
	storedUnits := wallet.Balance().Units()
	diffUnits := storedUnits - calculatedUnits

	calculatedMoney, err := domain.NewMoney(calculatedUnits, currency)
	if err != nil {
		return nil, err
	}

	diffMoney, err := domain.NewMoney(diffUnits, currency)
	if err != nil {
		return nil, err
	}

	consistent := diffUnits == 0

	return &ReconciliationOutput{
		WalletID:          walletID,
		StoredBalance:     wallet.Balance(),
		CalculatedBalance: calculatedMoney,
		Difference:        diffMoney,
		Consistent:        consistent,
		CheckedEntries:    count,
	}, nil
}

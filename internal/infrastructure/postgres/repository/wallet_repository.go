package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrWalletNotFound = errors.New("wallet not found")
	ErrWalletConflict = errors.New("wallet already exists for player and currency")
)

type WalletRepository struct {
	pool *pgxpool.Pool
}

func NewWalletRepository(pool *pgxpool.Pool) *WalletRepository {
	return &WalletRepository{pool: pool}
}

// Create inserts a new wallet into the database within an active transaction.
func (r *WalletRepository) Create(ctx context.Context, tx pgx.Tx, wallet *domain.Wallet) error {
	query := `
		INSERT INTO wallets (id, player_id, currency, balance, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`
	_, err := tx.Exec(ctx, query,
		wallet.ID(),
		wallet.PlayerID(),
		wallet.Currency(),
		wallet.Balance().Units(),
		wallet.Version(),
		wallet.CreatedAt(),
		wallet.UpdatedAt(),
	)
	if err != nil {
		return err
	}
	return nil
}

// GetByID retrieves a wallet by its ID without locking.
func (r *WalletRepository) GetByID(ctx context.Context, id string) (*domain.Wallet, error) {
	query := `
		SELECT id, player_id, currency, balance, version, created_at, updated_at
		FROM wallets
		WHERE id = $1
	`
	var (
		wID, playerID, currency string
		balanceUnits, version   int64
		createdAt, updatedAt    time.Time
	)

	err := r.pool.QueryRow(ctx, query, id).Scan(
		&wID, &playerID, &currency, &balanceUnits, &version, &createdAt, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrWalletNotFound
		}
		return nil, fmt.Errorf("failed to get wallet %s: %w", id, err)
	}

	money, err := domain.NewMoney(balanceUnits, currency)
	if err != nil {
		return nil, err
	}

	return domain.RehydrateWallet(wID, playerID, currency, money, version, createdAt, updatedAt)
}

// GetByIDForUpdate acquires a pessimistic row lock (SELECT ... FOR UPDATE) on the wallet.
// This strictly serializes concurrent requests for the same wallet while permitting independent wallets
// to process concurrently with zero global locks.
func (r *WalletRepository) GetByIDForUpdate(ctx context.Context, tx pgx.Tx, id string) (*domain.Wallet, error) {
	query := `
		SELECT id, player_id, currency, balance, version, created_at, updated_at
		FROM wallets
		WHERE id = $1
		FOR UPDATE
	`
	var (
		wID, playerID, currency string
		balanceUnits, version   int64
		createdAt, updatedAt    time.Time
	)

	err := tx.QueryRow(ctx, query, id).Scan(
		&wID, &playerID, &currency, &balanceUnits, &version, &createdAt, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrWalletNotFound
		}
		return nil, fmt.Errorf("failed to lock wallet %s: %w", id, err)
	}

	money, err := domain.NewMoney(balanceUnits, currency)
	if err != nil {
		return nil, err
	}

	return domain.RehydrateWallet(wID, playerID, currency, money, version, createdAt, updatedAt)
}

// GetByPlayerAndCurrency checks for an existing wallet by player ID and currency.
func (r *WalletRepository) GetByPlayerAndCurrency(ctx context.Context, playerID, currency string) (*domain.Wallet, error) {
	query := `
		SELECT id, player_id, currency, balance, version, created_at, updated_at
		FROM wallets
		WHERE player_id = $1 AND currency = $2
	`
	var (
		wID, pID, curr       string
		balanceUnits, version int64
		createdAt, updatedAt  time.Time
	)

	err := r.pool.QueryRow(ctx, query, playerID, currency).Scan(
		&wID, &pID, &curr, &balanceUnits, &version, &createdAt, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrWalletNotFound
		}
		return nil, fmt.Errorf("failed to get wallet for player %s currency %s: %w", playerID, currency, err)
	}

	money, err := domain.NewMoney(balanceUnits, curr)
	if err != nil {
		return nil, err
	}

	return domain.RehydrateWallet(wID, pID, curr, money, version, createdAt, updatedAt)
}

// Update updates the balance, version, and updated_at timestamp of a locked wallet within an active transaction.
func (r *WalletRepository) Update(ctx context.Context, tx pgx.Tx, wallet *domain.Wallet) error {
	query := `
		UPDATE wallets
		SET balance = $1, version = $2, updated_at = $3
		WHERE id = $4
	`
	cmdTag, err := tx.Exec(ctx, query,
		wallet.Balance().Units(),
		wallet.Version(),
		wallet.UpdatedAt(),
		wallet.ID(),
	)
	if err != nil {
		return fmt.Errorf("failed to update wallet %s: %w", wallet.ID(), err)
	}
	if cmdTag.RowsAffected() == 0 {
		return ErrWalletNotFound
	}
	return nil
}

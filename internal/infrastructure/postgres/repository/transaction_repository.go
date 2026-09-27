package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrTransactionNotFound = errors.New("wager transaction not found")
)

type TransactionRepository struct {
	pool *pgxpool.Pool
}

func NewTransactionRepository(pool *pgxpool.Pool) *TransactionRepository {
	return &TransactionRepository{pool: pool}
}

// Create inserts a new WagerTransaction into the database.
func (r *TransactionRepository) Create(ctx context.Context, tx pgx.Tx, t *domain.WagerTransaction) error {
	query := `
		INSERT INTO wager_transactions (
			id, origin, provider_id, external_transaction_id, idempotency_key, payload_hash,
			wallet_id, player_id, round_id, game_id, kind, amount, currency,
			reference_external_transaction_id, reference_internal_transaction_id,
			status, failure_code, balance_snapshot, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11, $12, $13,
			$14, $15,
			$16, $17, $18, $19, $20
		)
	`
	var (
		providerID, extID, key, hash, roundID, gameID, refExtID, refIntID, failCode sql.NullString
		balSnapshot                                                                 sql.NullInt64
	)

	if t.ProviderID() != "" {
		providerID = sql.NullString{String: t.ProviderID(), Valid: true}
	}
	if t.ExternalTransactionID() != "" {
		extID = sql.NullString{String: t.ExternalTransactionID(), Valid: true}
	}
	if t.IdempotencyKey() != "" {
		key = sql.NullString{String: t.IdempotencyKey(), Valid: true}
	}
	if t.PayloadHash() != "" {
		hash = sql.NullString{String: t.PayloadHash(), Valid: true}
	}
	if t.RoundID() != "" {
		roundID = sql.NullString{String: t.RoundID(), Valid: true}
	}
	if t.GameID() != "" {
		gameID = sql.NullString{String: t.GameID(), Valid: true}
	}
	if t.ReferenceExternalTransactionID() != "" {
		refExtID = sql.NullString{String: t.ReferenceExternalTransactionID(), Valid: true}
	}
	if t.ReferenceInternalTransactionID() != "" {
		refIntID = sql.NullString{String: t.ReferenceInternalTransactionID(), Valid: true}
	}
	if t.FailureCode() != "" {
		failCode = sql.NullString{String: t.FailureCode(), Valid: true}
	}
	if t.BalanceSnapshot() != nil {
		balSnapshot = sql.NullInt64{Int64: t.BalanceSnapshot().Units(), Valid: true}
	}

	_, err := tx.Exec(ctx, query,
		t.ID(),
		string(t.Origin()),
		providerID,
		extID,
		key,
		hash,
		t.WalletID(),
		t.PlayerID(),
		roundID,
		gameID,
		string(t.Kind()),
		t.Money().Units(),
		t.Money().Currency(),
		refExtID,
		refIntID,
		string(t.Status()),
		failCode,
		balSnapshot,
		t.CreatedAt(),
		t.UpdatedAt(),
	)
	if err != nil {
		return fmt.Errorf("failed to insert transaction %s: %w", t.ID(), err)
	}
	return nil
}

// Update updates transaction state, resolved reference, failure code and balance snapshot.
func (r *TransactionRepository) Update(ctx context.Context, tx pgx.Tx, t *domain.WagerTransaction) error {
	query := `
		UPDATE wager_transactions
		SET status = $1,
		    failure_code = $2,
		    reference_internal_transaction_id = $3,
		    balance_snapshot = $4,
		    updated_at = $5
		WHERE id = $6
	`
	var (
		refIntID, failCode sql.NullString
		balSnapshot        sql.NullInt64
	)
	if t.ReferenceInternalTransactionID() != "" {
		refIntID = sql.NullString{String: t.ReferenceInternalTransactionID(), Valid: true}
	}
	if t.FailureCode() != "" {
		failCode = sql.NullString{String: t.FailureCode(), Valid: true}
	}
	if t.BalanceSnapshot() != nil {
		balSnapshot = sql.NullInt64{Int64: t.BalanceSnapshot().Units(), Valid: true}
	}

	cmdTag, err := tx.Exec(ctx, query,
		string(t.Status()),
		failCode,
		refIntID,
		balSnapshot,
		t.UpdatedAt(),
		t.ID(),
	)
	if err != nil {
		return fmt.Errorf("failed to update transaction %s: %w", t.ID(), err)
	}
	if cmdTag.RowsAffected() == 0 {
		return ErrTransactionNotFound
	}
	return nil
}

// GetByID fetches a transaction by internal ID.
func (r *TransactionRepository) GetByID(ctx context.Context, id string) (*domain.WagerTransaction, error) {
	query := `
		SELECT id, origin, provider_id, external_transaction_id, idempotency_key, payload_hash,
		       wallet_id, player_id, round_id, game_id, kind, amount, currency,
		       reference_external_transaction_id, reference_internal_transaction_id,
		       status, failure_code, balance_snapshot, created_at, updated_at
		FROM wager_transactions
		WHERE id = $1
	`
	return r.scanRow(r.pool.QueryRow(ctx, query, id))
}

// GetByProviderAndExternalID finds an external transaction by provider and external ID.
func (r *TransactionRepository) GetByProviderAndExternalID(ctx context.Context, providerID, externalID string) (*domain.WagerTransaction, error) {
	query := `
		SELECT id, origin, provider_id, external_transaction_id, idempotency_key, payload_hash,
		       wallet_id, player_id, round_id, game_id, kind, amount, currency,
		       reference_external_transaction_id, reference_internal_transaction_id,
		       status, failure_code, balance_snapshot, created_at, updated_at
		FROM wager_transactions
		WHERE provider_id = $1 AND external_transaction_id = $2 AND origin = 'EXTERNAL'
	`
	return r.scanRow(r.pool.QueryRow(ctx, query, providerID, externalID))
}

// GetByProviderAndIdempotencyKey finds an external transaction by provider and idempotency key.
func (r *TransactionRepository) GetByProviderAndIdempotencyKey(ctx context.Context, providerID, key string) (*domain.WagerTransaction, error) {
	query := `
		SELECT id, origin, provider_id, external_transaction_id, idempotency_key, payload_hash,
		       wallet_id, player_id, round_id, game_id, kind, amount, currency,
		       reference_external_transaction_id, reference_internal_transaction_id,
		       status, failure_code, balance_snapshot, created_at, updated_at
		FROM wager_transactions
		WHERE provider_id = $1 AND idempotency_key = $2 AND origin = 'EXTERNAL'
	`
	return r.scanRow(r.pool.QueryRow(ctx, query, providerID, key))
}

// ListPendingReferences returns pending reference transactions ordered chronologically.
func (r *TransactionRepository) ListPendingReferences(ctx context.Context, limit int) ([]*domain.WagerTransaction, error) {
	query := `
		SELECT id, origin, provider_id, external_transaction_id, idempotency_key, payload_hash,
		       wallet_id, player_id, round_id, game_id, kind, amount, currency,
		       reference_external_transaction_id, reference_internal_transaction_id,
		       status, failure_code, balance_snapshot, created_at, updated_at
		FROM wager_transactions
		WHERE status = 'PENDING_REFERENCE'
		ORDER BY created_at ASC
		LIMIT $1
	`
	rows, err := r.pool.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query pending references: %w", err)
	}
	defer rows.Close()

	var result []*domain.WagerTransaction
	for rows.Next() {
		tx, err := r.scanRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, tx)
	}
	return result, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func (r *TransactionRepository) scanRow(row rowScanner) (*domain.WagerTransaction, error) {
	var (
		id, origin, kindStr, statusStr, currency string
		amountUnits                             int64
		providerID, extID, key, hash, roundID, gameID, refExtID, refIntID, failCode sql.NullString
		balSnapshot                             sql.NullInt64
		walletID, playerID                      string
		createdAt, updatedAt                    time.Time
	)

	err := row.Scan(
		&id, &origin, &providerID, &extID, &key, &hash,
		&walletID, &playerID, &roundID, &gameID, &kindStr, &amountUnits, &currency,
		&refExtID, &refIntID,
		&statusStr, &failCode, &balSnapshot, &createdAt, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTransactionNotFound
		}
		return nil, err
	}

	money, err := domain.NewMoney(amountUnits, currency)
	if err != nil {
		return nil, err
	}

	var snapshot *domain.Money
	if balSnapshot.Valid {
		snapMoney, err := domain.NewMoney(balSnapshot.Int64, currency)
		if err != nil {
			return nil, err
		}
		snapshot = &snapMoney
	}

	return domain.RehydrateTransaction(
		id,
		domain.TransactionOrigin(origin),
		providerID.String,
		extID.String,
		key.String,
		hash.String,
		walletID,
		playerID,
		roundID.String,
		gameID.String,
		domain.TransactionKind(kindStr),
		money,
		refExtID.String,
		refIntID.String,
		domain.TransactionStatus(statusStr),
		failCode.String,
		snapshot,
		createdAt,
		updatedAt,
	)
}

package repository

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type LedgerRepository struct {
	pool *pgxpool.Pool
}

func NewLedgerRepository(pool *pgxpool.Pool) *LedgerRepository {
	return &LedgerRepository{pool: pool}
}

// Create inserts an immutable ledger entry.
func (r *LedgerRepository) Create(ctx context.Context, tx pgx.Tx, entry *domain.WalletLedgerEntry) error {
	query := `
		INSERT INTO wallet_ledger_entries (
			id, wallet_id, transaction_id, direction, amount, currency,
			balance_before, balance_after, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	_, err := tx.Exec(ctx, query,
		entry.ID(),
		entry.WalletID(),
		entry.TransactionID(),
		string(entry.Direction()),
		entry.Amount().Units(),
		entry.Amount().Currency(),
		entry.BalanceBefore().Units(),
		entry.BalanceAfter().Units(),
		entry.CreatedAt(),
	)
	if err != nil {
		return fmt.Errorf("failed to insert ledger entry: %w", err)
	}
	return nil
}

// ListByWallet returns ledger entries with opaque cursor pagination and stable sorting.
func (r *LedgerRepository) ListByWallet(ctx context.Context, walletID string, cursor string, limit int) ([]*domain.WalletLedgerEntry, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	var (
		cursorTime time.Time
		cursorID   string
		hasCursor  bool
		err        error
	)

	if cursor != "" {
		cursorTime, cursorID, err = decodeCursor(cursor)
		if err != nil {
			return nil, "", fmt.Errorf("invalid cursor: %w", err)
		}
		hasCursor = true
	}

	var (
		rows pgx.Rows
	)

	if hasCursor {
		query := `
			SELECT id, wallet_id, transaction_id, direction, amount, currency,
			       balance_before, balance_after, created_at
			FROM wallet_ledger_entries
			WHERE wallet_id = $1 AND (created_at, id) > ($2, $3)
			ORDER BY created_at ASC, id ASC
			LIMIT $4
		`
		rows, err = r.pool.Query(ctx, query, walletID, cursorTime, cursorID, limit+1)
	} else {
		query := `
			SELECT id, wallet_id, transaction_id, direction, amount, currency,
			       balance_before, balance_after, created_at
			FROM wallet_ledger_entries
			WHERE wallet_id = $1
			ORDER BY created_at ASC, id ASC
			LIMIT $2
		`
		rows, err = r.pool.Query(ctx, query, walletID, limit+1)
	}

	if err != nil {
		return nil, "", fmt.Errorf("failed to query ledger entries: %w", err)
	}
	defer rows.Close()

	var entries []*domain.WalletLedgerEntry
	for rows.Next() {
		var (
			id, wID, txID, dirStr, currency string
			amountUnits, beforeUnits, afterUnits int64
			createdAt time.Time
		)

		if err := rows.Scan(
			&id, &wID, &txID, &dirStr, &amountUnits, &currency,
			&beforeUnits, &afterUnits, &createdAt,
		); err != nil {
			return nil, "", fmt.Errorf("failed to scan ledger row: %w", err)
		}

		amt, _ := domain.NewMoney(amountUnits, currency)
		before, _ := domain.NewMoney(beforeUnits, currency)
		after, _ := domain.NewMoney(afterUnits, currency)

		entry := domain.RehydrateLedgerEntry(
			id, wID, txID, domain.LedgerDirection(dirStr), amt, before, after, createdAt,
		)
		entries = append(entries, entry)
	}

	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var nextCursor string
	if len(entries) > limit {
		// We have a next page
		entries = entries[:limit]
		last := entries[len(entries)-1]
		nextCursor = encodeCursor(last.CreatedAt(), last.ID())
	}

	return entries, nextCursor, nil
}

// GetBalanceSumByWallet calculates sum of credits, debits and count of entries for reconciliation.
func (r *LedgerRepository) GetBalanceSumByWallet(ctx context.Context, tx pgx.Tx, walletID string) (totalCredits int64, totalDebits int64, count int, err error) {
	query := `
		SELECT
			COALESCE(SUM(CASE WHEN direction = 'CREDIT' THEN amount ELSE 0 END), 0) AS total_credits,
			COALESCE(SUM(CASE WHEN direction = 'DEBIT' THEN amount ELSE 0 END), 0) AS total_debits,
			COUNT(*) AS entry_count
		FROM wallet_ledger_entries
		WHERE wallet_id = $1
	`
	var runner pgx.Row
	if tx != nil {
		runner = tx.QueryRow(ctx, query, walletID)
	} else {
		runner = r.pool.QueryRow(ctx, query, walletID)
	}

	err = runner.Scan(&totalCredits, &totalDebits, &count)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to calculate ledger balance sum: %w", err)
	}
	return totalCredits, totalDebits, count, nil
}

func encodeCursor(t time.Time, id string) string {
	raw := fmt.Sprintf("%s|%s", t.UTC().Format(time.RFC3339Nano), id)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(cursor string) (time.Time, string, error) {
	data, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", err
	}
	parts := strings.Split(string(data), "|")
	if len(parts) != 2 {
		return time.Time{}, "", fmt.Errorf("malformed cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", err
	}
	return t, parts[1], nil
}

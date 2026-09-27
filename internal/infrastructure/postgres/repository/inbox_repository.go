package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type InboxRepository struct {
	pool *pgxpool.Pool
}

func NewInboxRepository(pool *pgxpool.Pool) *InboxRepository {
	return &InboxRepository{pool: pool}
}

// RecordOrGet inserts an inbox message if it does not exist, or returns the existing status.
// Uses ON CONFLICT DO NOTHING and SELECT FOR UPDATE within the same transaction.
func (r *InboxRepository) RecordOrGet(ctx context.Context, tx pgx.Tx, consumerName, messageID, messageHash string) (status string, alreadyExists bool, err error) {
	insertQuery := `
		INSERT INTO inbox_messages (consumer_name, message_id, message_hash, status, received_at)
		VALUES ($1, $2, $3, 'PROCESSING', NOW())
		ON CONFLICT (consumer_name, message_id) DO NOTHING
	`
	cmdTag, err := tx.Exec(ctx, insertQuery, consumerName, messageID, messageHash)
	if err != nil {
		return "", false, fmt.Errorf("failed to insert inbox message: %w", err)
	}

	if cmdTag.RowsAffected() == 1 {
		// New message, successfully recorded as PROCESSING
		return "PROCESSING", false, nil
	}

	// Message already existed, fetch current status with lock
	selectQuery := `
		SELECT status
		FROM inbox_messages
		WHERE consumer_name = $1 AND message_id = $2
		FOR UPDATE
	`
	err = tx.QueryRow(ctx, selectQuery, consumerName, messageID).Scan(&status)
	if err != nil {
		return "", true, fmt.Errorf("failed to get existing inbox message: %w", err)
	}

	return status, true, nil
}

// MarkCompleted marks an inbox message as PROCESSED with a completion timestamp.
func (r *InboxRepository) MarkCompleted(ctx context.Context, tx pgx.Tx, consumerName, messageID string) error {
	query := `
		UPDATE inbox_messages
		SET status = 'PROCESSED', completed_at = $1
		WHERE consumer_name = $2 AND message_id = $3
	`
	cmdTag, err := tx.Exec(ctx, query, time.Now().UTC(), consumerName, messageID)
	if err != nil {
		return fmt.Errorf("failed to mark inbox message as completed: %w", err)
	}
	if cmdTag.RowsAffected() == 0 {
		return errors.New("inbox message not found to mark completed")
	}
	return nil
}

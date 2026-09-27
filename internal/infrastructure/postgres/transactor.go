package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Transactor defines an interface for managing atomic database transactions.
type Transactor interface {
	WithinTransaction(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error
}

type pgxTransactor struct {
	pool *pgxpool.Pool
}

// NewTransactor creates a Transactor backed by pgxpool.Pool.
func NewTransactor(pool *pgxpool.Pool) Transactor {
	return &pgxTransactor{pool: pool}
}

// WithinTransaction executes a closure inside a database transaction.
// If fn returns an error or panics, the transaction is automatically rolled back.
// If fn succeeds, the transaction is committed.
func (t *pgxTransactor) WithinTransaction(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) (err error) {
	tx, err := t.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p) // re-throw panic after rollback
		} else if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()

	err = fn(ctx, tx)
	if err != nil {
		return err
	}

	if commitErr := tx.Commit(ctx); commitErr != nil {
		return fmt.Errorf("failed to commit transaction: %w", commitErr)
	}

	return nil
}

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type OutboxRecord struct {
	ID            string
	EventType     string
	AggregateType string
	AggregateID   string
	CorrelationID string
	CausationID   string
	Payload       []byte
	OccurredAt    time.Time
	Version       int
	Attempts      int
	NextRetryAt   time.Time
	PublishedAt   *time.Time
	LastError     string
}

type OutboxRepository struct {
	pool *pgxpool.Pool
}

func NewOutboxRepository(pool *pgxpool.Pool) *OutboxRepository {
	return &OutboxRepository{pool: pool}
}

// Create inserts a domain event envelope into the transactional outbox table.
func (r *OutboxRepository) Create(ctx context.Context, tx pgx.Tx, event *domain.EventEnvelope, aggregateType string) error {
	query := `
		INSERT INTO outbox_events (
			id, event_type, aggregate_type, aggregate_id, correlation_id, causation_id,
			payload, occurred_at, version, attempts, next_retry_at, published_at, last_error
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 0, NOW(), NULL, NULL)
	`
	occurredAt, err := time.Parse(time.RFC3339Nano, event.OccurredAt)
	if err != nil {
		occurredAt = time.Now().UTC()
	}

	payloadBytes, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event envelope: %w", err)
	}

	_, err = tx.Exec(ctx, query,
		event.EventID,
		string(event.EventType),
		aggregateType,
		event.AggregateID,
		event.CorrelationID,
		event.CausationID,
		payloadBytes,
		occurredAt,
		event.Version,
	)
	if err != nil {
		return fmt.Errorf("failed to insert outbox event %s: %w", event.EventID, err)
	}
	return nil
}

// FetchPending retrieves unpublished events using FOR UPDATE SKIP LOCKED.
// This allows multiple publisher worker instances to consume the outbox in parallel without contention.
func (r *OutboxRepository) FetchPending(ctx context.Context, tx pgx.Tx, limit int) ([]*OutboxRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `
		SELECT id, event_type, aggregate_type, aggregate_id, correlation_id, causation_id,
		       payload, occurred_at, version, attempts, next_retry_at, published_at, COALESCE(last_error, '')
		FROM outbox_events
		WHERE published_at IS NULL AND next_retry_at <= NOW()
		ORDER BY occurred_at ASC
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`
	var (
		rows pgx.Rows
		err  error
	)
	if tx != nil {
		rows, err = tx.Query(ctx, query, limit)
	} else {
		nonLockingQuery := `
			SELECT id, event_type, aggregate_type, aggregate_id, correlation_id, causation_id,
			       payload, occurred_at, version, attempts, next_retry_at, published_at, COALESCE(last_error, '')
			FROM outbox_events
			WHERE published_at IS NULL AND next_retry_at <= NOW()
			ORDER BY occurred_at ASC
			LIMIT $1
		`
		rows, err = r.pool.Query(ctx, nonLockingQuery, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to fetch pending outbox events: %w", err)
	}
	defer rows.Close()

	var records []*OutboxRecord
	for rows.Next() {
		var rec OutboxRecord
		var causationID *string
		if err := rows.Scan(
			&rec.ID,
			&rec.EventType,
			&rec.AggregateType,
			&rec.AggregateID,
			&rec.CorrelationID,
			&causationID,
			&rec.Payload,
			&rec.OccurredAt,
			&rec.Version,
			&rec.Attempts,
			&rec.NextRetryAt,
			&rec.PublishedAt,
			&rec.LastError,
		); err != nil {
			return nil, fmt.Errorf("failed to scan outbox record: %w", err)
		}
		if causationID != nil {
			rec.CausationID = *causationID
		}
		records = append(records, &rec)
	}

	return records, rows.Err()
}

// MarkPublished marks an outbox event as successfully dispatched.
func (r *OutboxRepository) MarkPublished(ctx context.Context, tx pgx.Tx, id string) error {
	query := `
		UPDATE outbox_events
		SET published_at = NOW(), last_error = NULL
		WHERE id = $1
	`
	_, err := tx.Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("failed to mark outbox event %s published: %w", id, err)
	}
	return nil
}

// ScheduleRetry increments attempts and sets an exponential backoff retry timestamp.
func (r *OutboxRepository) ScheduleRetry(ctx context.Context, tx pgx.Tx, id string, lastError string, nextRetryAt time.Time) error {
	query := `
		UPDATE outbox_events
		SET attempts = attempts + 1,
		    last_error = $1,
		    next_retry_at = $2
		WHERE id = $3
	`
	_, err := tx.Exec(ctx, query, lastError, nextRetryAt.UTC(), id)
	if err != nil {
		return fmt.Errorf("failed to schedule outbox event %s retry: %w", id, err)
	}
	return nil
}

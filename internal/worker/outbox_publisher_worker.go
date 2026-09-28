package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/postgres"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/postgres/repository"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/sqs"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/observability"
	"github.com/jackc/pgx/v5"
)

type OutboxPublisherWorker struct {
	transactor   postgres.Transactor
	outboxRepo   *repository.OutboxRepository
	publisher    sqs.EventPublisher
	pollInterval time.Duration
	batchSize    int
	logger       *slog.Logger
	metrics      *observability.Metrics
	stopChan     chan struct{}
	wg           sync.WaitGroup
}

func NewOutboxPublisherWorker(
	transactor postgres.Transactor,
	outboxRepo *repository.OutboxRepository,
	publisher sqs.EventPublisher,
	pollInterval time.Duration,
	batchSize int,
	logger *slog.Logger,
	metrics *observability.Metrics,
) *OutboxPublisherWorker {
	if pollInterval <= 0 {
		pollInterval = 500 * time.Millisecond
	}
	if batchSize <= 0 {
		batchSize = 50
	}
	return &OutboxPublisherWorker{
		transactor:   transactor,
		outboxRepo:   outboxRepo,
		publisher:    publisher,
		pollInterval: pollInterval,
		batchSize:    batchSize,
		logger:       logger,
		metrics:      metrics,
		stopChan:     make(chan struct{}),
	}
}

// Start begins background polling and publishing of outbox events.
func (w *OutboxPublisherWorker) Start(ctx context.Context) {
	w.wg.Add(1)
	go w.runLoop(ctx)
}

// Stop cleanly terminates the worker waiting for in-flight dispatches to finish.
func (w *OutboxPublisherWorker) Stop(ctx context.Context) error {
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

func (w *OutboxPublisherWorker) runLoop(ctx context.Context) {
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
			// Drain all batches immediately until fewer than batchSize items remain
			for {
				count := w.publishBatch(ctx)
				if count < w.batchSize || ctx.Err() != nil {
					break
				}
			}
		}
	}
}

// publishBatch locks pending events via FOR UPDATE SKIP LOCKED and dispatches them to SQS.
// Returns the number of events processed in this batch to support continuous draining.
func (w *OutboxPublisherWorker) publishBatch(ctx context.Context) int {
	var count int
	_ = w.transactor.WithinTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		records, err := w.outboxRepo.FetchPending(ctx, tx, w.batchSize)
		if err != nil || len(records) == 0 {
			return err
		}
		count = len(records)

		now := time.Now().UTC()
		for _, rec := range records {
			// Publish event to SQS FIFO queue
			// MessageGroupId = AggregateID (preserves per-aggregate order)
			// MessageDeduplicationId = EventID (ensures idempotency on broker)
			pubErr := w.publisher.Publish(ctx, rec.ID, rec.AggregateID, rec.Payload)
			if pubErr != nil {
				if w.metrics != nil && w.metrics.RetriesTotal != nil {
					w.metrics.RetriesTotal.WithLabelValues("outbox_publisher").Inc()
				}
				if w.logger != nil {
					w.logger.Warn("failed to publish outbox event, scheduling retry",
						slog.String("eventId", rec.ID),
						slog.String("aggregateId", rec.AggregateID),
						slog.Int("attempt", rec.Attempts),
						slog.String("error", pubErr.Error()),
					)
				}

				// Exponential backoff retry using integer bit shift (1, 2, 4, 8, 16, 32, 60s)
				shift := rec.Attempts
				if shift > 6 {
					shift = 6
				}
				backoffSec := 1 << shift
				if backoffSec > 60 {
					backoffSec = 60
				}
				nextRetry := now.Add(time.Duration(backoffSec) * time.Second)
				_ = w.outboxRepo.ScheduleRetry(ctx, tx, rec.ID, pubErr.Error(), nextRetry)
				continue
			}

			// Mark successfully published
			_ = w.outboxRepo.MarkPublished(ctx, tx, rec.ID)

			if w.metrics != nil {
				if w.metrics.OutboxPublishedTotal != nil {
					w.metrics.OutboxPublishedTotal.Inc()
				}
				if w.metrics.OutboxLagSeconds != nil {
					lag := time.Since(rec.OccurredAt).Seconds()
					w.metrics.OutboxLagSeconds.Set(lag)
				}
			}

			if w.logger != nil {
				w.logger.Debug("outbox event published successfully",
					slog.String("eventId", rec.ID),
					slog.String("aggregateId", rec.AggregateID),
					slog.String("eventType", rec.EventType),
				)
			}
		}

		return nil
	})
	return count
}

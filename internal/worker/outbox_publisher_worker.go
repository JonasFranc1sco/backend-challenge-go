package worker

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/postgres"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/postgres/repository"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/sqs"
	"github.com/jackc/pgx/v5"
)

type OutboxPublisherWorker struct {
	transactor   postgres.Transactor
	outboxRepo   *repository.OutboxRepository
	publisher    sqs.EventPublisher
	pollInterval time.Duration
	batchSize    int
	stopChan     chan struct{}
	wg           sync.WaitGroup
}

func NewOutboxPublisherWorker(
	transactor postgres.Transactor,
	outboxRepo *repository.OutboxRepository,
	publisher sqs.EventPublisher,
	pollInterval time.Duration,
	batchSize int,
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
			w.publishBatch(ctx)
		}
	}
}

// publishBatch locks pending events via FOR UPDATE SKIP LOCKED and dispatches them to SQS.
func (w *OutboxPublisherWorker) publishBatch(ctx context.Context) {
	_ = w.transactor.WithinTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		records, err := w.outboxRepo.FetchPending(ctx, tx, w.batchSize)
		if err != nil || len(records) == 0 {
			return err
		}

		now := time.Now().UTC()
		for _, rec := range records {
			// Publish event to SQS FIFO queue
			// MessageGroupId = AggregateID (preserves per-aggregate order)
			// MessageDeduplicationId = EventID (ensures idempotency on broker)
			pubErr := w.publisher.Publish(ctx, rec.ID, rec.AggregateID, rec.Payload)
			if pubErr != nil {
				// Exponential backoff retry
				backoff := math.Pow(2, float64(rec.Attempts))
				if backoff > 60 {
					backoff = 60
				}
				nextRetry := now.Add(time.Duration(backoff) * time.Second)
				_ = w.outboxRepo.ScheduleRetry(ctx, tx, rec.ID, pubErr.Error(), nextRetry)
				continue
			}

			// Mark successfully published
			_ = w.outboxRepo.MarkPublished(ctx, tx, rec.ID)
		}

		return nil
	})
}

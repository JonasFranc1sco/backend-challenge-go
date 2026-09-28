package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/domain"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/postgres"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/postgres/repository"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/observability"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/usecase"
	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	awstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
)

type SQSMessageEnvelope struct {
	MessageID  string         `json:"messageId"`
	Type       string         `json:"type"`
	OccurredAt string         `json:"occurredAt"`
	Data       SQSWagerData   `json:"data"`
}

type SQSWagerData struct {
	ProviderID                     string       `json:"providerId"`
	ExternalTransactionID          string       `json:"externalTransactionId"`
	IdempotencyKey                 string       `json:"idempotencyKey"`
	PlayerID                       string       `json:"playerId"`
	WalletID                       string       `json:"walletId"`
	RoundID                        string       `json:"roundId"`
	GameID                         string       `json:"gameId"`
	Kind                           string       `json:"kind"`
	Money                          SQSMoneyData `json:"money"`
	ReferenceExternalTransactionID string       `json:"referenceExternalTransactionId,omitempty"`
}

type SQSMoneyData struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// SQSConsumerWorker reads messages from AWS SQS FIFO queue with Transactional Inbox deduplication.
type SQSConsumerWorker struct {
	sqsClient      *awssqs.Client
	queueURL       string
	transactor     postgres.Transactor
	inboxRepo      *repository.InboxRepository
	processWagerUC *usecase.ProcessWagerTransactionUseCase
	consumerName   string
	maxMessages    int32
	waitTime       int32
	logger         *slog.Logger
	metrics        *observability.Metrics
	stopChan       chan struct{}
	wg             sync.WaitGroup
}

func NewSQSConsumerWorker(
	sqsClient *awssqs.Client,
	queueURL string,
	transactor postgres.Transactor,
	inboxRepo *repository.InboxRepository,
	processWagerUC *usecase.ProcessWagerTransactionUseCase,
	consumerName string,
	maxMessages int32,
	waitTime int32,
	logger *slog.Logger,
	metrics *observability.Metrics,
) *SQSConsumerWorker {
	if consumerName == "" {
		consumerName = "wager-transactions-consumer"
	}
	if maxMessages <= 0 || maxMessages > 10 {
		maxMessages = 10
	}
	if waitTime <= 0 {
		waitTime = 10
	}
	return &SQSConsumerWorker{
		sqsClient:      sqsClient,
		queueURL:       queueURL,
		transactor:     transactor,
		inboxRepo:      inboxRepo,
		processWagerUC: processWagerUC,
		consumerName:   consumerName,
		maxMessages:    maxMessages,
		waitTime:       waitTime,
		logger:         logger,
		metrics:        metrics,
		stopChan:       make(chan struct{}),
	}
}

// Start launches the background polling consumer loop.
func (w *SQSConsumerWorker) Start(ctx context.Context) {
	w.wg.Add(1)
	go w.runLoop(ctx)
}

// Stop stops polling and waits for in-flight message processing to finish.
func (w *SQSConsumerWorker) Stop(ctx context.Context) error {
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

func (w *SQSConsumerWorker) runLoop(ctx context.Context) {
	defer w.wg.Done()

	for {
		select {
		case <-w.stopChan:
			return
		case <-ctx.Done():
			return
		default:
			w.pollAndProcess(ctx)
		}
	}
}

func (w *SQSConsumerWorker) pollAndProcess(ctx context.Context) {
	input := &awssqs.ReceiveMessageInput{
		QueueUrl:            aws.String(w.queueURL),
		MaxNumberOfMessages: w.maxMessages,
		WaitTimeSeconds:     w.waitTime,
		VisibilityTimeout:   30,
	}

	result, err := w.sqsClient.ReceiveMessage(ctx, input)
	if err != nil {
		if w.logger != nil {
			w.logger.Warn("error polling SQS queue", slog.String("error", err.Error()))
		}
		select {
		case <-time.After(1 * time.Second):
		case <-ctx.Done():
		}
		return
	}

	for _, msg := range result.Messages {
		select {
		case <-w.stopChan:
			return
		case <-ctx.Done():
			return
		default:
			w.processSingleMessage(ctx, msg)
		}
	}
}

func (w *SQSConsumerWorker) processSingleMessage(ctx context.Context, msg awstypes.Message) {
	if msg.Body == nil {
		return
	}

	bodyStr := *msg.Body
	hash := sha256.Sum256([]byte(bodyStr))
	messageHash := hex.EncodeToString(hash[:])

	var envelope SQSMessageEnvelope
	if err := json.Unmarshal([]byte(bodyStr), &envelope); err != nil {
		// Poison message: permanently unprocessable syntax error
		if w.metrics != nil && w.metrics.DLQMessagesTotal != nil {
			w.metrics.DLQMessagesTotal.WithLabelValues("unparseable_json").Inc()
		}
		if w.logger != nil {
			w.logger.Error("poison message unprocessable JSON", slog.String("messageId", *msg.MessageId))
		}
		return
	}

	if envelope.MessageID == "" && msg.MessageId != nil {
		envelope.MessageID = *msg.MessageId
	}

	// Transactional Inbox: Commit domain changes + ledger + outbox + inbox in a single SQL transaction
	commitErr := w.transactor.WithinTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		status, alreadyExists, err := w.inboxRepo.RecordOrGet(ctx, tx, w.consumerName, envelope.MessageID, messageHash)
		if err != nil {
			return fmt.Errorf("inbox error: %w", err)
		}

		if alreadyExists && status == "PROCESSED" {
			// Already successfully treated in previous commit (at-least-once redelivery). Safe to delete from SQS.
			if w.metrics != nil && w.metrics.IdempotentReplaysTotal != nil {
				w.metrics.IdempotentReplaysTotal.WithLabelValues(envelope.Data.ProviderID).Inc()
			}
			return nil
		}

		// Parse money
		var money domain.Money
		kind := domain.TransactionKind(strings.ToUpper(strings.TrimSpace(envelope.Data.Kind)))
		if kind == domain.KindLoss {
			money, err = domain.NewNonNegativeMoneyFromDecimal(envelope.Data.Money.Amount, envelope.Data.Money.Currency)
			if err != nil || !money.IsZero() {
				// Invalid loss amount -> default to 0.00
				money, _ = domain.MoneyZero(envelope.Data.Money.Currency)
			}
		} else {
			money, err = domain.NewPositiveMoneyFromDecimal(envelope.Data.Money.Amount, envelope.Data.Money.Currency)
			if err != nil {
				// Reject immediately and mark completed in inbox
				return w.inboxRepo.MarkCompleted(ctx, tx, w.consumerName, envelope.MessageID)
			}
		}

		if w.metrics != nil && w.metrics.TransactionDuration != nil {
			timer := prometheus.NewTimer(w.metrics.TransactionDuration.WithLabelValues(string(kind)))
			defer timer.ObserveDuration()
		}

		// Execute Wager Transaction sharing the active SQL transaction
		out, execErr := w.processWagerUC.ExecuteWithTx(ctx, tx, usecase.ProcessWagerInput{
			ProviderID:                     envelope.Data.ProviderID,
			ExternalTransactionID:          envelope.Data.ExternalTransactionID,
			IdempotencyKey:                 envelope.Data.IdempotencyKey,
			PlayerID:                       envelope.Data.PlayerID,
			WalletID:                       envelope.Data.WalletID,
			RoundID:                        envelope.Data.RoundID,
			GameID:                         envelope.Data.GameID,
			Kind:                           kind,
			Money:                          money,
			ReferenceExternalTransactionID: envelope.Data.ReferenceExternalTransactionID,
			CorrelationID:                  envelope.MessageID,
		})

		if execErr != nil {
			// If it's a conflict or wallet not found (non-transient), we still mark inbox completed
			if errors.Is(execErr, usecase.ErrIdempotencyKeyConflict) {
				if w.metrics != nil && w.metrics.IdempotencyConflictsTotal != nil {
					w.metrics.IdempotencyConflictsTotal.WithLabelValues("payload_mismatch").Inc()
				}
				return w.inboxRepo.MarkCompleted(ctx, tx, w.consumerName, envelope.MessageID)
			}
			if errors.Is(execErr, usecase.ErrExternalIDAlreadyExists) {
				if w.metrics != nil && w.metrics.IdempotencyConflictsTotal != nil {
					w.metrics.IdempotencyConflictsTotal.WithLabelValues("external_id_conflict").Inc()
				}
				return w.inboxRepo.MarkCompleted(ctx, tx, w.consumerName, envelope.MessageID)
			}
			if errors.Is(execErr, usecase.ErrWalletNotFound) {
				return w.inboxRepo.MarkCompleted(ctx, tx, w.consumerName, envelope.MessageID)
			}
			// Transient infrastructure error: rollback so SQS redrives message
			if w.metrics != nil && w.metrics.RetriesTotal != nil {
				w.metrics.RetriesTotal.WithLabelValues("sqs_consumer").Inc()
			}
			return execErr
		}

		if w.metrics != nil {
			if w.metrics.WagerTransactionsTotal != nil {
				w.metrics.WagerTransactionsTotal.WithLabelValues(string(kind), string(out.Status), envelope.Data.ProviderID).Inc()
			}
			if out.IdempotentReplay && w.metrics.IdempotentReplaysTotal != nil {
				w.metrics.IdempotentReplaysTotal.WithLabelValues(envelope.Data.ProviderID).Inc()
			}
		}

		if w.logger != nil {
			w.logger.Info("sqs wager transaction processed",
				slog.String("messageId", envelope.MessageID),
				slog.String("transactionId", out.TransactionID),
				slog.String("walletId", envelope.Data.WalletID),
				slog.String("providerId", envelope.Data.ProviderID),
				slog.String("status", string(out.Status)),
				slog.Bool("idempotentReplay", out.IdempotentReplay),
			)
		}

		// Mark inbox message completed
		return w.inboxRepo.MarkCompleted(ctx, tx, w.consumerName, envelope.MessageID)
	})

	// Delete message from SQS strictly AFTER SQL transaction commit succeeds
	if commitErr == nil {
		_, _ = w.sqsClient.DeleteMessage(ctx, &awssqs.DeleteMessageInput{
			QueueUrl:      aws.String(w.queueURL),
			ReceiptHandle: msg.ReceiptHandle,
		})
	}
}

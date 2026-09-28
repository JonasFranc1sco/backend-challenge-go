package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/domain"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/postgres"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/postgres/repository"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/usecase"
	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	awstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5"
)

// SQSMessageEnvelope represents the expected SQS message contract.
type SQSMessageEnvelope struct {
	MessageID  string         `json:"messageId"`
	Type       string         `json:"type"`
	OccurredAt string         `json:"occurredAt"`
	Data       SQSMessageData `json:"data"`
}

type SQSMessageData struct {
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
		// Log or brief backoff on connection error
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
		return
	}

	if envelope.MessageID == "" {
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
			return nil
		}

		// Parse money
		var money domain.Money
		kind := domain.TransactionKind(strings.ToUpper(strings.TrimSpace(envelope.Data.Kind)))
		if kind == domain.KindLoss {
			money, err = domain.NewNonNegativeMoneyFromDecimal(envelope.Data.Money.Amount, envelope.Data.Money.Currency)
			if err != nil || !money.IsZero() {
				// Invalid loss amount -> rejection
				money, _ = domain.MoneyZero(envelope.Data.Money.Currency)
			}
		} else {
			money, err = domain.NewPositiveMoneyFromDecimal(envelope.Data.Money.Amount, envelope.Data.Money.Currency)
			if err != nil {
				// Reject immediately
				return nil
			}
		}

		// Execute Wager Transaction sharing the active SQL transaction
		_, execErr := w.processWagerUC.ExecuteWithTx(ctx, tx, usecase.ProcessWagerInput{
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
			if errors.Is(execErr, usecase.ErrIdempotencyKeyConflict) ||
				errors.Is(execErr, usecase.ErrExternalIDAlreadyExists) ||
				errors.Is(execErr, usecase.ErrWalletNotFound) {
				return w.inboxRepo.MarkCompleted(ctx, tx, w.consumerName, envelope.MessageID)
			}
			// Transient infrastructure error: rollback so SQS redrives message
			return execErr
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

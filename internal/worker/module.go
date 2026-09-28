package worker

import (
	"context"
	"log/slog"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/config"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/postgres"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/postgres/repository"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/sqs"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/observability"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/usecase"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.uber.org/fx"
)

func ProvideOutboxPublisherWorker(
	transactor postgres.Transactor,
	outboxRepo *repository.OutboxRepository,
	publisher sqs.EventPublisher,
	cfg *config.Config,
	logger *slog.Logger,
	metrics *observability.Metrics,
) *OutboxPublisherWorker {
	return NewOutboxPublisherWorker(
		transactor,
		outboxRepo,
		publisher,
		cfg.OutboxPollInt,
		cfg.OutboxBatchSize,
		logger,
		metrics,
	)
}

func ProvidePendingReferenceWorker(
	transactor postgres.Transactor,
	walletRepo *repository.WalletRepository,
	txRepo *repository.TransactionRepository,
	ledgerRepo *repository.LedgerRepository,
	outboxRepo *repository.OutboxRepository,
	cfg *config.Config,
	logger *slog.Logger,
	metrics *observability.Metrics,
) *PendingReferenceWorker {
	return NewPendingReferenceWorker(
		transactor,
		walletRepo,
		txRepo,
		ledgerRepo,
		outboxRepo,
		cfg.PendingPollInt,
		cfg.PendingMaxRetry,
		logger,
		metrics,
	)
}

func ProvideSQSConsumerWorker(
	sqsClient *awssqs.Client,
	transactor postgres.Transactor,
	inboxRepo *repository.InboxRepository,
	processWagerUC *usecase.ProcessWagerTransactionUseCase,
	cfg *config.Config,
	logger *slog.Logger,
	metrics *observability.Metrics,
) *SQSConsumerWorker {
	return NewSQSConsumerWorker(
		sqsClient,
		cfg.SQSQueueURL,
		transactor,
		inboxRepo,
		processWagerUC,
		"wager-transactions-consumer",
		cfg.SQSConsumerMax,
		cfg.SQSConsumerWait,
		logger,
		metrics,
	)
}

func RegisterWorkers(
	lc fx.Lifecycle,
	outboxWorker *OutboxPublisherWorker,
	pendingWorker *PendingReferenceWorker,
	sqsWorker *SQSConsumerWorker,
	logger *slog.Logger,
) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			logger.Info("Starting background workers...")
			outboxWorker.Start(ctx)
			pendingWorker.Start(ctx)
			sqsWorker.Start(ctx)
			logger.Info("All background workers started successfully")
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.Info("Stopping background workers gracefully...")
			if err := sqsWorker.Stop(ctx); err != nil {
				logger.Error("Error stopping SQS consumer worker", slog.String("error", err.Error()))
			}
			if err := pendingWorker.Stop(ctx); err != nil {
				logger.Error("Error stopping pending reference worker", slog.String("error", err.Error()))
			}
			if err := outboxWorker.Stop(ctx); err != nil {
				logger.Error("Error stopping outbox publisher worker", slog.String("error", err.Error()))
			}
			logger.Info("All background workers stopped successfully")
			return nil
		},
	})
}

var Module = fx.Module("worker",
	fx.Provide(ProvideOutboxPublisherWorker),
	fx.Provide(ProvidePendingReferenceWorker),
	fx.Provide(ProvideSQSConsumerWorker),
	fx.Invoke(RegisterWorkers),
)

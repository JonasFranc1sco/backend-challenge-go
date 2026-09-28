package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/config"
	deliveryhttp "github.com/JonasFranc1sco/backend-challenge-go/internal/delivery/http"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/domain"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/auth"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/postgres"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/postgres/repository"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/sqs"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/observability"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/usecase"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/worker"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/jackc/pgx/v5/pgxpool"
)

type TestEnv struct {
	Pool           *pgxpool.Pool
	Transactor     postgres.Transactor
	WalletRepo     *repository.WalletRepository
	TxRepo         *repository.TransactionRepository
	LedgerRepo     *repository.LedgerRepository
	InboxRepo      *repository.InboxRepository
	OutboxRepo     *repository.OutboxRepository
	OpenWalletUC   *usecase.OpenWalletUseCase
	ProcessWagerUC *usecase.ProcessWagerTransactionUseCase
	ReconcileUC    *usecase.ReconciliationUseCase
	SQSClient      *awssqs.Client
	JWTValidator   auth.JWTValidator
	Metrics        *observability.Metrics
	Logger         *slog.Logger
	Config         *config.Config
	HTTPHandler    http.Handler
}

func SetupTestEnv(t *testing.T) *TestEnv {
	ctx := context.Background()

	cfg, err := config.LoadConfig()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	metrics := observability.NewMetrics()

	pgCfg := postgres.Config{
		Host:     cfg.DBHost,
		Port:     cfg.DBPort,
		User:     cfg.DBUser,
		Password: cfg.DBPassword,
		Database: cfg.DBName,
		SSLMode:  cfg.DBSSLMode,
		MaxConns: 100,
		MinConns: 5,
	}

	pool, err := postgres.NewPool(ctx, pgCfg)
	if err != nil {
		t.Fatalf("failed to connect to test postgres at %s:%d: %v", cfg.DBHost, cfg.DBPort, err)
	}

	// Apply migrations
	migrator := postgres.NewMigrator(pool, "../../migrations")
	if err := migrator.Up(ctx); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	transactor := postgres.NewTransactor(pool)
	walletRepo := repository.NewWalletRepository(pool)
	txRepo := repository.NewTransactionRepository(pool)
	ledgerRepo := repository.NewLedgerRepository(pool)
	inboxRepo := repository.NewInboxRepository(pool)
	outboxRepo := repository.NewOutboxRepository(pool)

	openWalletUC := usecase.NewOpenWalletUseCase(transactor, walletRepo, txRepo, ledgerRepo, outboxRepo)
	processWagerUC := usecase.NewProcessWagerTransactionUseCase(transactor, walletRepo, txRepo, ledgerRepo, outboxRepo)
	reconcileUC := usecase.NewReconciliationUseCase(walletRepo, ledgerRepo)

	sqsClient, err := sqs.ProvideSQSClient(cfg)
	if err != nil {
		t.Fatalf("failed to init SQS client: %v", err)
	}

	jwksFetcher := auth.NewJWKSKeyFetcher(cfg.KeycloakJWKSURL, 1*time.Hour)
	jwtValidator := auth.NewKeycloakJWTValidator(jwksFetcher, cfg.KeycloakIssuer)

	walletHandler := deliveryhttp.NewWalletHandler(openWalletUC, reconcileUC, walletRepo, ledgerRepo, logger, metrics)
	txHandler := deliveryhttp.NewTransactionHandler(processWagerUC, txRepo, logger, metrics)
	healthHandler := deliveryhttp.NewHealthHandler(pool, sqsClient)

	router := deliveryhttp.NewRouter(deliveryhttp.RouterConfig{
		WalletHandler:      walletHandler,
		TransactionHandler: txHandler,
		HealthHandler:      healthHandler,
		JWTValidator:       jwtValidator,
		Metrics:            metrics,
	})

	return &TestEnv{
		Pool:           pool,
		Transactor:     transactor,
		WalletRepo:     walletRepo,
		TxRepo:         txRepo,
		LedgerRepo:     ledgerRepo,
		InboxRepo:      inboxRepo,
		OutboxRepo:     outboxRepo,
		OpenWalletUC:   openWalletUC,
		ProcessWagerUC: processWagerUC,
		ReconcileUC:    reconcileUC,
		SQSClient:      sqsClient,
		JWTValidator:   jwtValidator,
		Metrics:        metrics,
		Logger:         logger,
		Config:         cfg,
		HTTPHandler:    router,
	}
}

// FetchKeycloakToken requests an OAuth2 token from Keycloak using client credentials grant.
func FetchKeycloakToken(t *testing.T, clientID, clientSecret string) string {
	keycloakBase := os.Getenv("KEYCLOAK_BASE_URL")
	if keycloakBase == "" {
		keycloakBase = "http://localhost:8082"
	}
	tokenURL := fmt.Sprintf("%s/realms/wager-realm/protocol/openid-connect/token", keycloakBase)

	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		t.Fatalf("failed to create token request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("failed to execute token request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("token endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	var res struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("failed to decode token response: %v", err)
	}

	return res.AccessToken
}

// CreateNewInstance creates an independent application instance with its own pool and memory.
func CreateNewInstance(t *testing.T, cfg *config.Config) *TestEnv {
	ctx := context.Background()

	pgCfg := postgres.Config{
		Host:     cfg.DBHost,
		Port:     cfg.DBPort,
		User:     cfg.DBUser,
		Password: cfg.DBPassword,
		Database: cfg.DBName,
		SSLMode:  cfg.DBSSLMode,
		MaxConns: 20,
		MinConns: 2,
	}

	pool, err := postgres.NewPool(ctx, pgCfg)
	if err != nil {
		t.Fatalf("failed to create independent pool: %v", err)
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	metrics := observability.NewMetrics()

	transactor := postgres.NewTransactor(pool)
	walletRepo := repository.NewWalletRepository(pool)
	txRepo := repository.NewTransactionRepository(pool)
	ledgerRepo := repository.NewLedgerRepository(pool)
	inboxRepo := repository.NewInboxRepository(pool)
	outboxRepo := repository.NewOutboxRepository(pool)

	openWalletUC := usecase.NewOpenWalletUseCase(transactor, walletRepo, txRepo, ledgerRepo, outboxRepo)
	processWagerUC := usecase.NewProcessWagerTransactionUseCase(transactor, walletRepo, txRepo, ledgerRepo, outboxRepo)
	reconcileUC := usecase.NewReconciliationUseCase(walletRepo, ledgerRepo)

	sqsClient, err := sqs.ProvideSQSClient(cfg)
	if err != nil {
		t.Fatalf("failed to init SQS client: %v", err)
	}

	jwksFetcher := auth.NewJWKSKeyFetcher(cfg.KeycloakJWKSURL, 1*time.Hour)
	jwtValidator := auth.NewKeycloakJWTValidator(jwksFetcher, cfg.KeycloakIssuer)

	walletHandler := deliveryhttp.NewWalletHandler(openWalletUC, reconcileUC, walletRepo, ledgerRepo, logger, metrics)
	txHandler := deliveryhttp.NewTransactionHandler(processWagerUC, txRepo, logger, metrics)
	healthHandler := deliveryhttp.NewHealthHandler(pool, sqsClient)

	router := deliveryhttp.NewRouter(deliveryhttp.RouterConfig{
		WalletHandler:      walletHandler,
		TransactionHandler: txHandler,
		HealthHandler:      healthHandler,
		JWTValidator:       jwtValidator,
		Metrics:            metrics,
	})

	return &TestEnv{
		Pool:           pool,
		Transactor:     transactor,
		WalletRepo:     walletRepo,
		TxRepo:         txRepo,
		LedgerRepo:     ledgerRepo,
		InboxRepo:      inboxRepo,
		OutboxRepo:     outboxRepo,
		OpenWalletUC:   openWalletUC,
		ProcessWagerUC: processWagerUC,
		ReconcileUC:    reconcileUC,
		SQSClient:      sqsClient,
		JWTValidator:   jwtValidator,
		Metrics:        metrics,
		Logger:         logger,
		Config:         cfg,
		HTTPHandler:    router,
	}
}

// MustNewMoney is a test helper for creating Money or failing immediately.
func MustNewMoney(t *testing.T, amount, currency string) domain.Money {
	t.Helper()
	m, err := domain.NewPositiveMoneyFromDecimal(amount, currency)
	if err != nil {
		t.Fatalf("MustNewMoney error: %v", err)
	}
	return m
}

// MustNewNonNegMoney is a test helper for creating non-negative Money or failing immediately.
func MustNewNonNegMoney(t *testing.T, amount, currency string) domain.Money {
	t.Helper()
	m, err := domain.NewNonNegativeMoneyFromDecimal(amount, currency)
	if err != nil {
		t.Fatalf("MustNewNonNegMoney error: %v", err)
	}
	return m
}

// BuildWorkerSet creates the three background workers for an environment.
func BuildWorkerSet(env *TestEnv) (*worker.OutboxPublisherWorker, *worker.PendingReferenceWorker, *worker.SQSConsumerWorker) {
	pub := sqs.ProvideEventPublisher(env.SQSClient, env.Config)
	outboxWorker := worker.NewOutboxPublisherWorker(
		env.Transactor, env.OutboxRepo, pub, 200*time.Millisecond, 25, env.Logger, env.Metrics,
	)
	pendingWorker := worker.NewPendingReferenceWorker(
		env.Transactor, env.WalletRepo, env.TxRepo, env.LedgerRepo, env.OutboxRepo,
		300*time.Millisecond, 5, env.Logger, env.Metrics,
	)
	sqsWorker := worker.NewSQSConsumerWorker(
		env.SQSClient, env.Config.SQSQueueURL, env.Transactor, env.InboxRepo,
		env.ProcessWagerUC, "integration-consumer", 10, 2, env.Logger, env.Metrics,
	)
	return outboxWorker, pendingWorker, sqsWorker
}

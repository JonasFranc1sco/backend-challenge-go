package config

import (
	"os"
	"strconv"
	"time"

	"go.uber.org/fx"
)

type Config struct {
	AppEnv         string
	HTTPPort       string
	LogLevel       string
	DBHost         string
	DBPort         int
	DBUser         string
	DBPassword     string
	DBName         string
	DBSSLMode      string
	DBMaxConns     int32
	DBMinConns     int32
	AWSRegion      string
	AWSEndpoint    string
	AWSAccessKey   string
	AWSSecretKey   string
	SQSQueueURL    string
	SQSDLQURL      string
	SQSEventsURL   string
	KeycloakBaseURL string
	KeycloakRealm   string
	KeycloakJWKSURL string
	KeycloakIssuer  string
	OutboxPollInt   time.Duration
	OutboxBatchSize int
	PendingPollInt  time.Duration
	PendingMaxRetry int
	SQSConsumerMax  int32
	SQSConsumerWait int32
}

func LoadConfig() (*Config, error) {
	dbPort, _ := strconv.Atoi(getEnv("DB_PORT", "5432"))
	dbMax, _ := strconv.Atoi(getEnv("DB_MAX_CONNS", "50"))
	dbMin, _ := strconv.Atoi(getEnv("DB_MIN_CONNS", "5"))
	outboxBatch, _ := strconv.Atoi(getEnv("OUTBOX_BATCH_SIZE", "50"))
	pendingRetries, _ := strconv.Atoi(getEnv("PENDING_REF_MAX_RETRIES", "5"))
	sqsMax, _ := strconv.Atoi(getEnv("SQS_CONSUMER_MAX_MESSAGES", "10"))
	sqsWait, _ := strconv.Atoi(getEnv("SQS_CONSUMER_WAIT_TIME_SECONDS", "10"))

	outboxPoll, err := time.ParseDuration(getEnv("OUTBOX_POLL_INTERVAL", "500ms"))
	if err != nil {
		outboxPoll = 500 * time.Millisecond
	}

	pendingPoll, err := time.ParseDuration(getEnv("PENDING_REF_POLL_INTERVAL", "1s"))
	if err != nil {
		pendingPoll = 1 * time.Second
	}

	return &Config{
		AppEnv:          getEnv("APP_ENV", "local"),
		HTTPPort:        getEnv("HTTP_PORT", "8080"),
		LogLevel:        getEnv("LOG_LEVEL", "info"),
		DBHost:          getEnv("DB_HOST", "localhost"),
		DBPort:          dbPort,
		DBUser:          getEnv("DB_USER", "postgres"),
		DBPassword:      getEnv("DB_PASSWORD", "postgres"),
		DBName:          getEnv("DB_NAME", "wager_db"),
		DBSSLMode:       getEnv("DB_SSLMODE", "disable"),
		DBMaxConns:      int32(dbMax),
		DBMinConns:      int32(dbMin),
		AWSRegion:       getEnv("AWS_REGION", "us-east-1"),
		AWSEndpoint:      getEnv("AWS_ENDPOINT", "http://localhost:4566"),
		AWSAccessKey:    getEnv("AWS_ACCESS_KEY_ID", "test"),
		AWSSecretKey:    getEnv("AWS_SECRET_ACCESS_KEY", "test"),
		SQSQueueURL:     getEnv("SQS_WAGER_QUEUE_URL", "http://localhost:4566/000000000000/wager-transactions.fifo"),
		SQSDLQURL:       getEnv("SQS_WAGER_DLQ_URL", "http://localhost:4566/000000000000/wager-transactions-dlq.fifo"),
		SQSEventsURL:    getEnv("SQS_EVENTS_QUEUE_URL", "http://localhost:4566/000000000000/wager-events.fifo"),
		KeycloakBaseURL: getEnv("KEYCLOAK_BASE_URL", "http://localhost:8082"),
		KeycloakRealm:   getEnv("KEYCLOAK_REALM", "wager-realm"),
		KeycloakJWKSURL: getEnv("KEYCLOAK_JWKS_URL", "http://localhost:8082/realms/wager-realm/protocol/openid-connect/certs"),
		KeycloakIssuer:  getEnv("KEYCLOAK_ISSUER", "http://localhost:8082/realms/wager-realm"),
		OutboxPollInt:   outboxPoll,
		OutboxBatchSize: outboxBatch,
		PendingPollInt:  pendingPoll,
		PendingMaxRetry: pendingRetries,
		SQSConsumerMax:  int32(sqsMax),
		SQSConsumerWait: int32(sqsWait),
	}, nil
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

var Module = fx.Module("config",
	fx.Provide(LoadConfig),
)

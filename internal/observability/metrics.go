package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.uber.org/fx"
)

type Metrics struct {
	Registry                         *prometheus.Registry
	WagerTransactionsTotal           *prometheus.CounterVec
	IdempotentReplaysTotal           *prometheus.CounterVec
	IdempotencyConflictsTotal        *prometheus.CounterVec
	ReconciliationDiscrepanciesTotal prometheus.Counter
	OutboxPublishedTotal             prometheus.Counter
	OutboxLagSeconds                 prometheus.Gauge
	TransactionDuration              *prometheus.HistogramVec
	RetriesTotal                     *prometheus.CounterVec
	DLQMessagesTotal                 *prometheus.CounterVec
	ConcurrencyConflictsTotal        prometheus.Counter
}

func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()

	factory := promauto.With(reg)

	m := &Metrics{
		Registry: reg,
		WagerTransactionsTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "wager_transactions_total",
				Help: "Total number of processed wagering transactions partitioned by kind, status, and provider",
			},
			[]string{"kind", "status", "provider"},
		),
		IdempotentReplaysTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "idempotent_replays_total",
				Help: "Total number of idempotent replays served without re-execution",
			},
			[]string{"provider"},
		),
		IdempotencyConflictsTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "idempotency_conflicts_total",
				Help: "Total number of detected idempotency payload or key conflicts",
			},
			[]string{"reason"},
		),
		ReconciliationDiscrepanciesTotal: factory.NewCounter(
			prometheus.CounterOpts{
				Name: "reconciliation_discrepancies_total",
				Help: "Total number of detected financial discrepancies between wallet balance and ledger entries",
			},
		),
		OutboxPublishedTotal: factory.NewCounter(
			prometheus.CounterOpts{
				Name: "outbox_published_total",
				Help: "Total number of domain events published by the outbox publisher",
			},
		),
		OutboxLagSeconds: factory.NewGauge(
			prometheus.GaugeOpts{
				Name: "outbox_lag_seconds",
				Help: "Current time lag between event occurrence and outbox publication in seconds",
			},
		),
		TransactionDuration: factory.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "wager_transaction_duration_seconds",
				Help:    "Latency of transaction processing in seconds",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"kind"},
		),
		RetriesTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "retries_total",
				Help: "Total number of retries executed by background workers",
			},
			[]string{"worker"},
		),
		DLQMessagesTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "dlq_messages_total",
				Help: "Total number of messages sent or forwarded to the Dead Letter Queue",
			},
			[]string{"reason"},
		),
		ConcurrencyConflictsTotal: factory.NewCounter(
			prometheus.CounterOpts{
				Name: "concurrency_conflicts_total",
				Help: "Total number of concurrency conflicts encountered during wallet lock acquisition",
			},
		),
	}

	return m
}

var Module = fx.Module("observability",
	fx.Provide(NewLogger),
	fx.Provide(NewMetrics),
)

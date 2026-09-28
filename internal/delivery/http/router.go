package http

import (
	"net/http"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/auth"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/observability"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// RouterConfig holds handler and middleware dependencies for constructing the HTTP mux.
type RouterConfig struct {
	WalletHandler      *WalletHandler
	TransactionHandler *TransactionHandler
	HealthHandler      *HealthHandler
	JWTValidator       auth.JWTValidator
	Metrics            *observability.Metrics
}

// NewRouter constructs the HTTP ServeMux with authentication, role enforcement, and tenancy protection.
func NewRouter(cfg RouterConfig) http.Handler {
	mux := http.NewServeMux()

	// 1. Public Health & Metrics Endpoints
	mux.HandleFunc("GET /health/live", cfg.HealthHandler.Live)
	mux.HandleFunc("GET /health/ready", cfg.HealthHandler.Ready)

	if cfg.Metrics != nil && cfg.Metrics.Registry != nil {
		mux.Handle("GET /metrics", promhttp.HandlerFor(cfg.Metrics.Registry, promhttp.HandlerOpts{}))
	} else {
		mux.Handle("GET /metrics", promhttp.Handler())
	}

	// Auth middlewares
	authenticate := auth.Authenticate(cfg.JWTValidator)
	requireInternal := auth.RequireInternal()

	// 2. Internal Service Endpoints (Require 'internal' role)
	mux.Handle("POST /wallets", authenticate(requireInternal(http.HandlerFunc(cfg.WalletHandler.OpenWallet))))
	mux.Handle("GET /wallets/{walletId}", authenticate(requireInternal(http.HandlerFunc(cfg.WalletHandler.GetWallet))))
	mux.Handle("GET /wallets/{walletId}/ledger", authenticate(requireInternal(http.HandlerFunc(cfg.WalletHandler.GetLedger))))
	mux.Handle("POST /wallets/{walletId}/reconciliation", authenticate(requireInternal(http.HandlerFunc(cfg.WalletHandler.Reconcile))))

	// 3. Provider & Wagering Endpoints (Authenticated with Provider Tenancy checks inside handlers)
	mux.Handle("POST /wagering/transactions", authenticate(http.HandlerFunc(cfg.TransactionHandler.ProcessTransaction)))
	mux.Handle("GET /wagering/transactions/{transactionId}", authenticate(http.HandlerFunc(cfg.TransactionHandler.GetTransaction)))
	mux.Handle("GET /providers/{providerId}/wagering/transactions/{externalTransactionId}", authenticate(http.HandlerFunc(cfg.TransactionHandler.GetProviderTransaction)))

	return mux
}

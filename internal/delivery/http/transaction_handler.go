package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/domain"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/auth"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/postgres/repository"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/observability"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/usecase"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
)

type TransactionHandler struct {
	processWagerUC *usecase.ProcessWagerTransactionUseCase
	txRepo         *repository.TransactionRepository
	logger         *slog.Logger
	metrics        *observability.Metrics
}

func NewTransactionHandler(
	processWagerUC *usecase.ProcessWagerTransactionUseCase,
	txRepo *repository.TransactionRepository,
	logger *slog.Logger,
	metrics *observability.Metrics,
) *TransactionHandler {
	return &TransactionHandler{
		processWagerUC: processWagerUC,
		txRepo:         txRepo,
		logger:         logger,
		metrics:        metrics,
	}
}

// ProcessTransaction handles POST /wagering/transactions
func (h *TransactionHandler) ProcessTransaction(w http.ResponseWriter, r *http.Request) {
	// Idempotency-Key header is strictly mandatory
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "missing_idempotency_key", "Idempotency-Key header is mandatory")
		return
	}

	var req ProcessTransactionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "failed to parse JSON request body")
		return
	}

	// Provider Tenancy Check
	identity, ok := auth.GetAuthIdentity(r.Context())
	if ok && !identity.IsInternal() {
		if identity.ProviderID != strings.TrimSpace(req.ProviderID) {
			writeError(w, http.StatusForbidden, "provider_tenancy_mismatch", "authenticated provider does not match payload providerId")
			return
		}
	}

	kind := domain.TransactionKind(strings.ToUpper(strings.TrimSpace(req.Kind)))
	if !kind.IsValid() {
		writeError(w, http.StatusBadRequest, "invalid_kind", "invalid transaction kind: must be BET, WIN, LOSS, REFUND, or ROLLBACK")
		return
	}
	if kind == domain.KindOpening {
		writeError(w, http.StatusBadRequest, "invalid_kind", "OPENING transactions cannot be submitted externally")
		return
	}

	if h.metrics != nil && h.metrics.TransactionDuration != nil {
		timer := prometheus.NewTimer(h.metrics.TransactionDuration.WithLabelValues(string(kind)))
		defer timer.ObserveDuration()
	}

	// Parse Money depending on kind
	var (
		money domain.Money
		err   error
	)
	if kind == domain.KindLoss {
		money, err = domain.NewNonNegativeMoneyFromDecimal(req.Money.Amount, req.Money.Currency)
		if err != nil || !money.IsZero() {
			writeError(w, http.StatusBadRequest, "invalid_money", "LOSS transaction requires amount to be exactly '0.00'")
			return
		}
	} else {
		money, err = domain.NewPositiveMoneyFromDecimal(req.Money.Amount, req.Money.Currency)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_money", err.Error())
			return
		}
	}

	// Correlation ID
	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	if correlationID == "" {
		correlationID = uuid.NewString()
	}

	out, err := h.processWagerUC.Execute(r.Context(), usecase.ProcessWagerInput{
		ProviderID:                     strings.TrimSpace(req.ProviderID),
		ExternalTransactionID:          strings.TrimSpace(req.ExternalTransactionID),
		IdempotencyKey:                 idempotencyKey,
		PlayerID:                       strings.TrimSpace(req.PlayerID),
		WalletID:                       strings.TrimSpace(req.WalletID),
		RoundID:                        strings.TrimSpace(req.RoundID),
		GameID:                         strings.TrimSpace(req.GameID),
		Kind:                           kind,
		Money:                          money,
		ReferenceExternalTransactionID: strings.TrimSpace(req.ReferenceExternalTransactionID),
		CorrelationID:                  correlationID,
	})

	if err != nil {
		if errors.Is(err, usecase.ErrIdempotencyKeyConflict) {
			if h.metrics != nil && h.metrics.IdempotencyConflictsTotal != nil {
				h.metrics.IdempotencyConflictsTotal.WithLabelValues("payload_mismatch").Inc()
			}
			writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency key reused with different payload")
			return
		}
		if errors.Is(err, usecase.ErrExternalIDAlreadyExists) {
			if h.metrics != nil && h.metrics.IdempotencyConflictsTotal != nil {
				h.metrics.IdempotencyConflictsTotal.WithLabelValues("external_id_conflict").Inc()
			}
			writeError(w, http.StatusConflict, "external_id_conflict", "External transaction ID already registered under a different idempotency key")
			return
		}
		if errors.Is(err, usecase.ErrWalletNotFound) {
			writeError(w, http.StatusNotFound, "wallet_not_found", "wallet not found")
			return
		}
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	if h.metrics != nil {
		if h.metrics.WagerTransactionsTotal != nil {
			h.metrics.WagerTransactionsTotal.WithLabelValues(string(kind), string(out.Status), strings.TrimSpace(req.ProviderID)).Inc()
		}
		if out.IdempotentReplay && h.metrics.IdempotentReplaysTotal != nil {
			h.metrics.IdempotentReplaysTotal.WithLabelValues(strings.TrimSpace(req.ProviderID)).Inc()
		}
	}

	if h.logger != nil {
		h.logger.Info("wager transaction processed",
			slog.String("correlationId", correlationID),
			slog.String("transactionId", out.TransactionID),
			slog.String("walletId", strings.TrimSpace(req.WalletID)),
			slog.String("providerId", strings.TrimSpace(req.ProviderID)),
			slog.String("status", string(out.Status)),
			slog.Bool("idempotentReplay", out.IdempotentReplay),
		)
	}

	resp := TransactionResponse{
		TransactionID:    out.TransactionID,
		Status:           string(out.Status),
		Balance:          out.Balance,
		IdempotentReplay: out.IdempotentReplay,
		FailureCode:      out.FailureCode,
	}

	// Distinguishable HTTP response status codes
	switch out.Status {
	case domain.StatusProcessed:
		writeJSON(w, http.StatusOK, resp)
	case domain.StatusPendingReference:
		writeJSON(w, http.StatusAccepted, resp)
	case domain.StatusRejected:
		writeJSON(w, http.StatusUnprocessableEntity, resp)
	default:
		writeJSON(w, http.StatusOK, resp)
	}
}

// GetTransaction handles GET /wagering/transactions/{transactionId}
func (h *TransactionHandler) GetTransaction(w http.ResponseWriter, r *http.Request) {
	txID := strings.TrimSpace(r.PathValue("transactionId"))
	if txID == "" {
		writeError(w, http.StatusBadRequest, "missing_transaction_id", "transactionId is required")
		return
	}

	tx, err := h.txRepo.GetByID(r.Context(), txID)
	if err != nil {
		if errors.Is(err, repository.ErrTransactionNotFound) {
			writeError(w, http.StatusNotFound, "transaction_not_found", "transaction not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	// Provider Tenancy Check
	identity, ok := auth.GetAuthIdentity(r.Context())
	if ok && !identity.IsInternal() {
		if identity.ProviderID != tx.ProviderID() {
			writeError(w, http.StatusForbidden, "provider_tenancy_mismatch", "access denied to other provider transaction")
			return
		}
	}

	var bal domain.Money
	if tx.BalanceSnapshot() != nil {
		bal = *tx.BalanceSnapshot()
	} else {
		bal, _ = domain.MoneyZero(tx.Money().Currency())
	}

	writeJSON(w, http.StatusOK, TransactionResponse{
		TransactionID: tx.ID(),
		Status:        string(tx.Status()),
		Balance:       bal,
		FailureCode:   tx.FailureCode(),
	})
}

// GetProviderTransaction handles GET /providers/{providerId}/wagering/transactions/{externalTransactionId}
func (h *TransactionHandler) GetProviderTransaction(w http.ResponseWriter, r *http.Request) {
	providerID := strings.TrimSpace(r.PathValue("providerId"))
	externalID := strings.TrimSpace(r.PathValue("externalTransactionId"))

	if providerID == "" || externalID == "" {
		writeError(w, http.StatusBadRequest, "missing_parameters", "providerId and externalTransactionId are required")
		return
	}

	// Provider Tenancy Check
	identity, ok := auth.GetAuthIdentity(r.Context())
	if ok && !identity.IsInternal() {
		if identity.ProviderID != providerID {
			writeError(w, http.StatusForbidden, "provider_tenancy_mismatch", "access denied to other provider transaction")
			return
		}
	}

	tx, err := h.txRepo.GetByProviderAndExternalID(r.Context(), providerID, externalID)
	if err != nil {
		if errors.Is(err, repository.ErrTransactionNotFound) {
			writeError(w, http.StatusNotFound, "transaction_not_found", "transaction not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	var bal domain.Money
	if tx.BalanceSnapshot() != nil {
		bal = *tx.BalanceSnapshot()
	} else {
		bal, _ = domain.MoneyZero(tx.Money().Currency())
	}

	writeJSON(w, http.StatusOK, TransactionResponse{
		TransactionID: tx.ID(),
		Status:        string(tx.Status()),
		Balance:       bal,
		FailureCode:   tx.FailureCode(),
	})
}

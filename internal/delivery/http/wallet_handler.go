package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/domain"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/postgres/repository"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/usecase"
)

type WalletHandler struct {
	openWalletUC     *usecase.OpenWalletUseCase
	reconciliationUC *usecase.ReconciliationUseCase
	walletRepo       *repository.WalletRepository
	ledgerRepo       *repository.LedgerRepository
}

func NewWalletHandler(
	openWalletUC *usecase.OpenWalletUseCase,
	reconciliationUC *usecase.ReconciliationUseCase,
	walletRepo *repository.WalletRepository,
	ledgerRepo *repository.LedgerRepository,
) *WalletHandler {
	return &WalletHandler{
		openWalletUC:     openWalletUC,
		reconciliationUC: reconciliationUC,
		walletRepo:       walletRepo,
		ledgerRepo:       ledgerRepo,
	}
}

// OpenWallet handles POST /wallets
func (h *WalletHandler) OpenWallet(w http.ResponseWriter, r *http.Request) {
	var req OpenWalletRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "failed to parse JSON request body")
		return
	}

	req.PlayerID = strings.TrimSpace(req.PlayerID)
	if req.PlayerID == "" {
		writeError(w, http.StatusBadRequest, "invalid_player_id", "playerId is required")
		return
	}

	initialBalance, err := domain.NewNonNegativeMoneyFromDecimal(req.InitialBalance.Amount, req.InitialBalance.Currency)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_initial_balance", err.Error())
		return
	}

	out, err := h.openWalletUC.Execute(r.Context(), usecase.OpenWalletInput{
		PlayerID:       req.PlayerID,
		InitialBalance: initialBalance,
	})
	if err != nil {
		if errors.Is(err, usecase.ErrWalletAlreadyExists) {
			writeError(w, http.StatusConflict, "wallet_already_exists", "a wallet already exists for this player and currency")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, WalletResponse{
		ID:       out.ID,
		PlayerID: out.PlayerID,
		Balance:  out.Balance,
		Version:  out.Version,
	})
}

// GetWallet handles GET /wallets/{walletId}
func (h *WalletHandler) GetWallet(w http.ResponseWriter, r *http.Request) {
	walletID := strings.TrimSpace(r.PathValue("walletId"))
	if walletID == "" {
		writeError(w, http.StatusBadRequest, "missing_wallet_id", "walletId is required")
		return
	}

	wallet, err := h.walletRepo.GetByID(r.Context(), walletID)
	if err != nil {
		if errors.Is(err, repository.ErrWalletNotFound) {
			writeError(w, http.StatusNotFound, "wallet_not_found", "wallet not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, WalletResponse{
		ID:       wallet.ID(),
		PlayerID: wallet.PlayerID(),
		Balance:  wallet.Balance(),
		Version:  wallet.Version(),
	})
}

// GetLedger handles GET /wallets/{walletId}/ledger
func (h *WalletHandler) GetLedger(w http.ResponseWriter, r *http.Request) {
	walletID := strings.TrimSpace(r.PathValue("walletId"))
	if walletID == "" {
		writeError(w, http.StatusBadRequest, "missing_wallet_id", "walletId is required")
		return
	}

	cursor := r.URL.Query().Get("cursor")
	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}

	entries, nextCursor, err := h.ledgerRepo.ListByWallet(r.Context(), walletID, cursor, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	items := make([]LedgerItemDTO, 0, len(entries))
	for _, e := range entries {
		items = append(items, LedgerItemDTO{
			ID:            e.ID(),
			TransactionID: e.TransactionID(),
			Direction:     string(e.Direction()),
			Amount:        e.Amount(),
			BalanceBefore: e.BalanceBefore(),
			BalanceAfter:  e.BalanceAfter(),
			CreatedAt:     e.CreatedAt().Format(time.RFC3339Nano),
		})
	}

	writeJSON(w, http.StatusOK, LedgerListResponse{
		Items:      items,
		NextCursor: nextCursor,
	})
}

// Reconcile handles POST /wallets/{walletId}/reconciliation
func (h *WalletHandler) Reconcile(w http.ResponseWriter, r *http.Request) {
	walletID := strings.TrimSpace(r.PathValue("walletId"))
	if walletID == "" {
		writeError(w, http.StatusBadRequest, "missing_wallet_id", "walletId is required")
		return
	}

	out, err := h.reconciliationUC.Execute(r.Context(), walletID)
	if err != nil {
		if errors.Is(err, repository.ErrWalletNotFound) {
			writeError(w, http.StatusNotFound, "wallet_not_found", "wallet not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, out)
}

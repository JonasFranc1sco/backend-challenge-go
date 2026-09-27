package domain

import (
	"fmt"
	"strings"
	"time"
)

// TransactionKind represents the type of financial or game operation.
type TransactionKind string

const (
	KindOpening  TransactionKind = "OPENING"
	KindBet      TransactionKind = "BET"
	KindWin      TransactionKind = "WIN"
	KindLoss     TransactionKind = "LOSS"
	KindRefund   TransactionKind = "REFUND"
	KindRollback TransactionKind = "ROLLBACK"
)

func (k TransactionKind) IsValid() bool {
	switch k {
	case KindOpening, KindBet, KindWin, KindLoss, KindRefund, KindRollback:
		return true
	default:
		return false
	}
}

// TransactionStatus represents the lifecycle state of a transaction.
type TransactionStatus string

const (
	StatusPending          TransactionStatus = "PENDING"
	StatusPendingReference TransactionStatus = "PENDING_REFERENCE"
	StatusProcessed        TransactionStatus = "PROCESSED"
	StatusRejected         TransactionStatus = "REJECTED"
	StatusFailed           TransactionStatus = "FAILED"
)

func (s TransactionStatus) IsTerminal() bool {
	switch s {
	case StatusProcessed, StatusRejected, StatusFailed:
		return true
	default:
		return false
	}
}

// TransactionOrigin distinguishes internal platform operations from external game provider operations.
type TransactionOrigin string

const (
	OriginInternal TransactionOrigin = "INTERNAL"
	OriginExternal TransactionOrigin = "EXTERNAL"
)

// WagerTransaction represents a wagering or balance operation.
// It tracks external and internal identifiers, idempotency data, state machine, and financial results.
type WagerTransaction struct {
	id                              string
	origin                          TransactionOrigin
	providerID                      string
	externalTransactionID           string
	idempotencyKey                  string
	payloadHash                     string
	walletID                        string
	playerID                        string
	roundID                         string
	gameID                          string
	kind                            TransactionKind
	money                           Money
	referenceExternalTransactionID  string
	referenceInternalTransactionID  string
	status                          TransactionStatus
	failureCode                     string
	balanceSnapshot                 *Money
	createdAt                       time.Time
	updatedAt                       time.Time
}

// ExternalTransactionParams holds parameters to create an external transaction.
type ExternalTransactionParams struct {
	ID                             string
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PayloadHash                    string
	WalletID                       string
	PlayerID                       string
	RoundID                        string
	GameID                         string
	Kind                           TransactionKind
	Money                          Money
	ReferenceExternalTransactionID string
	Now                            time.Time
}

// NewExternalTransaction constructs a new external WagerTransaction in PENDING status.
// Enforces that OPENING is rejected and validates all mandatory metadata and kind-specific constraints.
func NewExternalTransaction(params ExternalTransactionParams) (*WagerTransaction, error) {
	if params.Kind == KindOpening {
		return nil, ErrOpeningNotAllowedExternally
	}
	if !params.Kind.IsValid() {
		return nil, fmt.Errorf("%w: '%s'", ErrInvalidTransactionKind, params.Kind)
	}

	id := strings.TrimSpace(params.ID)
	providerID := strings.TrimSpace(params.ProviderID)
	externalID := strings.TrimSpace(params.ExternalTransactionID)
	idempotencyKey := strings.TrimSpace(params.IdempotencyKey)
	payloadHash := strings.TrimSpace(params.PayloadHash)
	walletID := strings.TrimSpace(params.WalletID)
	playerID := strings.TrimSpace(params.PlayerID)
	roundID := strings.TrimSpace(params.RoundID)
	gameID := strings.TrimSpace(params.GameID)
	refExtID := strings.TrimSpace(params.ReferenceExternalTransactionID)

	if id == "" || providerID == "" || externalID == "" || idempotencyKey == "" ||
		payloadHash == "" || walletID == "" || playerID == "" || roundID == "" || gameID == "" {
		return nil, ErrExternalMetadataMissing
	}

	// Kind-specific validations
	switch params.Kind {
	case KindBet, KindWin:
		if !params.Money.IsPositive() {
			return nil, fmt.Errorf("%w: %s requires positive money amount", ErrZeroAmountNotAllowed, params.Kind)
		}
	case KindLoss:
		if !params.Money.IsZero() {
			return nil, fmt.Errorf("%w: LOSS received amount '%s'", ErrLossAmountMustBeZero, params.Money.String())
		}
	case KindRefund, KindRollback:
		if !params.Money.IsPositive() {
			return nil, fmt.Errorf("%w: %s requires positive money amount", ErrZeroAmountNotAllowed, params.Kind)
		}
		if refExtID == "" {
			return nil, fmt.Errorf("%w: %s requires referenceExternalTransactionId", ErrMissingReference, params.Kind)
		}
	}

	now := params.Now.UTC()
	return &WagerTransaction{
		id:                             id,
		origin:                         OriginExternal,
		providerID:                     providerID,
		externalTransactionID:          externalID,
		idempotencyKey:                 idempotencyKey,
		payloadHash:                    payloadHash,
		walletID:                       walletID,
		playerID:                       playerID,
		roundID:                        roundID,
		gameID:                         gameID,
		kind:                           params.Kind,
		money:                          params.Money,
		referenceExternalTransactionID: refExtID,
		status:                         StatusPending,
		createdAt:                      now,
		updatedAt:                      now,
	}, nil
}

// NewOpeningTransaction constructs an internal OPENING transaction created on positive balance wallet initialization.
// The transaction is immediately PROCESSED with a balance snapshot equal to the opening money.
func NewOpeningTransaction(id, walletID, playerID string, initialBalance Money, now time.Time) (*WagerTransaction, error) {
	id = strings.TrimSpace(id)
	walletID = strings.TrimSpace(walletID)
	playerID = strings.TrimSpace(playerID)

	if id == "" || walletID == "" || playerID == "" {
		return nil, fmt.Errorf("%w: id, walletID and playerID cannot be empty", ErrWalletNotInitialized)
	}
	if !initialBalance.IsPositive() {
		return nil, fmt.Errorf("%w: OPENING requires strictly positive initial balance", ErrZeroAmountNotAllowed)
	}

	snapshot := initialBalance
	utcNow := now.UTC()

	return &WagerTransaction{
		id:              id,
		origin:          OriginInternal,
		walletID:        walletID,
		playerID:        playerID,
		kind:            KindOpening,
		money:           initialBalance,
		status:          StatusProcessed,
		balanceSnapshot: &snapshot,
		createdAt:       utcNow,
		updatedAt:       utcNow,
	}, nil
}

// RehydrateTransaction restores a WagerTransaction from persistent storage.
func RehydrateTransaction(
	id string,
	origin TransactionOrigin,
	providerID, externalTransactionID, idempotencyKey, payloadHash string,
	walletID, playerID, roundID, gameID string,
	kind TransactionKind,
	money Money,
	referenceExternalID, referenceInternalID string,
	status TransactionStatus,
	failureCode string,
	balanceSnapshot *Money,
	createdAt, updatedAt time.Time,
) (*WagerTransaction, error) {
	return &WagerTransaction{
		id:                             id,
		origin:                         origin,
		providerID:                     providerID,
		externalTransactionID:          externalTransactionID,
		idempotencyKey:                 idempotencyKey,
		payloadHash:                    payloadHash,
		walletID:                       walletID,
		playerID:                       playerID,
		roundID:                        roundID,
		gameID:                         gameID,
		kind:                           kind,
		money:                          money,
		referenceExternalTransactionID: referenceExternalID,
		referenceInternalTransactionID: referenceInternalID,
		status:                         status,
		failureCode:                    failureCode,
		balanceSnapshot:                balanceSnapshot,
		createdAt:                      createdAt.UTC(),
		updatedAt:                      updatedAt.UTC(),
	}, nil
}

// State Machine Transitions

// MarkProcessed transitions the transaction to PROCESSED (terminal).
func (t *WagerTransaction) MarkProcessed(balanceSnapshot Money, now time.Time) error {
	if t.status.IsTerminal() {
		return fmt.Errorf("%w: transaction '%s' is in terminal state '%s'", ErrTerminalState, t.id, t.status)
	}
	t.status = StatusProcessed
	t.balanceSnapshot = &balanceSnapshot
	t.updatedAt = now.UTC()
	return nil
}

// MarkRejected transitions the transaction to REJECTED (terminal) with a business failure code.
func (t *WagerTransaction) MarkRejected(failureCode string, now time.Time) error {
	if t.status.IsTerminal() {
		return fmt.Errorf("%w: transaction '%s' is in terminal state '%s'", ErrTerminalState, t.id, t.status)
	}
	t.status = StatusRejected
	t.failureCode = strings.TrimSpace(failureCode)
	t.updatedAt = now.UTC()
	return nil
}

// MarkFailed transitions the transaction to FAILED (terminal) for permanent infrastructure failures.
func (t *WagerTransaction) MarkFailed(failureCode string, now time.Time) error {
	if t.status.IsTerminal() {
		return fmt.Errorf("%w: transaction '%s' is in terminal state '%s'", ErrTerminalState, t.id, t.status)
	}
	t.status = StatusFailed
	t.failureCode = strings.TrimSpace(failureCode)
	t.updatedAt = now.UTC()
	return nil
}

// MarkPendingReference transitions a PENDING transaction to PENDING_REFERENCE waiting for its reference.
func (t *WagerTransaction) MarkPendingReference(now time.Time) error {
	if t.status.IsTerminal() {
		return fmt.Errorf("%w: transaction '%s' is in terminal state '%s'", ErrTerminalState, t.id, t.status)
	}
	if t.status != StatusPending {
		return fmt.Errorf("%w: cannot transition from '%s' to PENDING_REFERENCE", ErrInvalidStateTransition, t.status)
	}
	t.status = StatusPendingReference
	t.updatedAt = now.UTC()
	return nil
}

// ResolveInternalReference associates the resolved internal transaction ID.
func (t *WagerTransaction) ResolveInternalReference(refInternalID string) {
	t.referenceInternalTransactionID = refInternalID
}

// Getters

func (t *WagerTransaction) ID() string                             { return t.id }
func (t *WagerTransaction) Origin() TransactionOrigin               { return t.origin }
func (t *WagerTransaction) ProviderID() string                     { return t.providerID }
func (t *WagerTransaction) ExternalTransactionID() string          { return t.externalTransactionID }
func (t *WagerTransaction) IdempotencyKey() string                 { return t.idempotencyKey }
func (t *WagerTransaction) PayloadHash() string                    { return t.payloadHash }
func (t *WagerTransaction) WalletID() string                       { return t.walletID }
func (t *WagerTransaction) PlayerID() string                       { return t.playerID }
func (t *WagerTransaction) RoundID() string                        { return t.roundID }
func (t *WagerTransaction) GameID() string                         { return t.gameID }
func (t *WagerTransaction) Kind() TransactionKind                  { return t.kind }
func (t *WagerTransaction) Money() Money                           { return t.money }
func (t *WagerTransaction) ReferenceExternalTransactionID() string { return t.referenceExternalTransactionID }
func (t *WagerTransaction) ReferenceInternalTransactionID() string { return t.referenceInternalTransactionID }
func (t *WagerTransaction) Status() TransactionStatus              { return t.status }
func (t *WagerTransaction) FailureCode() string                    { return t.failureCode }
func (t *WagerTransaction) BalanceSnapshot() *Money                { return t.balanceSnapshot }
func (t *WagerTransaction) CreatedAt() time.Time                   { return t.createdAt }
func (t *WagerTransaction) UpdatedAt() time.Time                   { return t.updatedAt }

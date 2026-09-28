package usecase

import (
	"errors"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/domain"
)

var (
	ErrIdempotencyKeyConflict   = errors.New("idempotency key reused with different payload")
	ErrExternalIDAlreadyExists = errors.New("external transaction ID already registered with a different idempotency key")
	ErrWalletAlreadyExists     = errors.New("wallet already exists for this player and currency")
	ErrWalletNotFound          = errors.New("wallet not found")
)

// OpenWalletInput represents the input to open a player wallet.
type OpenWalletInput struct {
	PlayerID       string
	InitialBalance domain.Money
}

// OpenWalletOutput represents the result of opening a wallet.
type OpenWalletOutput struct {
	ID       string       `json:"id"`
	PlayerID string       `json:"playerId"`
	Balance  domain.Money `json:"balance"`
	Version  int64        `json:"version"`
}

// ProcessWagerInput represents the input for a wagering transaction (HTTP or SQS).
type ProcessWagerInput struct {
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PlayerID                       string
	WalletID                       string
	RoundID                        string
	GameID                         string
	Kind                           domain.TransactionKind
	Money                          domain.Money
	ReferenceExternalTransactionID string
	CorrelationID                  string
}

// ProcessWagerOutput represents the result returned to the caller.
type ProcessWagerOutput struct {
	TransactionID    string                 `json:"transactionId"`
	Status           domain.TransactionStatus `json:"status"`
	Balance          domain.Money           `json:"balance"`
	IdempotentReplay bool                   `json:"idempotentReplay"`
	FailureCode      string                 `json:"failureCode,omitempty"`
}

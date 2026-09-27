package domain

import (
	"encoding/json"
	"time"
)

// EventType defines standard names for domain events.
type EventType string

const (
	EventWagerTransactionProcessed        EventType = "WagerTransactionProcessed"
	EventWagerTransactionRejected         EventType = "WagerTransactionRejected"
	EventWalletBalanceChanged             EventType = "WalletBalanceChanged"
	EventWagerTransactionPendingReference EventType = "WagerTransactionPendingReference"
)

// EventEnvelope provides a standardized container for published domain events.
type EventEnvelope struct {
	EventID       string          `json:"eventId"`
	EventType     EventType       `json:"eventType"`
	AggregateID   string          `json:"aggregateId"`
	CorrelationID string          `json:"correlationId"`
	CausationID   string          `json:"causationId,omitempty"`
	OccurredAt    string          `json:"occurredAt"` // RFC 3339 UTC string
	Version       int             `json:"version"`
	Data          json.RawMessage `json:"data"`
}

// WalletBalanceChangedPayload represents the event payload emitted on every effective balance movement.
type WalletBalanceChangedPayload struct {
	WalletID      string `json:"walletId"`
	TransactionID string `json:"transactionId"`
	Direction     string `json:"direction"`
	Money         Money  `json:"money"`
	BalanceBefore Money  `json:"balanceBefore"`
	BalanceAfter  Money  `json:"balanceAfter"`
	WalletVersion int64  `json:"walletVersion"`
}

// WagerTransactionProcessedPayload represents the payload for successful operations, including LOSS.
type WagerTransactionProcessedPayload struct {
	TransactionID         string `json:"transactionId"`
	Origin                string `json:"origin"`
	ProviderID            string `json:"providerId,omitempty"`
	ExternalTransactionID string `json:"externalTransactionId,omitempty"`
	WalletID              string `json:"walletId"`
	PlayerID              string `json:"playerId"`
	RoundID               string `json:"roundId,omitempty"`
	GameID                string `json:"gameId,omitempty"`
	Kind                  string `json:"kind"`
	Money                 Money  `json:"money"`
	BalanceSnapshot       *Money `json:"balanceSnapshot,omitempty"`
}

// WagerTransactionRejectedPayload represents the payload for rejected operations.
type WagerTransactionRejectedPayload struct {
	TransactionID         string `json:"transactionId"`
	ProviderID            string `json:"providerId"`
	ExternalTransactionID string `json:"externalTransactionId"`
	WalletID              string `json:"walletId"`
	PlayerID              string `json:"playerId"`
	RoundID               string `json:"roundId"`
	GameID                string `json:"gameId"`
	Kind                  string `json:"kind"`
	Money                 Money  `json:"money"`
	FailureCode           string `json:"failureCode"`
}

// WagerTransactionPendingReferencePayload represents the payload when a reversal awaits its reference.
type WagerTransactionPendingReferencePayload struct {
	TransactionID                  string `json:"transactionId"`
	ProviderID                     string `json:"providerId"`
	ExternalTransactionID          string `json:"externalTransactionId"`
	ReferenceExternalTransactionID string `json:"referenceExternalTransactionId"`
	WalletID                       string `json:"walletId"`
	PlayerID                       string `json:"playerId"`
	RoundID                        string `json:"roundId"`
	Kind                           string `json:"kind"`
	Money                          Money  `json:"money"`
}

// NewEventEnvelope wraps typed event payload into an EventEnvelope.
func NewEventEnvelope(
	eventID string,
	eventType EventType,
	aggregateID string,
	correlationID string,
	causationID string,
	occurredAt time.Time,
	version int,
	payload any,
) (*EventEnvelope, error) {
	dataBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	return &EventEnvelope{
		EventID:       eventID,
		EventType:     eventType,
		AggregateID:   aggregateID,
		CorrelationID: correlationID,
		CausationID:   causationID,
		OccurredAt:    occurredAt.UTC().Format(time.RFC3339Nano),
		Version:       version,
		Data:          dataBytes,
	}, nil
}

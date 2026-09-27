package domain_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/domain"
)

func TestEvents_EnvelopeSerialization(t *testing.T) {
	now := time.Date(2026, 9, 27, 20, 0, 0, 0, time.UTC)
	m25, _ := domain.NewMoneyFromDecimal("25.00", "BRL")
	mBefore, _ := domain.NewMoneyFromDecimal("1000.00", "BRL")
	mAfter, _ := domain.NewMoneyFromDecimal("975.00", "BRL")

	payload := domain.WalletBalanceChangedPayload{
		WalletID:      "wallet-123",
		TransactionID: "tx-456",
		Direction:     "DEBIT",
		Money:         m25,
		BalanceBefore: mBefore,
		BalanceAfter:  mAfter,
		WalletVersion: 2,
	}

	env, err := domain.NewEventEnvelope(
		"evt-001",
		domain.EventWalletBalanceChanged,
		"wallet-123",
		"corr-789",
		"caus-101",
		now,
		1,
		payload,
	)
	if err != nil {
		t.Fatalf("unexpected error creating envelope: %v", err)
	}

	envBytes, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("unexpected error marshalling envelope: %v", err)
	}

	rawJSON := string(envBytes)
	if !strings.Contains(rawJSON, `"amount":"25.00"`) {
		t.Errorf("expected serialized amount 25.00 in payload, got %s", rawJSON)
	}
	if !strings.Contains(rawJSON, `"eventType":"WalletBalanceChanged"`) {
		t.Errorf("expected eventType WalletBalanceChanged, got %s", rawJSON)
	}
	if !strings.Contains(rawJSON, `"occurredAt":"2026-09-27T20:00:00Z"`) {
		t.Errorf("expected RFC3339 timestamp, got %s", rawJSON)
	}
}

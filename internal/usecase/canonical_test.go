package usecase_test

import (
	"testing"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/domain"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/usecase"
)

func TestCanonicalPayloadHash_Determinism(t *testing.T) {
	m25, _ := domain.NewMoneyFromDecimal("25.00", "BRL")

	p1 := usecase.CanonicalPayload{
		ProviderID:            "provider-a",
		ExternalTransactionID: "tx-123",
		PlayerID:              "player-456",
		WalletID:              "wallet-789",
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Kind:                  "BET",
		Money:                 m25,
	}

	p2 := usecase.CanonicalPayload{
		ProviderID:            "provider-a",
		ExternalTransactionID: "tx-123",
		PlayerID:              "player-456",
		WalletID:              "wallet-789",
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Kind:                  "BET",
		Money:                 m25,
	}

	h1, err := usecase.ComputeCanonicalPayloadHash(p1)
	if err != nil {
		t.Fatalf("unexpected error computing hash: %v", err)
	}
	h2, err := usecase.ComputeCanonicalPayloadHash(p2)
	if err != nil {
		t.Fatalf("unexpected error computing hash: %v", err)
	}

	if h1 != h2 {
		t.Errorf("expected deterministic identical hash, got %s and %s", h1, h2)
	}

	if len(h1) != 64 {
		t.Errorf("expected 64-char sha256 hex string, got len %d", len(h1))
	}

	// Change amount -> different hash
	mDiff, _ := domain.NewMoneyFromDecimal("25.01", "BRL")
	pDiff := p1
	pDiff.Money = mDiff
	hDiff, err := usecase.ComputeCanonicalPayloadHash(pDiff)
	if err != nil {
		t.Fatalf("unexpected error on diff: %v", err)
	}

	if h1 == hDiff {
		t.Errorf("expected different hashes for different amounts, got identical: %s", h1)
	}
}
